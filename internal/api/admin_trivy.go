// Package api — admin Trivy DB management endpoints (Phase 05-03, SCAN-09/10/11).
//
// GET  /api/v1/admin/trivy/db/status       — Trivy DB metadata (SCAN-11)
// POST /api/v1/admin/trivy/db              — upload Trivy DB tarball (SCAN-09)
// POST /api/v1/admin/trivy/db/pull         — start online Trivy DB pull (SCAN-10, async)
// GET  /api/v1/admin/trivy/db/pull/status  — progress of in-flight pull
// GET  /api/v1/admin/trivy/db/history      — applied Trivy DB updates, newest first
package api

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/dxc-internal/omnirepo/internal/audit"
	"github.com/dxc-internal/omnirepo/internal/auth"
	authmw "github.com/dxc-internal/omnirepo/internal/auth/middleware"
	"github.com/dxc-internal/omnirepo/internal/storage"
)

// minTrivyDBBytes is the lower-bound size check applied to the extracted
// trivy.db file before the live DB is rotated in. F-13.1 — the upload
// handler previously accepted any valid .tar.gz and SwapDir-rotated its
// contents into place, so uploading an innocuous archive (a backup, a
// documentation tarball) wiped the scanner with no undo path. Every real
// Trivy release ships a BoltDB file well over a hundred megabytes; 1 MiB
// is a wide margin under that floor while still rejecting every accidental
// non-Trivy upload we tested.
const minTrivyDBBytes int64 = 1 << 20

// boltDBMagic is the 4-byte magic at offset 16 of every BoltDB file, in
// little-endian on-disk form. Checking it before SwapDir is cheap defense
// in depth on top of the size + filename guard — a 1 MiB non-BoltDB file
// would still pass the rest of the validator but fail catastrophically
// the first time the scanner tried to open it, with no clear rollback.
var boltDBMagic = []byte{0xed, 0xda, 0x0c, 0xed}

// trivyRotateMu serializes writes to the live Trivy DB directory across
// all admin endpoints (upload + online pull). Two concurrent SwapDir
// calls would race on the rename-aside / rename-in steps and could
// transiently leave the scanner pointing at a missing dir or orphan a
// backup dir. `trivyPullJob` already prevents pull+pull overlap, but it
// does not coordinate with upload. A single package-level mutex held
// from pre-swap-validation through SwapDir covers every rotation path.
var trivyRotateMu sync.Mutex

// trivyMetadata is the subset of Trivy's metadata.json we read to surface
// the actual DB version in trivy_db_meta. Unused fields are intentionally
// omitted — Trivy adds keys between minor releases and we don't want to
// reject future tarballs just because they ship extra metadata.
type trivyMetadata struct {
	Version      int    `json:"Version"`
	NextUpdate   string `json:"NextUpdate"`
	UpdatedAt    string `json:"UpdatedAt"`
	DownloadedAt string `json:"DownloadedAt"`
}

// validateExtractedTrivyDB returns an error describing why dir is not a
// usable Trivy DB. The extracted metadata (when present and parseable) is
// also returned so the caller can stamp a meaningful version string on
// the trivy_db_meta row instead of the upload filename.
//
// Enforced shape (matches github.com/aquasecurity/trivy-db release tarballs):
//   - trivy.db regular file at the root, ≥ minTrivyDBBytes.
//   - metadata.json, if present, must parse as JSON.
func validateExtractedTrivyDB(dir string) (*trivyMetadata, error) {
	dbPath := filepath.Join(dir, "trivy.db")
	info, err := os.Stat(dbPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, errors.New("tarball does not contain trivy.db at the root")
		}
		return nil, errors.New("tarball trivy.db: stat failed")
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("tarball trivy.db is not a regular file")
	}
	if info.Size() < minTrivyDBBytes {
		return nil, fmt.Errorf("tarball trivy.db is too small to be a Trivy database (%d bytes)", info.Size())
	}
	// Cheap BoltDB magic-byte sniff — defence in depth on top of the
	// size + filename checks. A file that passes the size floor but is
	// not actually a BoltDB database would fail deep inside the scanner
	// on first use with no clear rotation path.
	if err := verifyBoltDBMagic(dbPath); err != nil {
		return nil, err
	}

	mdPath := filepath.Join(dir, "metadata.json")
	if _, err := os.Stat(mdPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// metadata.json is conventional but not strictly required by
			// the scanner; accept tarballs without it.
			return nil, nil
		}
		return nil, errors.New("tarball metadata.json: stat failed")
	}
	raw, err := os.ReadFile(mdPath)
	if err != nil {
		return nil, errors.New("tarball metadata.json: read failed")
	}
	var md trivyMetadata
	if err := json.Unmarshal(raw, &md); err != nil {
		return nil, errors.New("tarball metadata.json is not valid JSON")
	}
	return &md, nil
}

