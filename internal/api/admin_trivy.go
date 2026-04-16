// Package api — admin Trivy DB management endpoints (Phase 05-03, SCAN-09/10/11).
//
// GET  /api/v1/admin/trivy/db/status  — Trivy DB metadata (SCAN-11)
// POST /api/v1/admin/trivy/db         — upload Trivy DB tarball (SCAN-09)
// POST /api/v1/admin/trivy/db/pull    — online Trivy DB pull (SCAN-10)
package api

import (
	"archive/tar"
	"compress/gzip"
	"database/sql"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/dxc-internal/omnirepo/internal/audit"
	"github.com/dxc-internal/omnirepo/internal/auth"
	authmw "github.com/dxc-internal/omnirepo/internal/auth/middleware"
)

// mountAdminTrivy installs Trivy DB admin endpoints on r.
func (d Deps) mountAdminTrivy(r chi.Router) {
	r.With(authmw.RequireCan(auth.ActionTriggerGC)).
		Get("/admin/trivy/db/status", d.handleTrivyDBStatus)
	r.With(authmw.RequireCan(auth.ActionTriggerGC)).
		Post("/admin/trivy/db", d.handleTrivyDBUpload)
	r.With(authmw.RequireCan(auth.ActionTriggerGC)).
		Post("/admin/trivy/db/pull", d.handleTrivyDBPull)
}

func (d Deps) handleTrivyDBStatus(w http.ResponseWriter, r *http.Request) {
	dbDir := filepath.Join(d.DataRoot, "trivy", "db")

	// Check trivy_db_meta table for the latest row.
	var version, source, appliedAt string
	var sizeBytes int64
	err := d.DB.Reader.QueryRowContext(r.Context(), `
		SELECT version, source, size_bytes, applied_at
		FROM trivy_db_meta
		ORDER BY id DESC LIMIT 1
	`).Scan(&version, &source, &sizeBytes, &appliedAt)

	if err == sql.ErrNoRows {
		// No meta row. Check if the DB directory has files (baked-in).
		entries, _ := os.ReadDir(dbDir)
		if len(entries) > 0 {
			writeJSON(w, http.StatusOK, map[string]any{
				"version":   "unknown",
				"source":    "baked-in",
				"age_hours": -1,
				"stale":     true,
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"version":   "",
			"source":    "none",
			"age_hours": -1,
			"stale":     true,
		})
		return
	}
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, "")
		return
	}

	// Calculate age and stale flag.
	applied, _ := time.Parse(time.RFC3339, appliedAt)
	ageHours := time.Since(applied).Hours()

	// Read stale threshold from settings (default 7 days).
	warnDays := 7
	if v, err := d.Settings.Get(r.Context(), "scan.db_warn_age_days"); err == nil {
		var n int
		if _, scanErr := fmt.Sscanf(v, "%d", &n); scanErr == nil && n > 0 {
			warnDays = n
		}
	}
	stale := ageHours > float64(warnDays*24)

	writeJSON(w, http.StatusOK, map[string]any{
		"version":    version,
		"source":     source,
		"size_bytes": sizeBytes,
		"applied_at": appliedAt,
		"age_hours":  math.Round(ageHours*100) / 100,
		"stale":      stale,
	})
}

