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
	"bytes"
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

// Phase 8 Plan 01 (MIRROR-01..07): canonical envelope codes emitted by
// CreateRepo and PatchRepo mirror-validation branches. Dotted wire form
// satisfies the envelope schema regex (^[a-z][a-z0-9_]*\.[a-z][a-z0-9_]*$)
// while the second segment keeps the operator-facing token verbatim so
// tests, docs, and grep-based plan-check assertions resolve directly.
const (
	codeRepoMirrorTypeUnsupported  = "repo.mirror_type_unsupported"
	codeRepoMirrorURLInvalid       = "repo.mirror_url_invalid"
	codeRepoMirrorFilterInvalid    = "repo.mirror_filter_invalid"
	codeRepoMirrorURLImmutable     = "repo.mirror_url_immutable"
	codeRepoMirrorCredWrongProject = "repo.mirror_cred_wrong_project"
	// codeRepoDriftPurgeMirrorOnly (v1.5 Phase 6 / DRIFTPURGE-04, D-17):
	// drift_purge=true is only meaningful on mirror repos. A non-mirror
	// repo has no upstream to diff against; accepting the flag would
	// be a silent no-op. Reject explicitly so operators learn the
	// invariant at call time.
	codeRepoDriftPurgeMirrorOnly = "repo.drift_purge_mirror_only"
)

// repoPatchRequest is the PATCH body shape. All fields optional; nil pointers
// leave the column untouched (see metadata.UpdateFields).
//
// Phase 8 Plan 01 (MIRROR-02, MIRROR-07): IsMirror / MirrorUpstreamURL are
// accepted on the wire but always rejected with 400 repo.mirror_url_immutable
// so the wire shape matches CreateRepoRequest and the UI library can reuse
// one TypeScript interface for both requests. The three editable fields
// (MirrorFilter / MirrorCredID / ScanOnSync) flow through to UpdateFields.
type repoPatchRequest struct {
	DescriptionMD   *string `json:"description_md,omitempty"`
	AutoScan        *bool   `json:"auto_scan,omitempty"`
	BlockOnSeverity *string `json:"block_on_severity,omitempty"`
	PublicRead      *bool   `json:"public_read,omitempty"`

	IsMirror          *bool            `json:"is_mirror,omitempty"`
	MirrorUpstreamURL *string          `json:"mirror_upstream_url,omitempty"`
	MirrorFilter      *json.RawMessage `json:"mirror_filter,omitempty"`
	// MirrorCredIDRaw uses non-pointer json.RawMessage so the handler can
	// distinguish "field absent" (len=0) from "set to null" (4-byte
	// `null`) from "set to int" — mirroring UpdateFields.MirrorCredIDSet
	// semantics. A pointer to json.RawMessage would NOT work for the
	// null case because encoding/json nils a *json.RawMessage when the
	// JSON value is null, collapsing "absent" and "null" into the same
	// state (Go pointer semantics). Fixed in plan 11-03 Task 3 so the
	// Docker Hub cred-gate can distinguish "patch leaves cred alone"
	// from "patch clears cred".
	MirrorCredIDRaw json.RawMessage `json:"mirror_cred_id,omitempty"`
	ScanOnSync      *bool           `json:"scan_on_sync,omitempty"`
	// DriftPurge (v1.5 Phase 6 / DRIFTPURGE-04, D-17): mirror-only opt-in.
	// PATCH'ing drift_purge=true on a non-mirror repo is rejected with
	// codeRepoDriftPurgeMirrorOnly. drift_purge=false is always allowed.
	DriftPurge *bool `json:"drift_purge,omitempty"`
}

