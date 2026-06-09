package npm

import (
	"crypto/sha1" //nolint:gosec // npm's wire format mandates sha1 shasums
	"crypto/sha512"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/vladoportos/omnirepo/internal/audit"
	"github.com/vladoportos/omnirepo/internal/auth"
	"github.com/vladoportos/omnirepo/internal/metadata"
)

// publishBody mirrors the JSON document `npm publish` PUTs: the package
// name, the dist-tags being set, exactly one versions entry, and the
// tarball base64-inlined under _attachments.
type publishBody struct {
	Name        string                     `json:"name"`
	DistTags    map[string]string          `json:"dist-tags"`
	Versions    map[string]json.RawMessage `json:"versions"`
	Attachments map[string]struct {
		ContentType string `json:"content_type"`
		Data        string `json:"data"`
		Length      int64  `json:"length"`
	} `json:"_attachments"`
}

// versionDist is the subset of a version manifest the server verifies.
type versionDist struct {
	Description string `json:"description"`
	Dist        struct {
		Shasum    string `json:"shasum"`
		Integrity string `json:"integrity"`
	} `json:"dist"`
}

// publish handles PUT /<name> — `npm publish`.
//
// Flow:
//  1. Resolve repo; auth (maintainer-required, ActionNPMUpload).
//  2. Decode the publish JSON (body capped by MaxBytesReader).
//  3. Exactly one version + one attachment expected; name fields must
//     agree with the URL.
//  4. Decode the base64 tarball, compute sha1 + sha512; verify against
//     the manifest's dist entries when the client supplied them.
//  5. Immutability: existing (name, version) → 403 (registry semantics).
//  6. Write tarball via PathStore; writer tx: npm_packages insert +
//     dist-tag upserts + artifacts_fts.
//  7. Audit EvtNPMUpload → 201.
func (h *Handler) publish(w http.ResponseWriter, r *http.Request) {
	res, ok := h.resolveRepo(w, r)
	if !ok {
		return
	}
	if res.req.Op != "publish" {
		http.Error(w, "malformed publish path", http.StatusBadRequest)
		return
	}
	actor, ok := auth.ActorFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthenticated", http.StatusUnauthorized)
		return
	}
	if !h.requireRepoWrite(r.Context(), actor, res.project.ID, auth.ActionNPMUpload) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, h.maxPutBytes)
	defer func() { _ = r.Body.Close() }()

	var body publishBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "invalid publish body", http.StatusBadRequest)
		return
	}
	if body.Name != res.req.Name {
		http.Error(w, "package name does not match URL", http.StatusBadRequest)
		return
	}
	if len(body.Versions) != 1 || len(body.Attachments) != 1 {
		http.Error(w, "publish body must carry exactly one version and one attachment", http.StatusBadRequest)
		return
	}

	var version string
	var manifestRaw json.RawMessage
	for v, m := range body.Versions {
		version, manifestRaw = v, m
	}
	if err := validateVersion(version); err != nil {
		http.Error(w, "invalid version", http.StatusBadRequest)
		return
	}
	var dist versionDist
	_ = json.Unmarshal(manifestRaw, &dist)

	var attName string
	var attData string
	for n, a := range body.Attachments {
		attName, attData = n, a.Data
	}
	wantFile := tarballName(res.req.Name, version)
	// npm names the attachment "<name>-<version>.tgz" (scoped packages
	// keep the @scope/ prefix in the attachment key); accept both the
	// full and the basename spelling.
	if attName != wantFile && !strings.HasSuffix(attName, "/"+wantFile) && attName != res.req.Name+"-"+version+".tgz" {
		http.Error(w, "attachment name does not match version", http.StatusBadRequest)
		return
	}
	tarball, err := base64.StdEncoding.DecodeString(attData)
	if err != nil || len(tarball) == 0 {
		http.Error(w, "invalid attachment data", http.StatusBadRequest)
		return
	}

	sum1 := sha1.Sum(tarball) //nolint:gosec // npm wire format
	shasum := hex.EncodeToString(sum1[:])
	sum512 := sha512.Sum512(tarball)
	integrity := "sha512-" + base64.StdEncoding.EncodeToString(sum512[:])
	if dist.Dist.Shasum != "" && dist.Dist.Shasum != shasum {
		http.Error(w, "shasum mismatch between attachment and manifest", http.StatusBadRequest)
		return
	}
	if dist.Dist.Integrity != "" && dist.Dist.Integrity != integrity {
		http.Error(w, "integrity mismatch between attachment and manifest", http.StatusBadRequest)
		return
	}

	// Immutability: never publish over an existing version.
	if _, err := h.packages.FindByNameVersion(r.Context(), res.repo.ID, res.req.Name, version); err == nil {
		h.auditEvent(r, audit.EvtNPMUpload, res.req.Name+"@"+version, "rejected", map[string]any{
			"project": res.project.Name,
			"repo":    res.repo.Name,
			"reason":  "version_exists",
		})
		http.Error(w, "cannot publish over existing version", http.StatusForbidden)
		return
	}

	storageKey := storageKeyFor(res.project.Name, res.repo.Name, res.req.Name, wantFile)
	if _, err := h.pathStore.Put(r.Context(), storageKey, strings.NewReader(string(tarball))); err != nil {
		slog.ErrorContext(r.Context(), "npm.publish.storage_failed",
			slog.String("incident_id", chimw.GetReqID(r.Context())),
			slog.String("package", res.req.Name),
			slog.Any("err", err),
		)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}

	if err := h.db.WriteTx(r.Context(), func(tx *sql.Tx) error {
		if _, err := h.packages.Insert(r.Context(), tx, &metadata.NPMPackage{
			RepoID:      res.repo.ID,
			Name:        res.req.Name,
			Version:     version,
			Description: dist.Description,
			VersionJSON: string(manifestRaw),
			Tarball:     wantFile,
			SizeBytes:   int64(len(tarball)),
			Shasum:      shasum,
			Integrity:   integrity,
		}); err != nil {
			return err
		}
		for tag, v := range body.DistTags {
			if v != version || tag == "" {
				continue // tags may only point at the version being published
			}
			if err := h.packages.SetDistTag(r.Context(), tx, res.repo.ID, res.req.Name, tag, version); err != nil {
				return err
			}
		}
		if err := metadata.IndexArtifactDelete(r.Context(), tx, res.repo.ID, integrity); err != nil {
			return err
		}
		return metadata.IndexArtifact(r.Context(), tx, res.repo.ID, res.req.Name, version, integrity)
	}); err != nil {
		_ = h.pathStore.Delete(r.Context(), storageKey)
		slog.ErrorContext(r.Context(), "npm.publish.commit_failed",
			slog.String("incident_id", chimw.GetReqID(r.Context())),
			slog.String("package", res.req.Name),
			slog.Any("err", err),
		)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}

	h.auditEvent(r, audit.EvtNPMUpload, res.req.Name+"@"+version, "ok", map[string]any{
		"project":    res.project.Name,
		"repo":       res.repo.Name,
		"package":    res.req.Name,
		"version":    version,
		"size_bytes": len(tarball),
	})

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "id": res.req.Name})
}
