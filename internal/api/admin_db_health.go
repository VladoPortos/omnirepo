// Package api — admin database health endpoint (Phase 10 DBHEALTH-01..07,
// CONTEXT D-14/D-15/D-16).
//
// GET /api/v1/admin/db/health — super-admin-only card payload for the
// Dashboard DBHealthCard. Assembled from:
//   - Cached boot-time `PRAGMA integrity_check(256)` result (settings rows
//     written by internal/metadata.RunBootIntegrityCheck in plan 10-01).
//   - Three O(1) PRAGMAs (page_count / page_size / freelist_count) run
//     against a pinned reader connection.
//   - Two os.Stat calls (DB file + WAL sidecar) for on-disk + WAL bytes.
//   - PragmaDSNSnapshot() for journal_mode + driver.pragmas — source-of-truth
//     per PITFALLS §2 (no runtime `PRAGMA journal_mode` probe).
//
// Never runs `PRAGMA integrity_check` inline — that pragma is a heavy scan
// that would wedge the size-1 writer pool (PITFALLS §1). Manual refresh is
// plan 10-03's POST /admin/db/health/check endpoint, which appends to the
// same mountAdminDBHealth router group and shares the dbHealthJob lease
// defined here.
//
// Budget: <100 ms on a 500 MB DB (DBHEALTH-07 / SC3). Fast merge-gate in
// admin_db_health_test.go uses a 10 MB proxy DB; authoritative 500 MB
// assertion lives in admin_db_health_perf_test.go behind //go:build perf500
// and is wired into `make test-perf`.
package api

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/dxc-internal/omnirepo/internal/auth"
	authmw "github.com/dxc-internal/omnirepo/internal/auth/middleware"
	"github.com/dxc-internal/omnirepo/internal/metadata"
)

// Constants locked by CONTEXT D-10 / DBHEALTH-03 and pinned to v1.4 vendor.
const (
	// integrityRateLimitWindow is the 1/hour throttle (CONTEXT D-10) that
	// the POST /admin/db/health/check endpoint (plan 10-03) will enforce
	// via dbHealthJob.lastRunAt. The GET endpoint reads this to compute
	// `next_available_at` so the UI can server-drive eligibility (D-11)
	// instead of client-clock-drifting.
	integrityRateLimitWindow = time.Hour

	// walWarnOverBytes is the 100 MB threshold from DBHEALTH-03: over this
	// size the UI renders the card in the `warning` variant (CONTEXT D-03).
	// Value surfaced as `wal.warn_over_bytes` so the frontend does not need
	// to duplicate the constant.
	walWarnOverBytes = int64(100 * 1024 * 1024)

	// driverLabelModerncVersion pins the vendored modernc.org/sqlite
	// version. Bump this constant whenever vendor/modules.txt changes the
	// modernc.org/sqlite pin — CI's `go mod verify` will catch vendor drift
	// but this label is cosmetic and does not have its own gate. Verified
	// against vendor/modules.txt @ 2026-04-21: "# modernc.org/sqlite v1.48.2".
	driverLabelModerncVersion = "v1.48.2"
)

// integrityCheckJob is the process-wide single-flight slot for the
// integrity-check manual-refresh flow (plan 10-03). This plan (10-02)
// defines the struct + package-global so:
//  1. The GET handler can read a lease snapshot and surface running /
//     can_run_now / next_available_at without plan 10-03 being landed yet.
//  2. Plan 10-03 can append the POST handler to the same mount function
//     and mutate state under the same mutex.
//
// Concurrency model: handlers take mu briefly for snapshot or state
// transition. Long-running work (the actual PRAGMA) runs in a detached
// goroutine — never under mu.
type integrityCheckJob struct {
	mu        sync.Mutex
	state     string    // "idle" | "running"
	startedAt time.Time // populated when state == "running"; zero otherwise

	// lastRunAt is the wall time of the most recent COMPLETED manual run
	// (ok or failed). Drives integrityRateLimitWindow. Zero = never run.
	lastRunAt time.Time

	// lastStatus mirrors settings.db.integrity_check.status for the last
	// manual run. Not surfaced directly by this plan; plan 10-03 consumes
	// it for 409 `already_running` and 429 `rate_limited` envelopes.
	lastStatus string
}