// verifyBoltDBMagic reads the first 20 bytes of path and checks for the
// BoltDB magic number at offset 16. Real BoltDB files (which Trivy uses)
// write the magic into the first page header at that fixed offset. A
// mismatch means either the file is too small to be a DB or it's the
// wrong file type — in either case we refuse to rotate it in.
func verifyBoltDBMagic(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return errors.New("tarball trivy.db: open failed")
	}
	defer func() { _ = f.Close() }()
	buf := make([]byte, 20)
	n, err := io.ReadFull(f, buf)
	if err != nil || n != len(buf) {
		return errors.New("tarball trivy.db: short read on header")
	}
	// The on-disk BoltDB page header stores the magic in little-endian
	// byte order; compare raw bytes rather than decoding a uint32 so the
	// check doesn't depend on endianness of the host.
	if !bytes.Equal(buf[16:20], boltDBMagic) {
		return errors.New("tarball trivy.db is not a valid BoltDB file")
	}
	return nil
}

// trivyPullJob tracks an in-flight Trivy DB pull so the frontend can poll
// progress instead of waiting on a long-lived request that just spins.
// Single-flight: only one pull runs at a time process-wide.
type trivyPullJob struct {
	mu         sync.Mutex
	state      string // "idle" | "running" | "success" | "failure"
	startedAt  time.Time
	finishedAt time.Time
	bytes      atomic.Int64 // live byte count from tmpDir size sampler
	errorMsg   string
}

// pullJob is the package-global single-flight tracker. Handlers mutate
// state under pullJob.mu; bytes is atomic so the sampler goroutine can
// update it lock-free.
var pullJob = &trivyPullJob{state: "idle"}

// dirSize walks path and sums file sizes. Silently returns whatever was
// counted before an error — used both by the live sampler and by the
// status endpoint, so partial counts beat nothing.
func dirSize(path string) int64 {
	var total int64
	_ = filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}

// trivyDBDir resolves the directory the admin handlers use for the Trivy
// scanner DB. Operator overrides via cfg.Trivy.DBPath come in through
// Deps.TrivyDBDir; the fallback matches the historical hardcoded layout so
// existing installs and tests keep working.
func (d Deps) trivyDBDir() string {
	if d.TrivyDBDir != "" {
		return d.TrivyDBDir
	}
	return filepath.Join(d.DataRoot, "trivy", "db")
}

// mountAdminTrivy installs Trivy DB admin endpoints on r.
func (d Deps) mountAdminTrivy(r chi.Router) {
	r.With(authmw.RequireCan(auth.ActionTriggerGC)).
		Get("/admin/trivy/db/status", d.handleTrivyDBStatus)
	r.With(authmw.RequireCan(auth.ActionTriggerGC)).
		Post("/admin/trivy/db", d.handleTrivyDBUpload)
	r.With(authmw.RequireCan(auth.ActionTriggerGC)).
		Post("/admin/trivy/db/pull", d.handleTrivyDBPull)
	r.With(authmw.RequireCan(auth.ActionTriggerGC)).
		Get("/admin/trivy/db/pull/status", d.handleTrivyDBPullStatus)
	r.With(authmw.RequireCan(auth.ActionTriggerGC)).
		Get("/admin/trivy/db/history", d.handleTrivyDBHistory)
}

