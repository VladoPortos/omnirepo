package raw

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/dxc-internal/omnirepo/internal/audit"
	"github.com/dxc-internal/omnirepo/internal/auth"
	"github.com/dxc-internal/omnirepo/internal/metadata"
)


// put handles PUT /<project>/raw/<repo>/<path...>.
//
// Flow:
//  1. Resolve project + repo.
//  2. Validate path.
//  3. Authorize: actor must be authenticated AND a project member (super-admin
//     bypass via auth.Can). Anonymous never allowed for writes.
//  4. Stream body through MaxBytesReader -> sha256 TeeReader -> PathStore.Put
//     under repo-relative key "<project>/raw/<repo>/<path>".
//  5. In a single writer tx: Insert raw_files row, IndexArtifact (FTS5),
//     enqueue scan if repo.auto_scan, audit.
//  6. 201 Created with Location header.
func (h *Handler) put(w http.ResponseWriter, r *http.Request) {
	res, ok := h.resolveRepoAndPath(w, r, true)
	if !ok {
		return
	}

	actor, ok := auth.ActorFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthenticated", http.StatusUnauthorized)
		return
	}
	if !h.actorIsProjectMember(r.Context(), actor, res.project.ID) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	// Cap body. http.MaxBytesReader returns *MaxBytesError on overflow.
	r.Body = http.MaxBytesReader(w, r.Body, h.maxPutBytes)
	defer func() { _ = r.Body.Close() }()

	hasher := sha256.New()
	tee := io.TeeReader(r.Body, hasher)

	// PathStore key: <project>/raw/<repo>/<rel>. PathStore enforces
	// containment (no .. escapes), and our validateRawPath has already
	// rejected dotted segments — defense in depth.
	storageKey := storageKeyFor(res.project.Name, res.repo.Name, res.relPath)

	size, err := h.pathStore.Put(r.Context(), storageKey, tee)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		// Audit finding #9: never surface internal driver/path messages to
		// the client — log them server-side and return an opaque 500.
		slog.ErrorContext(r.Context(), "raw.put.storage_failed",
			"project", res.project.Name, "repo", res.repo.Name,
			"path", res.relPath, "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}

	digest := "sha256:" + hex.EncodeToString(hasher.Sum(nil))
	mimeType := detectMIMEFromExt(res.relPath)

	// Single writer tx: raw_files upsert + FTS5 + optional scan enqueue.
	if err := h.db.WriteTx(r.Context(), func(tx *sql.Tx) error {
		if err := h.files.Insert(r.Context(), tx, res.repo.ID, res.relPath, size, mimeType, digest); err != nil {
			return err
		}
		// FTS5 index — refresh by deleting any prior digest+repo entry,
		// then inserting a new one. The artifact key is path so search by
		// filename works (SRCH-01).
		if err := metadata.IndexArtifactDelete(r.Context(), tx, res.repo.ID, res.relPath); err != nil {
			return err
		}
		if err := metadata.IndexArtifact(r.Context(), tx, res.repo.ID, res.relPath, "", res.relPath); err != nil {
			return err
		}
		if res.repo.AutoScan && h.scans != nil {
			if _, err := h.scans.Enqueue(r.Context(), tx, res.repo.ID, "raw", res.relPath); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		// HI-02: DB commit failed after bytes were written to disk. Roll the
		// file back so we don't leak an orphan the metadata layer has no row
		// for. Best-effort: if the delete itself fails, log via audit below.
		_ = h.pathStore.Delete(r.Context(), storageKey)
		// Audit finding #9.
		slog.ErrorContext(r.Context(), "raw.put.commit_failed",
			"project", res.project.Name, "repo", res.repo.Name,
			"path", res.relPath, "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}

	// Audit. Best-effort.
	h.auditEvent(r, audit.EvtRawPut, res.repo, res.relPath, "ok", map[string]any{
		"project": res.project.Name,
		"repo":    res.repo.Name,
		"path":    res.relPath,
		"size":    size,
		"sha256":  digest,
		"mime":    mimeType,
	})

	w.Header().Set("Location", r.URL.Path)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusCreated)
}

// storageKeyFor builds the PathStore-relative key for a (project, repo, rel)
// triple. Always slash-separated regardless of host OS — PathStore translates
// to filepath.Separator internally.
func storageKeyFor(project, repo, rel string) string {
	parts := []string{project, "raw", repo}
	if rel != "" {
		parts = append(parts, rel)
	}
	return strings.Join(parts, "/")
}

// detectMIMEFromExt returns mime.TypeByExtension on the filename's extension,
// or "" when unknown. The caller falls back to http.DetectContentType (D-29).
func detectMIMEFromExt(relPath string) string {
	ext := filepath.Ext(relPath)
	if ext == "" {
		return ""
	}
	return mime.TypeByExtension(ext)
}

// actorIsProjectMember mirrors the policy decision auth.Can would make for
// repo writes, without needing an Action constant for raw operations.
// Super-admin bypasses the membership check.
//
// Project-owned API keys carry their scope in actor.ProjectScope.
// User-owned actors' membership is looked up via project_members.
func (h *Handler) actorIsProjectMember(ctx context.Context, actor auth.Actor, projectID int64) bool {
	if actor.Kind == auth.ActorKindAnonymous {
		return false
	}
	if actor.IsSuperAdmin {
		return true
	}
	if actor.Kind == auth.ActorKindAPIKey && actor.ProjectScope != nil {
		return *actor.ProjectScope == projectID
	}
	if actor.ID == 0 {
		return false
	}
	// User-scoped: query project_members directly.
	var n int
	err := h.db.Reader.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM project_members WHERE project_id=? AND user_id=?`,
		projectID, actor.ID,
	).Scan(&n)
	if err != nil {
		return false
	}
	return n > 0
}