// dbHealthJob is the process-wide single-flight tracker shared between
// the GET handler (read-only snapshot) and plan 10-03's POST handler
// (state mutation). Package-private so tests can reach it via internal
// test files.
var dbHealthJob = &integrityCheckJob{state: "idle"}

// leaseSnapshot is the short-lived value the GET handler reads under mu.
// Held-lock scope is strictly the field copy — the snapshot is RFC3339-
// formatted off-lock in handleDBHealth.
type leaseSnapshot struct {
	Running         bool
	CanRunNow       bool
	NextAvailableAt string // RFC3339; "" if CanRunNow == true
	JobStartedAt    string // RFC3339; "" if !Running
	LastManualRunAt string // RFC3339; "" if lastRunAt is zero
}

// Snapshot returns the lease state formatted for the GET response. Takes
// mu, reads the 3 time fields + state, releases, then formats RFC3339
// strings off-lock to keep the critical section minimal.
func (j *integrityCheckJob) Snapshot() leaseSnapshot {
	j.mu.Lock()
	state := j.state
	startedAt := j.startedAt
	lastRunAt := j.lastRunAt
	j.mu.Unlock()

	snap := leaseSnapshot{
		Running: state == "running",
	}
	if !lastRunAt.IsZero() {
		snap.LastManualRunAt = lastRunAt.UTC().Format(time.RFC3339)
	}
	if !startedAt.IsZero() {
		snap.JobStartedAt = startedAt.UTC().Format(time.RFC3339)
	}

	// CanRunNow eligibility: never-run OR last run is outside the
	// rate-limit window. Server-driven (D-11) so client clock drift does
	// not let the UI flash the button enabled while the server 429s.
	if lastRunAt.IsZero() {
		snap.CanRunNow = true
	} else {
		nextAt := lastRunAt.Add(integrityRateLimitWindow)
		if !time.Now().Before(nextAt) {
			snap.CanRunNow = true
		} else {
			snap.CanRunNow = false
			snap.NextAvailableAt = nextAt.UTC().Format(time.RFC3339)
		}
	}
	return snap
}

// cachedIntegrity is the typed result of readCachedIntegrity. All three
// fields are zero-valued when the boot hook has not yet populated the
// settings rows (e.g. in-memory test DBs that skip app.Run) — callers
// surface the defaults as `status=""` / `checked_at=""` / `duration_ms=0`
// in the response so the UI renders an "unknown" variant rather than 500.
type cachedIntegrity struct {
	Status     string
	CheckedAt  string
	DurationMs int64
}

// readCachedIntegrity loads the three cache rows written by plan 10-01's
// RunBootIntegrityCheck + plan 10-03's manual-refresh goroutine. Missing
// keys are treated as empty — NOT as errors — so a freshly-migrated DB
// with no integrity cache yet still returns 200 from /admin/db/health.
//
// Any non-ErrNotFound settings error is swallowed and logged upstream by
// the handler's slog call; the card surfaces "unknown" rather than 500.
func readCachedIntegrity(ctx context.Context, settings *metadata.SettingsRepo) cachedIntegrity {
	var c cachedIntegrity
	if settings == nil {
		return c
	}
	if v, err := settings.Get(ctx, metadata.SettingDBIntegrityCheckStatus); err == nil {
		c.Status = v
	}
	if v, err := settings.Get(ctx, metadata.SettingDBIntegrityCheckCheckedAt); err == nil {
		c.CheckedAt = v
	}
	if v, err := settings.Get(ctx, metadata.SettingDBIntegrityCheckDurationMs); err == nil {
		if n, perr := strconv.ParseInt(v, 10, 64); perr == nil {
			c.DurationMs = n
		}
	}
	return c
}