func (d Deps) handleTrivyDBStatus(w http.ResponseWriter, r *http.Request) {
	dbDir := d.trivyDBDir()

	// Disk size is sourced from the live directory, not from the trivy_db_meta
	// row, because (a) online-pulled rows historically recorded size=0, and
	// (b) operators who edit/trim the DB on disk should see the real size.
	diskBytes := dirSize(dbDir)

	// Check trivy_db_meta table for the latest row.
	var version, source, appliedAt string
	var metaSize int64
	err := d.DB.Reader.QueryRowContext(r.Context(), `
		SELECT version, source, size_bytes, applied_at
		FROM trivy_db_meta
		ORDER BY id DESC LIMIT 1
	`).Scan(&version, &source, &metaSize, &appliedAt)

	if err == sql.ErrNoRows {
		// No meta row. Check if the DB directory has files (baked-in).
		entries, _ := os.ReadDir(dbDir)
		if len(entries) > 0 {
			writeJSON(w, http.StatusOK, map[string]any{
				"version":    "unknown",
				"source":     "baked-in",
				"age_hours":  -1,
				"stale":      true,
				"size_bytes": diskBytes,
				"path":       dbDir,
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"version":    "",
			"source":     "none",
			"age_hours":  -1,
			"stale":      true,
			"size_bytes": int64(0),
			"path":       dbDir,
		})
		return
	}
	if err != nil {
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
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

	// Prefer live disk size; fall back to the meta row size only when the
	// directory has been removed/emptied (diskBytes == 0 but we have a row).
	reportedSize := diskBytes
	if reportedSize == 0 {
		reportedSize = metaSize
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"version":    version,
		"source":     source,
		"size_bytes": reportedSize,
		"applied_at": appliedAt,
		"age_hours":  math.Round(ageHours*100) / 100,
		"stale":      stale,
		"path":       dbDir,
	})
}

func (d Deps) handleTrivyDBUpload(w http.ResponseWriter, r *http.Request) {
	// T-05-03-01: tarball extraction security.
	if err := r.ParseMultipartForm(512 << 20); err != nil { // 512 MiB max
		writeJSONError(w, r, http.StatusBadRequest, ErrValidationFailed, "multipart parse: "+err.Error())
		return
	}

	f, hdr, err := r.FormFile("db")
	if err != nil {
		writeJSONError(w, r, http.StatusBadRequest, ErrValidationFailed, "missing 'db' file field")
		return
	}
	defer func() { _ = f.Close() }()

	// Validate it's gzip.
	gzr, err := gzip.NewReader(f)
	if err != nil {
		writeJSONError(w, r, http.StatusUnprocessableEntity, ErrValidationFailed, "not a valid gzip file")
		return
	}
	defer func() { _ = gzr.Close() }()

	// Extract to temp directory under DataRoot/tmp/.
	tmpDir, err := os.MkdirTemp(filepath.Join(d.DataRoot, "tmp"), "trivydb-*")
	if err != nil {
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
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
			writeJSONError(w, r, http.StatusUnprocessableEntity, ErrValidationFailed, "tar read error")
			return
		}

		// T-05-03-01: path traversal prevention.
		clean := filepath.Clean(header.Name)
		if strings.Contains(clean, "..") || filepath.IsAbs(clean) {
			writeJSONError(w, r, http.StatusUnprocessableEntity, ErrValidationFailed, "path traversal in tar entry")
			return
		}

		target := filepath.Join(tmpDir, clean)
		// Ensure target is within tmpDir.
		if !strings.HasPrefix(target, tmpDir+string(filepath.Separator)) && target != tmpDir {
			writeJSONError(w, r, http.StatusUnprocessableEntity, ErrValidationFailed, "path escape in tar entry")
			return
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o750); err != nil {
				writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
				return
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
				writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
				return
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
			if err != nil {
				writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
				return
			}
			n, copyErr := io.Copy(out, io.LimitReader(tr, maxTotalExtracted-totalSize+1))
			_ = out.Close()
			if copyErr != nil {
				writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
				return
			}
			totalSize += n
			if totalSize > maxTotalExtracted {
				writeJSONError(w, r, http.StatusUnprocessableEntity, ErrValidationFailed, "extracted size exceeds 2 GiB limit")
				return
			}
		}
	}

	// F-13.1: verify the extracted tarball actually contains a Trivy DB
	// before SwapDir deletes the live scanner state. Without this gate an
	// operator could rotate any innocuous .tar.gz into place (a backup,
	// an HTML docs archive, etc.) and destroy the scanner with no undo —
	// SwapDir's "restore on failure" only fires for rename failures, not
	// for logically-wrong-but-valid tarballs.
	md, verr := validateExtractedTrivyDB(tmpDir)
	if verr != nil {
		writeJSONError(w, r, http.StatusUnprocessableEntity, ErrValidationFailed, verr.Error())
		return
	}

	// Atomic swap: rename-aside old dir, rename-in new dir, remove old on
	// success. Audit finding #6: the previous implementation RemoveAll'd the
	// old DB before the rename, which could leave the scanner with no DB at
	// all if the rename then failed. SwapDir restores the old dir on failure.
	//
	// Codex batch-13 Q4: serialize every rotation through trivyRotateMu so
	// a concurrent upload + online-pull cannot interleave rename-aside and
	// rename-in steps on the same dbDir.
	dbDir := d.trivyDBDir()
	trivyRotateMu.Lock()
	err = storage.SwapDir(tmpDir, dbDir)
	trivyRotateMu.Unlock()
	if err != nil {
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "atomic swap failed")
		return
	}

	// Insert trivy_db_meta row. Prefer the DB's own schema version string
	// pulled from metadata.json when available — the upload filename is a
	// weak proxy (operators often rename downloads) and telling the UI
	// "Version: db.tar.gz" is actively unhelpful.
	version := hdr.Filename
	if md != nil && md.Version != 0 {
		version = fmt.Sprintf("schema=%d updated=%s", md.Version, md.UpdatedAt)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	var userID *int64
	if a, ok := auth.ActorFromContext(r.Context()); ok && a.ID != 0 {
		userID = &a.ID
	}
	_ = d.DB.WriteTx(r.Context(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(r.Context(), `
			INSERT INTO trivy_db_meta(version, source, size_bytes, applied_at, applied_by)
			VALUES (?, 'uploaded', ?, ?, ?)
		`, version, totalSize, now, userID)
		return err
	})

	// Audit.
	if a, ok := auth.ActorFromContext(r.Context()); ok {
		uid := a.ID
		d.recordAudit(r, audit.Event{
			Kind:        audit.EvtTrivyDBRotated,
			ActorUserID: &uid,
			TargetKind:  "trivy_db",
			TargetID:    hdr.Filename,
			Outcome:     "uploaded",
			Details: map[string]any{
				"size_bytes": totalSize,
				"source":     "uploaded",
				"version":    version,
			},
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":     "ok",
		"size_bytes": totalSize,
		"source":     "uploaded",
	})
}

// handleTrivyDBPull kicks off a Trivy DB download and returns immediately
// with 202 Accepted. The UI polls /admin/trivy/db/pull/status for progress.
// Only one pull runs at a time; a second POST while one is in flight
// returns 409 Conflict.
func (d Deps) handleTrivyDBPull(w http.ResponseWriter, r *http.Request) {
	pullJob.mu.Lock()
	if pullJob.state == "running" {
		pullJob.mu.Unlock()
		writeJSONError(w, r, http.StatusConflict, ErrValidationFailed,
			"A Trivy DB pull is already in progress.")
		return
	}
	pullJob.state = "running"
	pullJob.startedAt = time.Now()
	pullJob.finishedAt = time.Time{}
	pullJob.errorMsg = ""
	pullJob.bytes.Store(0)
	pullJob.mu.Unlock()

	// Capture actor up front; the goroutine runs outside the request
	// scope, so we can't rely on the request context for auth.
	var userID *int64
	if a, ok := auth.ActorFromContext(r.Context()); ok && a.ID != 0 {
		userID = &a.ID
	}

	// Audit the start of the pull so operators see who kicked it off
	// even if the job later fails.
	if userID != nil {
		uid := *userID
		d.recordAudit(r, audit.Event{
			Kind:        audit.EvtMaintenanceToggled,
			ActorUserID: &uid,
			TargetKind:  "trivy_db",
			Outcome:     "pull-started",
		})
	}

	// Run the pull in the background so the HTTP request returns quickly.
	// Use a detached context with a 10-minute cap.
	go d.runTrivyDBPull(userID)

	writeJSON(w, http.StatusAccepted, map[string]any{
		"status":     "started",
		"started_at": pullJob.startedAt.UTC().Format(time.RFC3339),
	})
}

// runTrivyDBPull is the background worker. It samples tmpDir size while
// trivy runs so the UI can render a progress bar.
func (d Deps) runTrivyDBPull(userID *int64) {
	bgCtx := context.Background()

	finish := func(errMsg string) {
		pullJob.mu.Lock()
		pullJob.finishedAt = time.Now()
		if errMsg != "" {
			pullJob.state = "failure"
			pullJob.errorMsg = errMsg
		} else {
			pullJob.state = "success"
			pullJob.errorMsg = ""
		}
		pullJob.mu.Unlock()
	}

	tmpDir, err := os.MkdirTemp(filepath.Join(d.DataRoot, "tmp"), "trivypull-*")
	if err != nil {
		slog.ErrorContext(bgCtx, "trivy.pull.mktempdir", "err", err)
		finish("could not create temp dir: " + err.Error())
		return
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	ctx, cancel := context.WithTimeout(bgCtx, 10*time.Minute)
	defer cancel()

	// Progress sampler: while the command runs, periodically walk tmpDir
	// and publish the total size. Trivy writes the DB files incrementally
	// into <cache-dir>/db/, so this tracks real download progress.
	sampleDone := make(chan struct{})
	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-sampleDone:
				return
			case <-ticker.C:
				pullJob.bytes.Store(dirSize(tmpDir))
			}
		}
	}()

	cmd := exec.CommandContext(ctx, "trivy", "image", "--download-db-only", "--cache-dir", tmpDir)
	output, cmdErr := cmd.CombinedOutput()
	close(sampleDone)

	if cmdErr != nil {
		// Full trivy output goes to the server log (admins can grep for the
		// incident via this event). The client-facing message stays generic
		// so stray URLs, request headers, or auth artifacts in trivy's
		// stderr can't leak into the API response.
		slog.ErrorContext(bgCtx, "trivy.pull.exec_failed",
			"err", cmdErr,
			"output", strings.TrimSpace(string(output)),
			"context_err", ctx.Err(),
		)
		if ctx.Err() == context.DeadlineExceeded {
			finish("Trivy DB download timed out after 10 minutes. Upload a DB tarball instead.")
			return
		}
		finish("Unable to reach the Trivy database server. Check the server log for details (search for trivy.pull.exec_failed), or upload a DB tarball instead.")
		return
	}

	// Trivy places the DB under <cache-dir>/db/; some versions differ.
	srcDB := filepath.Join(tmpDir, "db")
	if _, statErr := os.Stat(srcDB); statErr != nil {
		srcDB = tmpDir
	}

	dbDir := d.trivyDBDir()
	// Codex batch-13 Q4: serialize with the upload path through
	// trivyRotateMu so a simultaneous operator-initiated upload cannot
	// interleave its SwapDir with the pull's.
	trivyRotateMu.Lock()
	swapErr := storage.SwapDir(srcDB, dbDir)
	trivyRotateMu.Unlock()
	if swapErr != nil {
		slog.ErrorContext(bgCtx, "trivy.pull.swap_failed",
			"err", swapErr, "src", srcDB, "dst", dbDir)
		finish("failed to install downloaded DB: " + swapErr.Error())
		return
	}

	// Capture the real on-disk size after the swap — previously this
	// recorded 0, which broke the size column in the history table and
	// in the status response.
	installedBytes := dirSize(dbDir)
	pullJob.bytes.Store(installedBytes)

	now := time.Now().UTC().Format(time.RFC3339)
	if writeErr := d.DB.WriteTx(bgCtx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(bgCtx, `
			INSERT INTO trivy_db_meta(version, source, size_bytes, applied_at, applied_by)
			VALUES ('online', 'online-pulled', ?, ?, ?)
		`, installedBytes, now, userID)
		return err
	}); writeErr != nil {
		slog.ErrorContext(bgCtx, "trivy.pull.meta_insert_failed", "err", writeErr)
	}

	if d.Audit != nil && userID != nil {
		uid := *userID
		_ = d.Audit.Record(bgCtx, audit.Event{
			Kind:        audit.EvtMaintenanceToggled,
			ActorUserID: &uid,
			TargetKind:  "trivy_db",
			Outcome:     "online-pulled",
			Details: map[string]any{
				"size_bytes": installedBytes,
			},
		})
	}

	slog.InfoContext(bgCtx, "trivy.pull.success",
		"bytes", installedBytes, "dst", dbDir)
	finish("")
}