// repoResponse mirrors the Repo row projected for the REST API. We do not
// echo soft-delete or size internals the PATCH handler never touches.
//
// Phase 8 Plan 04 (MIRROR-16..21): mirror fields are echoed on GET so the
// UI can render the Sync Now button + Mirror config card conditionally on
// `is_mirror`. `mirror_filter_json` is a raw JSON string (TEXT column) —
// the UI parses it into the protocol-specific SyncFilter shape (PascalCase
// wire keys matching the Go SyncFilter struct fields in
// internal/protocol/{deb,rpm,pypi,helm}/upstream_parse.go). `mirror_cred_id`
// is a nullable pointer so JSON emits `null` (not omitted) when unset —
// lets the UI distinguish "no cred configured" from "field missing".
type repoResponse struct {
	ID              int64  `json:"id"`
	ProjectID       int64  `json:"project_id"`
	Type            string `json:"type"`
	Name            string `json:"name"`
	DescriptionMD   string `json:"description_md"`
	AutoScan        bool   `json:"auto_scan"`
	BlockOnSeverity string `json:"block_on_severity"`
	PublicRead      bool   `json:"public_read"`
	SizeBytes       int64  `json:"size_bytes"`
	// F-T15: ItemCount renders "42 packages · 180 MB" in the repo header.
	// Meaning depends on type — see repoItemCountExpr. 0 is a valid empty
	// repo; callers can suppress the badge if desired.
	ItemCount int64     `json:"item_count"`
	CreatedAt time.Time `json:"created_at"`

	// Phase 8 Plan 04 (MIRROR-16..21) mirror fields.
	IsMirror          bool   `json:"is_mirror"`
	MirrorUpstreamURL string `json:"mirror_upstream_url"`
	MirrorFilterJSON  string `json:"mirror_filter_json"`
	MirrorCredID      *int64 `json:"mirror_cred_id"`
	ScanOnSync        bool   `json:"scan_on_sync"`
	// DriftPurge (v1.5 Phase 6 / DRIFTPURGE-04, D-17): per-mirror opt-in
	// for drift purge on sync. Non-mirror repos always serialize false.
	DriftPurge bool `json:"drift_purge"`
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

		IsMirror:          r.IsMirror,
		MirrorUpstreamURL: r.MirrorUpstreamURL,
		MirrorFilterJSON:  r.MirrorFilterJSON,
		MirrorCredID:      r.MirrorCredID,
		ScanOnSync:        r.ScanOnSync,
		DriftPurge:        r.DriftPurge,
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
		writeJSONError(w, r, http.StatusNotFound, ErrNotFound, "repo not found")
		return nil, nil, false
	}
	p, err := d.Projects.FindByName(r.Context(), projectName)
	if err != nil {
		writeJSONError(w, r, http.StatusNotFound, ErrNotFound, "project not found")
		return nil, nil, false
	}
	rr, err := d.Repos.FindByTriple(r.Context(), p.ID, repoType, repoName)
	if err != nil {
		writeJSONError(w, r, http.StatusNotFound, ErrNotFound, "repo not found")
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
	// F-T15: overlay live item count so header renders "42 packages · 180 MB".
	if counts := d.liveRepoItemCounts(r.Context(), []int64{rr.ID}); len(counts) > 0 {
		resp.ItemCount = counts[rr.ID]
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
		writeJSONError(w, r, http.StatusBadRequest, ErrValidationFailed, "invalid JSON")
		return
	}
	if body.BlockOnSeverity != nil {
		if _, ok := validBlockOnSeverity[*body.BlockOnSeverity]; !ok {
			writeJSONError(w, r, http.StatusBadRequest, ErrValidationFailed, "invalid block_on_severity")
			return
		}
	}

	// Phase 8 Plan 01 (MIRROR-02): reject attempts to change is_mirror or
	// mirror_upstream_url via PATCH. The constants below (codeRepoMirror*)
	// live at the top of this file so grep-based plan-check assertions
	// resolve against a single canonical source.
	if body.IsMirror != nil || body.MirrorUpstreamURL != nil {
		writeJSONError(w, r, http.StatusBadRequest, codeRepoMirrorURLImmutable,
			"is_mirror and mirror_upstream_url cannot be changed after creation")
		return
	}
	// v1.5 Phase 6 (DRIFTPURGE-04, D-17): drift_purge=true is mirror-only.
	// Non-mirror repos have no upstream diff to trigger purge against;
	// reject explicitly with a typed envelope rather than silently storing
	// a flag that has no effect. drift_purge=false is always allowed (no-op
	// on non-mirror; idempotent on mirror).
	if body.DriftPurge != nil && *body.DriftPurge && !before.IsMirror {
		writeJSONError(w, r, http.StatusBadRequest, codeRepoDriftPurgeMirrorOnly,
			"drift_purge=true is only valid on mirror repos")
		return
	}
	// Validate editable mirror fields.
	var patchFilterStr *string
	if body.MirrorFilter != nil && before.IsMirror {
		ok, canonical := validateMirrorFilter(before.Type, *body.MirrorFilter)
		if !ok {
			writeJSONError(w, r, http.StatusBadRequest, codeRepoMirrorFilterInvalid,
				"mirror_filter JSON does not match the protocol SyncFilter shape")
			return
		}
		s := string(canonical)
		patchFilterStr = &s
	}
	// MirrorCredID editing: distinguish absent / null / int.
	var patchCredID *int64
	var patchCredIDSet bool
	if len(body.MirrorCredIDRaw) > 0 {
		raw := bytes.TrimSpace(body.MirrorCredIDRaw)
		if len(raw) == 0 || string(raw) == "null" {
			patchCredIDSet = true // clear to NULL
		} else {
			var v int64
			if err := json.Unmarshal(raw, &v); err != nil {
				writeJSONError(w, r, http.StatusBadRequest, ErrValidationFailed,
					"mirror_cred_id must be an integer or null")
				return
			}
			// Cross-project ownership check (T-08-01-07). before.ProjectID
			// is populated by resolveRepoFromURL above.
			if ok, _ := mirrorCredOwnership(r.Context(), d.UpstreamCreds, before.ProjectID, v); !ok {
				writeJSONError(w, r, http.StatusBadRequest, codeRepoMirrorCredWrongProject,
					"mirror_cred_id must belong to the same project as the repo")
				return
			}
			patchCredID = &v
			patchCredIDSet = true
		}
	}

	// Phase 11 Plan 03 Task 3 (OCIHELM-05 / D-04): Docker Hub cred gate on
	// PATCH. The invariant is post-patch: after this PATCH commits, the
	// mirror MUST NOT be (Docker Hub oci://, no cred). The effective
	// upstream URL is immutable (see earlier rejection branch), so we
	// compute the effective cred_id as patch value if set, else the
	// existing row's value, then resolve its kind. The gate always runs
	// when conditions apply; UpstreamCreds is only required to resolve
	// the cred's kind (nil cred-id alone is enough to trip the gate).
	if before.Type == "helm" && before.IsMirror {
		effectiveCredID := before.MirrorCredID
		if patchCredIDSet {
			effectiveCredID = patchCredID
		}
		credKind := ""
		if effectiveCredID != nil && *effectiveCredID > 0 && d.UpstreamCreds != nil {
			if cm, cerr := d.UpstreamCreds.Get(r.Context(), before.ProjectID, *effectiveCredID); cerr == nil && cm != nil {
				credKind = string(cm.Kind)
			}
		}
		if env := refuseDockerHubWithoutCred(before.MirrorUpstreamURL, credKind); env != nil {
			writeEnvelope(w, r, env)
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
	if patchFilterStr != nil && *patchFilterStr != before.MirrorFilterJSON {
		diff["mirror_filter_json"] = map[string]any{"from": before.MirrorFilterJSON, "to": *patchFilterStr}
	}
	if patchCredIDSet {
		diff["mirror_cred_id"] = map[string]any{"from": before.MirrorCredID, "to": patchCredID}
	}
	if body.ScanOnSync != nil && *body.ScanOnSync != before.ScanOnSync {
		diff["scan_on_sync"] = map[string]any{"from": before.ScanOnSync, "to": *body.ScanOnSync}
	}
	if body.DriftPurge != nil && *body.DriftPurge != before.DriftPurge {
		diff["drift_purge"] = map[string]any{"from": before.DriftPurge, "to": *body.DriftPurge}
	}

	var updated metadata.Repo
	err := d.DB.WriteTx(r.Context(), func(tx *sql.Tx) error {
		u, err := d.Repos.Update(r.Context(), tx, before.ID, metadata.UpdateFields{
			DescriptionMD:    body.DescriptionMD,
			AutoScan:         body.AutoScan,
			BlockOnSeverity:  body.BlockOnSeverity,
			PublicRead:       body.PublicRead,
			MirrorFilterJSON: patchFilterStr,
			MirrorCredID:     patchCredID,
			MirrorCredIDSet:  patchCredIDSet,
			ScanOnSync:       body.ScanOnSync,
			DriftPurge:       body.DriftPurge,
		})
		if err != nil {
			return err
		}
		updated = u
		return nil
	})
	if err != nil {
		if errors.Is(err, metadata.ErrNotFound) {
			writeJSONError(w, r, http.StatusNotFound, ErrNotFound, "repo not found")
			return
		}
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
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
		writeJSONError(w, r, http.StatusNotImplemented, "not_implemented",
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
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
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
		if tpath, terr := d.Trash.Move(r.Context(), onDisk, "repo", rr.ID, auth.ActorLoginFromContext(r.Context())); terr != nil {
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