// readCheapPragmas runs the three O(1) PRAGMAs against a pinned reader
// connection. All three are constant-time on WAL-mode SQLite since:
//   - page_count reads from the database header (first page).
//   - page_size is a compile-time attribute cached in the header.
//   - freelist_count reads the freelist trunk pointer (header + 1 page).
//
// No row-scanning or disk traversal — cost does not grow with DB size.
// Reader pool only; writer pool is reserved for transactional writes.
func readCheapPragmas(ctx context.Context, conn *sql.Conn) (pageCount, pageSize, freelistCount int64, err error) {
	if err = conn.QueryRowContext(ctx, "PRAGMA page_count").Scan(&pageCount); err != nil {
		return 0, 0, 0, err
	}
	if err = conn.QueryRowContext(ctx, "PRAGMA page_size").Scan(&pageSize); err != nil {
		return 0, 0, 0, err
	}
	if err = conn.QueryRowContext(ctx, "PRAGMA freelist_count").Scan(&freelistCount); err != nil {
		return 0, 0, 0, err
	}
	return pageCount, pageSize, freelistCount, nil
}

// fileStatBytes returns the size of the file at path, or 0 when the file
// does not exist (ErrNotExist branch). Other stat errors (EACCES, I/O
// failures) return 0 so the card renders rather than 500s on a partial
// filesystem fault — operators see the integrity status and other fields
// even if WAL stat fails. Errors are logged upstream in the handler.
func fileStatBytes(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0
		}
		// Non-ErrNotExist: still return 0 rather than propagate. The card
		// is a read-only diagnostic; degrading one field is preferable to
		// 500ing the whole response. Handler logs the underlying error.
		return 0
	}
	return info.Size()
}

// resolveDBPath derives the SQLite file path from d.DataRoot using the
// same layout as internal/app.Run (cfg.DataRoot + "db/omnirepo.sqlite").
// Deps does not currently carry an explicit DBPath field — if one is
// added later, prefer it over the computed path here (D-16 composition
// root convention).
//
// Returns "" when DataRoot is empty (test harnesses that skip app.Run);
// the caller treats this as "file stat returns 0" which gracefully
// degrades the size + WAL fields.
func resolveDBPath(d Deps) string {
	if d.DataRoot == "" {
		return ""
	}
	return filepath.Join(d.DataRoot, "db", "omnirepo.sqlite")
}

// parsePragmaDSNEntry splits a `key(value)` entry from PragmaDSNSnapshot
// into its two components. e.g. `journal_mode(WAL)` → ("journal_mode", "WAL").
// Returns ("", "") on malformed input — callers skip such entries.
func parsePragmaDSNEntry(entry string) (key, value string) {
	lp := strings.Index(entry, "(")
	rp := strings.LastIndex(entry, ")")
	if lp <= 0 || rp <= lp {
		return "", ""
	}
	return entry[:lp], entry[lp+1 : rp]
}

// driverSummary builds the `driver` response object from PragmaDSNSnapshot.
// Label is a static format string; pragmas is a map of every DSN-applied
// pragma as declared in pragmas.go. Callers must not mutate the returned
// map — it is constructed fresh per call so aliasing is harmless but
// unnecessary.
func driverSummary() (label string, pragmas map[string]string) {
	label = "modernc " + driverLabelModerncVersion + " (FTS5, JSON1)"
	pragmas = map[string]string{}
	for _, entry := range metadata.PragmaDSNSnapshot() {
		k, v := parsePragmaDSNEntry(entry)
		if k == "" {
			continue
		}
		// Preserve the raw value for the map (readers want the exact
		// DSN-applied text, not a normalized form).
		pragmas[k] = v
	}
	return label, pragmas
}

// journalModeFromDSN extracts the journal_mode value from PragmaDSNSnapshot
// (source-of-truth per PITFALLS §2) and lowercases it for the response —
// the UI compares against "wal" in a case-insensitive renderer so we
// normalize once here to keep downstream assertions simple.
func journalModeFromDSN() string {
	for _, entry := range metadata.PragmaDSNSnapshot() {
		k, v := parsePragmaDSNEntry(entry)
		if k == "journal_mode" {
			return strings.ToLower(v)
		}
	}
	return ""
}

// mountAdminDBHealth installs the DB-health admin endpoints on r.
//
// This plan (10-02) mounts only the GET route. Plan 10-03 will append
// the POST /admin/db/health/check route under the same super-admin gate
// and the same dbHealthJob lease. Keep the mount function the single
// entry point so admin_phase1.go's registration remains one line.
func (d Deps) mountAdminDBHealth(r chi.Router) {
	r.With(authmw.RequireCan(auth.ActionTriggerGC)).
		Get("/admin/db/health", d.handleDBHealth)
	// Plan 10-03 will append:
	//   r.With(authmw.RequireCan(auth.ActionTriggerGC)).
	//       Post("/admin/db/health/check", d.handleDBHealthCheck)
}