func (d Deps) handleTrivyDBUpload(w http.ResponseWriter, r *http.Request) {
	// T-05-03-01: tarball extraction security.
	if err := r.ParseMultipartForm(512 << 20); err != nil { // 512 MiB max
		writeJSONError(w, http.StatusBadRequest, ErrValidationFailed, "multipart parse: "+err.Error())
		return
	}

	f, hdr, err := r.FormFile("db")
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, ErrValidationFailed, "missing 'db' file field")
		return
	}
	defer func() { _ = f.Close() }()

	// Validate it's gzip.
	gzr, err := gzip.NewReader(f)
	if err != nil {
		writeJSONError(w, http.StatusUnprocessableEntity, ErrValidationFailed, "not a valid gzip file")
		return
	}
	defer func() { _ = gzr.Close() }()

	// Extract to temp directory under DataRoot/tmp/.
	tmpDir, err := os.MkdirTemp(filepath.Join(d.DataRoot, "tmp"), "trivydb-*")
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, "")
		return
	}
	defer func() { _ = os.RemoveAll(tmpDir) }() // cleanup on failure

	const maxTotalExtracted = 2 << 30 // 2 GiB cumulative extraction limit

	tr := tar.NewReader(gzr)
	var totalSize int64
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			writeJSONError(w, http.StatusUnprocessableEntity, ErrValidationFailed, "tar read error")
			return
		}

		// T-05-03-01: path traversal prevention.
		clean := filepath.Clean(header.Name)
		if strings.Contains(clean, "..") || filepath.IsAbs(clean) {
			writeJSONError(w, http.StatusUnprocessableEntity, ErrValidationFailed, "path traversal in tar entry")
			return
		}

		target := filepath.Join(tmpDir, clean)
		// Ensure target is within tmpDir.
		if !strings.HasPrefix(target, tmpDir+string(filepath.Separator)) && target != tmpDir {
			writeJSONError(w, http.StatusUnprocessableEntity, ErrValidationFailed, "path escape in tar entry")
			return
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o750); err != nil {
				writeJSONError(w, http.StatusInternalServerError, ErrInternal, "")
				return
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
				writeJSONError(w, http.StatusInternalServerError, ErrInternal, "")
				return
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
			if err != nil {
				writeJSONError(w, http.StatusInternalServerError, ErrInternal, "")
				return
			}
			n, copyErr := io.Copy(out, io.LimitReader(tr, maxTotalExtracted-totalSize+1))
			_ = out.Close()
			if copyErr != nil {
				writeJSONError(w, http.StatusInternalServerError, ErrInternal, "")
				return
			}
			totalSize += n
			if totalSize > maxTotalExtracted {
				writeJSONError(w, http.StatusUnprocessableEntity, ErrValidationFailed, "extracted size exceeds 2 GiB limit")
				return
			}
		}
	}

	// Atomic swap: rename tmpDir to DataRoot/trivy/db/.
	dbDir := filepath.Join(d.DataRoot, "trivy", "db")
	if err := os.MkdirAll(filepath.Dir(dbDir), 0o750); err != nil {
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, "")
		return
	}
	// Remove old DB directory first.
	_ = os.RemoveAll(dbDir)
	if err := os.Rename(tmpDir, dbDir); err != nil {
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, "atomic swap failed: "+err.Error())
		return
	}

	// Insert trivy_db_meta row.
	now := time.Now().UTC().Format(time.RFC3339)
	var userID *int64
	if a, ok := auth.ActorFromContext(r.Context()); ok && a.ID != 0 {
		userID = &a.ID
	}
	_ = d.DB.WriteTx(r.Context(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(r.Context(), `
			INSERT INTO trivy_db_meta(version, source, size_bytes, applied_at, applied_by)
			VALUES (?, 'uploaded', ?, ?, ?)
		`, hdr.Filename, totalSize, now, userID)
		return err
	})

	// Audit.
	if a, ok := auth.ActorFromContext(r.Context()); ok {
		uid := a.ID
		d.recordAudit(r, audit.Event{
			Kind:        audit.EvtMaintenanceToggled, // reuse; could add EvtTrivyDBUploaded
			ActorUserID: &uid,
			TargetKind:  "trivy_db",
			TargetID:    hdr.Filename,
			Outcome:     "uploaded",
			Details: map[string]any{
				"size_bytes": totalSize,
			},
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":     "ok",
		"size_bytes": totalSize,
		"source":     "uploaded",
	})
}

func (d Deps) handleTrivyDBPull(w http.ResponseWriter, r *http.Request) {
	// Create temp cache dir for Trivy download.
	tmpDir, err := os.MkdirTemp(filepath.Join(d.DataRoot, "tmp"), "trivypull-*")
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, "")
		return
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	// Run trivy to download DB.
	cmd := exec.CommandContext(r.Context(), "trivy", "image", "--download-db-only", "--cache-dir", tmpDir)
	output, err := cmd.CombinedOutput()
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "network_unavailable",
			"Unable to reach the Trivy database server. Upload a DB tarball instead.")
		return
	}

	// Atomic swap: move downloaded DB to DataRoot/trivy/db/.
	// Trivy places the DB under <cache-dir>/db/
	srcDB := filepath.Join(tmpDir, "db")
	if _, err := os.Stat(srcDB); err != nil {
		// Some Trivy versions place it differently.
		srcDB = tmpDir
	}

	dbDir := filepath.Join(d.DataRoot, "trivy", "db")
	if err := os.MkdirAll(filepath.Dir(dbDir), 0o750); err != nil {
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, "")
		return
	}
	_ = os.RemoveAll(dbDir)
	if err := os.Rename(srcDB, dbDir); err != nil {
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, "swap failed")
		return
	}

	// Insert trivy_db_meta row.
	now := time.Now().UTC().Format(time.RFC3339)
	var userID *int64
	if a, ok := auth.ActorFromContext(r.Context()); ok && a.ID != 0 {
		userID = &a.ID
	}
	_ = d.DB.WriteTx(r.Context(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(r.Context(), `
			INSERT INTO trivy_db_meta(version, source, size_bytes, applied_at, applied_by)
			VALUES ('online', 'online-pulled', 0, ?, ?)
		`, now, userID)
		return err
	})

	// Audit.
	if a, ok := auth.ActorFromContext(r.Context()); ok {
		uid := a.ID
		d.recordAudit(r, audit.Event{
			Kind:        audit.EvtMaintenanceToggled,
			ActorUserID: &uid,
			TargetKind:  "trivy_db",
			Outcome:     "online-pulled",
		})
	}

	_ = output // suppress unused warning
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok",
		"source": "online-pulled",
	})
}
