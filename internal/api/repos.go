// Package api — repo mutation endpoints (Phase 02-11, D-34, D-35).
//
// PATCH /api/v1/projects/{name}/repos/{type}/{repo}         → REPO-05
// POST  /api/v1/projects/{name}/repos/{type}/{repo}/wipe    → REPO-07
//
// Both endpoints require project membership; super-admin always wins via
// auth.Can's step-2 bypass. Anonymous actors never reach here because the
// subrouter sits behind SessionOrAPIKey.
//
// The wipe handler runs its DB mutation inside a single writer transaction
// (so refcount decrements + DELETE rows commit atomically) and only moves the
// on-disk tree to trash AFTER the tx commits. If the trash.Move fails, we
// log a warning and return trash_id="" — DB state is already correct, and
// the orphan directory is swept by GC (Phase 02-12).
package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/dxc-internal/omnirepo/internal/audit"
	"github.com/dxc-internal/omnirepo/internal/auth"
	"github.com/dxc-internal/omnirepo/internal/metadata"
)

// maxRepoPatchBodyBytes caps PATCH bodies to keep description_md size sane.
// 64 KiB matches the upstream-creds cap and is more than enough for any
// sensible README blurb; larger descriptions should live in the Git/RAW repo
// itself.
const maxRepoPatchBodyBytes = 64 * 1024

// repoPatchRequest is the PATCH body shape. All fields optional; nil pointers
// leave the column untouched (see metadata.UpdateFields).
type repoPatchRequest struct {
	DescriptionMD   *string `json:"description_md,omitempty"`
	AutoScan        *bool   `json:"auto_scan,omitempty"`
	BlockOnSeverity *string `json:"block_on_severity,omitempty"`
	PublicRead      *bool   `json:"public_read,omitempty"`
}

// repoResponse mirrors the Repo row projected for the REST API. We do not
// echo soft-delete or size internals the PATCH handler never touches.
type repoResponse struct {
	ID              int64     `json:"id"`
	ProjectID       int64     `json:"project_id"`
	Type            string    `json:"type"`
	Name            string    `json:"name"`
	DescriptionMD   string    `json:"description_md"`
	AutoScan        bool      `json:"auto_scan"`
	BlockOnSeverity string    `json:"block_on_severity"`
	PublicRead      bool      `json:"public_read"`
	SizeBytes       int64     `json:"size_bytes"`
	CreatedAt       time.Time `json:"created_at"`
}

func repoToResponse(r metadata.Repo) repoResponse {
	return repoResponse{
		ID:              r.ID,
		ProjectID:       r.ProjectID,
		Type:            r.Type,
		Name:            r.Name,
		DescriptionMD:   r.DescriptionMD,
		AutoScan:        r.AutoScan,
		BlockOnSeverity: r.BlockOnSeverity,
		PublicRead:      r.PublicRead,
		SizeBytes:       r.SizeBytes,
		CreatedAt:       r.CreatedAt,
	}
}

// validBlockOnSeverity mirrors the DDL CHECK constraint (see 001_initial.up.sql).
var validBlockOnSeverity = map[string]struct{}{
	"none": {}, "low": {}, "medium": {}, "high": {}, "critical": {},
}

// resolveRepoFromURL is the shared lookup used by PATCH + wipe. Returns
// (project, repo, ok); on error it has already written a 404 body and ok=false.
func (d Deps) resolveRepoFromURL(w http.ResponseWriter, r *http.Request) (*metadata.Project, *metadata.Repo, bool) {
	projectName := chi.URLParam(r, "name")
	repoType := chi.URLParam(r, "type")
	repoName := chi.URLParam(r, "repo")
	if _, ok := validRepoTypes[repoType]; !ok {
		writeJSONError(w, http.StatusNotFound, ErrNotFound, "repo not found")
		return nil, nil, false
	}
	p, err := d.Projects.FindByName(r.Context(), projectName)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, ErrNotFound, "project not found")
		return nil, nil, false
	}
	rr, err := d.Repos.FindByTriple(r.Context(), p.ID, repoType, repoName)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, ErrNotFound, "repo not found")
		return nil, nil, false
	}
	return p, rr, true
}

// handleGetRepo returns a single repo's metadata. Used by the UI to render
// repo detail pages. Requires project membership; super-admin bypass applies.
// Public-read repos are still gated by membership — this endpoint carries the
// full administrative projection and should not leak through anonymous reads.
func (d Deps) handleGetRepo(w http.ResponseWriter, r *http.Request) {
	_, rr, ok := d.resolveRepoFromURL(w, r)
	if !ok {
		return
	}
	resp := repoToResponse(*rr)
	// F-5: repos.size_bytes is never written; overlay the live aggregate so
	// the repo header ("<name> · <size>") shows non-zero for repos that
	// actually contain artifacts.
	if sizes := d.liveRepoSizes(r.Context(), []int64{rr.ID}); len(sizes) > 0 {
		resp.SizeBytes = sizes[rr.ID]
	}
	writeJSON(w, http.StatusOK, resp)
}