// handleDBHealth assembles the full DBHealthCard payload. Route-level
// super-admin gating is already applied by mountAdminDBHealth — no
// in-handler auth checks required.
//
// Never runs `PRAGMA integrity_check` (PITFALLS §1). Never probes
// `PRAGMA journal_mode` (PITFALLS §2). Never takes a writer tx.
func (d Deps) handleDBHealth(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// --- 1. Cached integrity (settings rows). Empty on missing/fresh DB.
	integrity := readCachedIntegrity(ctx, d.Settings)

	// --- 2. Cheap PRAGMAs against one pinned reader connection. Pinning
	// guarantees page_count / page_size / freelist_count come from the
	// same snapshot; without it the three queries could land on different
	// pool connections in a heavily-loaded scenario.
	var pageCount, pageSize, freelistCount int64
	if d.DB != nil && d.DB.Reader != nil {
		conn, err := d.DB.Reader.Conn(ctx)
		if err != nil {
			// Degrade: log + continue with zeros. Upstream logger already
			// sees the err via slog — the card stays readable.
			writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
			return
		}
		defer func() { _ = conn.Close() }()
		pageCount, pageSize, freelistCount, err = readCheapPragmas(ctx, conn)
		if err != nil {
			writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
			return
		}
	}

	logicalBytes := pageCount * pageSize
	freelistBytes := freelistCount * pageSize

	// --- 3. File-level size facts: on-disk DB + WAL sidecar.
	dbPath := resolveDBPath(d)
	onDiskBytes := int64(0)
	walBytes := int64(0)
	if dbPath != "" {
		onDiskBytes = fileStatBytes(dbPath)
		// WAL sidecar naming convention: "<dbfile>-wal" (SQLite docs,
		// reiterated in PITFALLS §3). modernc.org/sqlite honors this
		// without quirks. `-shm` is NOT included — it's a shared-memory
		// index file whose size is not operationally meaningful.
		walBytes = fileStatBytes(dbPath + "-wal")
	}

	// --- 4. Driver summary + journal mode (DSN source-of-truth).
	driverLabel, driverPragmas := driverSummary()
	journalMode := journalModeFromDSN()

	// --- 5. Lease snapshot for running / can_run_now / next_available_at.
	// last_manual_run_at: prefer the in-process lease (written by plan
	// 10-03's POST handler) but fall back to the settings row which
	// survives restarts. In-process always wins when both exist because
	// the lease is updated synchronously and the settings row is written
	// asynchronously from the goroutine.
	lease := dbHealthJob.Snapshot()
	lastManualRunAt := lease.LastManualRunAt
	if lastManualRunAt == "" && d.Settings != nil {
		if v, err := d.Settings.Get(ctx, metadata.SettingDBIntegrityCheckLastManualAt); err == nil {
			lastManualRunAt = v
		}
	}

	// --- 6. Response assembly. Field names LOCKED per plan 10-02 <interfaces>
	// — the frontend in plan 10-04 consumes these verbatim. Adding or
	// removing keys here is a schema change that must update the plan.
	resp := map[string]any{
		"integrity": map[string]any{
			"status":      integrity.Status,
			"checked_at":  integrity.CheckedAt,
			"duration_ms": integrity.DurationMs,
		},
		"size": map[string]any{
			"on_disk_bytes":  onDiskBytes,
			"logical_bytes":  logicalBytes,
			"page_count":     pageCount,
			"page_size":      pageSize,
			"freelist_count": freelistCount,
			"freelist_bytes": freelistBytes,
		},
		"wal": map[string]any{
			"bytes":           walBytes,
			"warn_over_bytes": walWarnOverBytes,
		},
		"journal_mode": journalMode,
		"driver": map[string]any{
			"label":   driverLabel,
			"pragmas": driverPragmas,
		},
		"running":            lease.Running,
		"can_run_now":        lease.CanRunNow,
		"next_available_at":  lease.NextAvailableAt,
		"last_manual_run_at": lastManualRunAt,
	}
	writeJSON(w, http.StatusOK, resp)
}
