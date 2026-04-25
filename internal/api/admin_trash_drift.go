// Package api — drift-purge restore handler (v1.5 Phase 6 / 06-07).
//
// handleDriftRestore is the kind-dispatch target for the four
// <proto>_drift trash kinds. The generic /admin/trash/{id}/restore
// route forwards to it before the file-only path so the row repo
// gets re-inserted (UPSERT per D-04) before the on-disk file is
// moved back.
package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"

	"github.com/dxc-internal/omnirepo/internal/audit"
	"github.com/dxc-internal/omnirepo/internal/auth"
	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/storage"
)

// handleDriftRestore restores a single drift-purge trash entry: validate
// sidecar row_snapshot → resolve repo_id → UPSERT row in a write tx →
// move file back via Trash.Restore → emit audit. Returns
//   409 restore.conflict.repo_missing — when snapshot.repo_id refs an
//        absent or soft-deleted repo (D-05).
//   500 ErrInternal — sidecar malformed, row repo unconfigured, or
//        UPSERT/file-move failure.
//   200 {"status": "ok"} — success.
func (d Deps) handleDriftRestore(
	w http.ResponseWriter,
	r *http.Request,
	e storage.TrashEntry,
	id string,
) {
	if e.RowSnapshot == nil {
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal,
			"drift trash entry is missing row_snapshot sidecar field")
		return
	}
	var snap map[string]any
	if err := json.Unmarshal(e.RowSnapshot, &snap); err != nil {
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal,
			"row_snapshot sidecar parse failed: "+err.Error())
		return
	}
	repoIDF, ok := snap["repo_id"].(float64)
	if !ok {
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal,
			"row_snapshot missing repo_id")
		return
	}
	repoID := int64(repoIDF)

	// Validate repo_id present + live.
	if d.Repos == nil {
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal,
			"repos store not configured")
		return
	}
	repo, rerr := d.Repos.FindByID(r.Context(), repoID)
	if errors.Is(rerr, metadata.ErrNotFound) || repo == nil ||
		(rerr == nil && repo != nil && repo.DeletedAt != nil) {
		writeJSONError(w, r, http.StatusConflict, codeRestoreConflictRepoMissing,
			"source repo no longer exists; purge the trash entry or re-create the repo first")
		return
	}
	if rerr != nil {
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal,
			"repo lookup failed: "+rerr.Error())
		return
	}

	// Resolve childPath + dstPath from sidecar OriginalPath (F-14.6 pattern).
	if e.OriginalPath == "" {
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal,
			"drift trash entry missing original_path")
		return
	}
	childPath := filepath.Join(e.Path, filepath.Base(e.OriginalPath))
	dstPath := e.OriginalPath

	// Pre-check destination collision so the operator gets a clean 409
	// rather than a generic 500 from Trash.Restore.
	if _, statErr := os.Stat(dstPath); statErr == nil {
		writeJSONError(w, r, http.StatusConflict, ErrConflict,
			"destination "+dstPath+" already exists; purge the live item or rename it before restoring")
		return
	}

	// Codex Phase-6 review fix (Q2): file move FIRST, then UPSERT.
	// Reverse order leaves a window where a successful UPSERT followed
	// by a failed file move produces a row pointing at a missing file
	// (visible to operators as a 404-on-download). With this order, a
	// failed file move returns 500 cleanly with no DB pollution; a
	// failed UPSERT after a successful file move leaves an orphan file
	// at the destination, which the GC sweep cleans up later (worst
	// case: wasted disk, no broken row visible to users).
	if d.DB == nil {
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal,
			"db not configured")
		return
	}

	// Move the file back FIRST.
	if err := d.Trash.Restore(r.Context(), childPath, dstPath); err != nil {
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal,
			"trash file restore failed: "+err.Error())
		return
	}

	// Per-kind UPSERT inside a single write tx.
	if err := d.DB.WriteTx(r.Context(), func(tx *sql.Tx) error {
		switch e.Kind {
		case "pypi_file_drift":
			if d.PyPIFiles == nil {
				return errors.New("pypi_files repo not configured")
			}
			f := rebuildPyPIFile(snap)
			_, err := d.PyPIFiles.Insert(r.Context(), tx, &f)
			return err
		case "rpm_package_drift":
			if d.RPMPackages == nil {
				return errors.New("rpm_packages repo not configured")
			}
			p := rebuildRPMPackage(snap)
			_, err := d.RPMPackages.Insert(r.Context(), tx, &p)
			return err
		case "deb_package_drift":
			if d.DEBPackages == nil {
				return errors.New("deb_packages repo not configured")
			}
			p := rebuildDEBPackage(snap)
			_, err := d.DEBPackages.Insert(r.Context(), tx, &p)
			return err
		case "helm_chart_drift":
			if d.HelmCharts == nil {
				return errors.New("helm_charts repo not configured")
			}
			c := rebuildHelmChart(snap)
			_, err := d.HelmCharts.Insert(r.Context(), tx, &c)
			return err
		}
		return errors.New("unknown drift kind: " + e.Kind)
	}); err != nil {
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal,
			"restore row UPSERT failed: "+err.Error())
		return
	}

	if a, ok := auth.ActorFromContext(r.Context()); ok {
		uid := a.ID
		d.recordAudit(r, audit.Event{
			Kind:        audit.EvtRepoUpdated,
			ActorUserID: &uid,
			TargetKind:  "trash",
			TargetID:    id,
			Outcome:     "restored_drift",
		})
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// snapStr extracts a string field from a json-decoded snapshot map. Missing
// or wrong-typed keys produce empty strings — Insert callers validate
// non-empty fields themselves and will surface a clear error.
func snapStr(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

// snapInt64 extracts a numeric field from a json-decoded snapshot map.
// JSON numbers always decode to float64 in Go's encoding/json default mode;
// SQLite INTEGER columns fit safely in float64's 53-bit mantissa for any
// realistic id/size in this codebase.
func snapInt64(m map[string]any, key string) int64 {
	if v, ok := m[key].(float64); ok {
		return int64(v)
	}
	return 0
}

// snapInt extracts an integer column (e.g. RPMPackage.Epoch).
func snapInt(m map[string]any, key string) int {
	if v, ok := m[key].(float64); ok {
		return int(v)
	}
	return 0
}

// rebuildPyPIFile rebuilds a metadata.PyPIFile from a sidecar snapshot.
// UploadedAt and ID are NOT restored — Insert assigns a new ID and the
// UploadedAt timestamp; the historical UploadedAt is informational and
// not required for correctness.
func rebuildPyPIFile(snap map[string]any) metadata.PyPIFile {
	return metadata.PyPIFile{
		RepoID:            snapInt64(snap, "repo_id"),
		ProjectNormalized: snapStr(snap, "project_normalized"),
		Version:           snapStr(snap, "version"),
		Filename:          snapStr(snap, "filename"),
		Kind:              snapStr(snap, "kind"),
		RequiresPython:    snapStr(snap, "requires_python"),
		SizeBytes:         snapInt64(snap, "size_bytes"),
		Digest:            snapStr(snap, "digest"),
		CoreMetadataJSON:  snapStr(snap, "core_metadata_json"),
	}
}

func rebuildRPMPackage(snap map[string]any) metadata.RPMPackage {
	return metadata.RPMPackage{
		RepoID:      snapInt64(snap, "repo_id"),
		Name:        snapStr(snap, "name"),
		Epoch:       snapInt(snap, "epoch"),
		Version:     snapStr(snap, "version"),
		Release:     snapStr(snap, "release"),
		Arch:        snapStr(snap, "arch"),
		Summary:     snapStr(snap, "summary"),
		Description: snapStr(snap, "description"),
		License:     snapStr(snap, "license"),
		URL:         snapStr(snap, "url"),
		SourceRPM:   snapStr(snap, "source_rpm"),
		SizeBytes:   snapInt64(snap, "size_bytes"),
		Digest:      snapStr(snap, "digest"),
		Filename:    snapStr(snap, "filename"),
	}
}

func rebuildDEBPackage(snap map[string]any) metadata.DEBPackage {
	return metadata.DEBPackage{
		RepoID:          snapInt64(snap, "repo_id"),
		SuiteID:         snapInt64(snap, "suite_id"),
		Package:         snapStr(snap, "package"),
		Version:         snapStr(snap, "version"),
		Architecture:    snapStr(snap, "architecture"),
		Maintainer:      snapStr(snap, "maintainer"),
		Section:         snapStr(snap, "section"),
		Priority:        snapStr(snap, "priority"),
		Depends:         snapStr(snap, "depends"),
		Description:     snapStr(snap, "description"),
		SizeBytes:       snapInt64(snap, "size_bytes"),
		Digest:          snapStr(snap, "digest"),
		Filename:        snapStr(snap, "filename"),
		StoragePoolPath: snapStr(snap, "storage_pool_path"),
	}
}

func rebuildHelmChart(snap map[string]any) metadata.HelmChart {
	return metadata.HelmChart{
		RepoID:          snapInt64(snap, "repo_id"),
		Name:            snapStr(snap, "name"),
		Version:         snapStr(snap, "version"),
		AppVersion:      snapStr(snap, "app_version"),
		Description:     snapStr(snap, "description"),
		KeywordsJSON:    snapStr(snap, "keywords_json"),
		MaintainersJSON: snapStr(snap, "maintainers_json"),
		SizeBytes:       snapInt64(snap, "size_bytes"),
		Digest:          snapStr(snap, "digest"),
		Filename:        snapStr(snap, "filename"),
	}
}