// handlePatchRepo implements D-34. The per-route middleware already enforced
// project membership via RequireCanWith(ActionUpdateRepo), so any caller
// reaching here is authorized.
func (d Deps) handlePatchRepo(w http.ResponseWriter, r *http.Request) {
	_, before, ok := d.resolveRepoFromURL(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxRepoPatchBodyBytes)
	var body repoPatchRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, ErrValidationFailed, "invalid JSON")
		return
	}
	if body.BlockOnSeverity != nil {
		if _, ok := validBlockOnSeverity[*body.BlockOnSeverity]; !ok {
			writeJSONError(w, http.StatusBadRequest, ErrValidationFailed, "invalid block_on_severity")
			return
		}
	}

	// Compute the diff BEFORE the UPDATE so the audit row reflects
	// intended-state changes only. Unchanged fields (same value submitted as
	// already stored) are omitted.
	diff := map[string]any{}
	if body.DescriptionMD != nil && *body.DescriptionMD != before.DescriptionMD {
		diff["description_md"] = map[string]any{"from": before.DescriptionMD, "to": *body.DescriptionMD}
	}
	if body.AutoScan != nil && *body.AutoScan != before.AutoScan {
		diff["auto_scan"] = map[string]any{"from": before.AutoScan, "to": *body.AutoScan}
	}
	if body.BlockOnSeverity != nil && *body.BlockOnSeverity != before.BlockOnSeverity {
		diff["block_on_severity"] = map[string]any{"from": before.BlockOnSeverity, "to": *body.BlockOnSeverity}
	}
	if body.PublicRead != nil && *body.PublicRead != before.PublicRead {
		diff["public_read"] = map[string]any{"from": before.PublicRead, "to": *body.PublicRead}
	}

	var updated metadata.Repo
	err := d.DB.WriteTx(r.Context(), func(tx *sql.Tx) error {
		u, err := d.Repos.Update(r.Context(), tx, before.ID, metadata.UpdateFields{
			DescriptionMD:   body.DescriptionMD,
			AutoScan:        body.AutoScan,
			BlockOnSeverity: body.BlockOnSeverity,
			PublicRead:      body.PublicRead,
		})
		if err != nil {
			return err
		}
		updated = u
		return nil
	})
	if err != nil {
		if errors.Is(err, metadata.ErrNotFound) {
			writeJSONError(w, http.StatusNotFound, ErrNotFound, "repo not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, "")
		return
	}

	// Audit AFTER commit (audit.Logger.Record opens its own tx — matches the
	// pattern in handleCreateRepo/handleDeleteRepo). Only emit when the diff
	// is non-empty; a no-op PATCH is not an auditable state change.
	if len(diff) > 0 {
		if a, ok := auth.ActorFromContext(r.Context()); ok {
			uid := a.ID
			d.recordAudit(r, audit.Event{
				Kind:        audit.EvtRepoUpdated,
				ActorUserID: &uid,
				TargetKind:  "repo",
				TargetID:    strconv.FormatInt(updated.ID, 10),
				Details:     map[string]any{"diff": diff},
			})
		}
	}
	writeJSON(w, http.StatusOK, repoToResponse(updated))
}

// handleWipeRepo implements D-35. Only docker + raw types are supported in
// Phase 2; other types return 501.
func (d Deps) handleWipeRepo(w http.ResponseWriter, r *http.Request) {
	project, rr, ok := d.resolveRepoFromURL(w, r)
	if !ok {
		return
	}
	switch rr.Type {
	case "docker", "raw":
		// ok
	default:
		writeJSONError(w, http.StatusNotImplemented, "not_implemented",
			fmt.Sprintf("wipe not supported for type %q in Phase 2", rr.Type))
		return
	}

	var count, bytesFreed int64
	err := d.DB.WriteTx(r.Context(), func(tx *sql.Tx) error {
		switch rr.Type {
		case "docker":
			c, b, err := d.Repos.WipeDocker(r.Context(), tx, rr.ID)
			if err != nil {
				return err
			}
			count, bytesFreed = c, b
		case "raw":
			c, b, err := d.Repos.WipeRaw(r.Context(), tx, rr.ID)
			if err != nil {
				return err
			}
			count, bytesFreed = c, b
		}
		return nil
	})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, "")
		return
	}

	// Move the on-disk tree to trash AFTER the DB tx commits (non-transactional
	// filesystem op). We defend-in-depth-validate every URL-sourced path
	// segment before constructing the filesystem target, mirroring the
	// WR-06 pattern in handleDeleteRepo.
	trashID := ""
	safe := true
	if err := auth.ProjectNameValid(project.Name); err != nil {
		safe = false
	}
	if _, ok := validRepoTypes[rr.Type]; !ok {
		safe = false
	}
	if err := auth.ProjectNameValid(rr.Name); err != nil {
		safe = false
	}
	if safe && d.Trash != nil {
		onDisk := filepath.Join(d.DataRoot, "repos", project.Name, rr.Type, rr.Name)
		if tpath, terr := d.Trash.Move(r.Context(), onDisk, "repo", rr.ID); terr != nil {
			if !errors.Is(terr, context.Canceled) && !errors.Is(terr, os.ErrNotExist) {
				slog.WarnContext(r.Context(), "wipe: trash move failed",
					"repo_id", rr.ID, "err", terr)
			}
		} else {
			trashID = tpath
		}
	}

	if a, ok := auth.ActorFromContext(r.Context()); ok {
		uid := a.ID
		d.recordAudit(r, audit.Event{
			Kind:        audit.EvtRepoWiped,
			ActorUserID: &uid,
			TargetKind:  "repo",
			TargetID:    strconv.FormatInt(rr.ID, 10),
			Details: map[string]any{
				"artifact_count": count,
				"bytes_freed":    bytesFreed,
			},
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"artifact_count": count,
		"bytes_freed":    bytesFreed,
		"trash_id":       trashID,
	})
}