// handleTrivyDBPullStatus returns the current pull-job snapshot so the
// UI can render a progress bar while a pull is in flight and show the
// outcome after it finishes.
func (d Deps) handleTrivyDBPullStatus(w http.ResponseWriter, r *http.Request) {
	pullJob.mu.Lock()
	state := pullJob.state
	startedAt := pullJob.startedAt
	finishedAt := pullJob.finishedAt
	errMsg := pullJob.errorMsg
	pullJob.mu.Unlock()

	resp := map[string]any{
		"state":            state,
		"bytes_downloaded": pullJob.bytes.Load(),
	}
	if !startedAt.IsZero() {
		resp["started_at"] = startedAt.UTC().Format(time.RFC3339)
	}
	if !finishedAt.IsZero() {
		resp["finished_at"] = finishedAt.UTC().Format(time.RFC3339)
	}
	if errMsg != "" {
		resp["error"] = errMsg
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleTrivyDBHistory returns the applied Trivy DB updates, newest first.
// Cap at 50 rows — this is a sparse table (one entry per upload/pull).
func (d Deps) handleTrivyDBHistory(w http.ResponseWriter, r *http.Request) {
	rows, err := d.DB.Reader.QueryContext(r.Context(), `
		SELECT version, source, size_bytes, applied_at
		FROM trivy_db_meta
		ORDER BY id DESC
		LIMIT 50
	`)
	if err != nil {
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
		return
	}
	defer func() { _ = rows.Close() }()

	items := make([]map[string]any, 0, 8)
	for rows.Next() {
		var version, source, appliedAt string
		var sizeBytes int64
		if err := rows.Scan(&version, &source, &sizeBytes, &appliedAt); err != nil {
			writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
			return
		}
		items = append(items, map[string]any{
			"version":    version,
			"source":     source,
			"size_bytes": sizeBytes,
			"applied_at": appliedAt,
		})
	}
	if err := rows.Err(); err != nil {
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

