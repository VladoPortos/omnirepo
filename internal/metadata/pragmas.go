package metadata

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
)

// pragmaDSNValues is the D-09 pragma list carried into every connection via
// modernc.org/sqlite's `_pragma=<stmt>` DSN extension. Each entry becomes one
// `_pragma=<entry>` query-string parameter on the DSN (see ensureDSN in
// db.go), so every connection both pools open gets these pragmas applied
// before the first statement runs.
//
// Format note: modernc.org/sqlite expects function-call syntax inside
// _pragma (e.g. `foreign_keys(on)`), not `PRAGMA foreign_keys=ON` statement
// form. The driver runs each as `PRAGMA <value>;` under the hood.
//
// Order matters: journal_mode must be set before any write occurs, but in the
// DSN path the driver applies the list once at connection open, before any
// user statement runs, so the ordering is implicitly correct.
var pragmaDSNValues = []string{
	"journal_mode(WAL)",
	"synchronous(NORMAL)",
	"foreign_keys(ON)",
	"busy_timeout(5000)",
	"cache_size(-65536)",
	"temp_store(MEMORY)",
}

// PragmaDSNSnapshot returns a copy of the pragmas applied to every connection
// at open time via the modernc.org/sqlite `_pragma=` DSN extension. The admin
// DB-health endpoint (plan 10-02 / internal/api/admin_db_health.go) uses this
// as the SOURCE-OF-TRUTH for the `driver.pragmas` map and for the
// `journal_mode` response field.
//
// Why a snapshot, not a runtime `PRAGMA journal_mode` probe: per research
// PITFALLS §2, per-connection pragma state in modernc.org/sqlite can drift
// across the pool (a runtime probe hits whichever connection the pool hands
// out). The DSN list is the only canonical declaration — every new connection
// is guaranteed to have applied these values. See internal/metadata/db.go
// `ensureDSN` for the wiring.
//
// Returns a fresh slice each call so callers cannot mutate the package-level
// list by aliasing.
func PragmaDSNSnapshot() []string {
	out := make([]string, len(pragmaDSNValues))
	copy(out, pragmaDSNValues)
	return out
}

// checkCompileOptions asserts the modernc.org/sqlite build has the features
// OmniRepo relies on: ENABLE_FTS5 (global search) and the JSON1 extension
// (audit details, settings blobs).
//
// Note on ENABLE_JSON1: as of SQLite 3.38 the JSON1 extension is compiled in
// unconditionally and is no longer reported by PRAGMA compile_options — so a
// compile_options lookup returns 0 on every modernc.org/sqlite build even
// though json() works. We therefore probe FTS5 via sqlite_compileoption_used
// (still reported) and probe JSON1 functionally by executing `SELECT json()`.
// Either missing surfaces a wrapped error naming the feature — per D-09.
func checkCompileOptions(ctx context.Context, conn *sql.Conn) error {
	var hasFTS5 int
	if err := conn.QueryRowContext(ctx, "SELECT sqlite_compileoption_used(?)", "ENABLE_FTS5").Scan(&hasFTS5); err != nil {
		return fmt.Errorf("metadata: compile_options check for ENABLE_FTS5: %w", err)
	}
	if hasFTS5 != 1 {
		return fmt.Errorf("metadata: sqlite build missing required compile option ENABLE_FTS5")
	}

	// Functional probe for ENABLE_JSON1 — must return a valid JSON text.
	var out string
	if err := conn.QueryRowContext(ctx, `SELECT json('{"probe":1}')`).Scan(&out); err != nil {
		return fmt.Errorf("metadata: sqlite build missing required compile option ENABLE_JSON1: %w", err)
	}
	return nil
}

// Settings key names used by RunBootIntegrityCheck (Phase 10 DBHEALTH-06, D-16).
// Exported so admin endpoints (plans 10-02, 10-03) and the Dashboard card can
// read the cached state without duplicating string literals.
const (
	SettingDBIntegrityCheckStatus       = "db.integrity_check.status"
	SettingDBIntegrityCheckCheckedAt    = "db.integrity_check.checked_at"
	SettingDBIntegrityCheckDurationMs   = "db.integrity_check.duration_ms"
	SettingDBIntegrityCheckLastManualAt = "db.integrity_check.last_manual_at"
)

// AuditRecorder is the minimal audit-emission interface RunBootIntegrityCheck
// depends on. Defined here (rather than imported from internal/audit) to avoid
// a metadata → audit import cycle — audit itself already imports metadata.
//
// Callers (internal/app/app.go) supply an adapter that translates these kind
// strings back to audit.EventKind values and invokes the real audit logger.
// A nil AuditRecorder is tolerated: RunBootIntegrityCheck skips the Record
// call entirely, which keeps unit tests and bootstrap paths simple.
type AuditRecorder interface {
	Record(ctx context.Context, kind string, details map[string]any)
}

// Audit event kind strings mirrored from internal/audit/events.go. Duplicated
// as constants here ONLY so RunBootIntegrityCheck can emit the correct kinds
// without importing the audit package (cycle avoidance — see AuditRecorder
// doc comment). If either side diverges the emission will silently miss the
// TestEveryStateChangingActionEmitsEvent coverage — guard by keeping these
// in sync with audit.EvtIntegrityCheck*.
const (
	auditKindIntegrityCheckCompleted = "admin.integrity_check.completed"
	auditKindIntegrityCheckFailed    = "admin.integrity_check.failed"
)

// RunBootIntegrityCheck executes PRAGMA integrity_check(256) exactly once at
// boot. Thin wrapper delegating to RunIntegrityCheckNow with source="boot"
// so the boot sequence and plan 10-03's manual-trigger goroutine share
// exactly one code path for the PRAGMA + cache write + audit emit.
//
// Per CONTEXT D-15 / DBHEALTH-06 this runs once post-migrations and before
// HTTP Listen — no ticker, no GC-piggyback. Failure disposition (Pitfall
// 10.3 / A1): log + cache + continue; always returns nil.
func RunBootIntegrityCheck(ctx context.Context, db *DB, settings *SettingsRepo, auditRec AuditRecorder) error {
	_, _ = RunIntegrityCheckNow(ctx, db, settings, auditRec, "boot")
	return nil
}

// RunIntegrityCheckNow executes PRAGMA integrity_check(256) against the
// reader pool, caches the result (status + checked_at + duration_ms) to
// the settings table, and emits a single audit event (.completed on "ok",
// .failed otherwise). The source parameter is echoed back into the audit
// event details and controls whether db.integrity_check.last_manual_at is
// also written.
//
// source="boot"   — called from RunBootIntegrityCheck at app startup. Does
//                   NOT write last_manual_at (that key is reserved for
//                   operator-triggered runs).
// source="manual" — called from plan 10-03's POST /admin/db/health/check
//                   goroutine. Writes last_manual_at=now RFC3339 so the
//                   GET /admin/db/health endpoint can surface the last
//                   manual run timestamp across process restarts.
//
// Returns (status, durationMs) so the manual-trigger goroutine can update
// the in-process lease (dbHealthJob.lastStatus / lastRunAt) without
// re-reading the settings row it just wrote.
//
// Failure disposition (Pitfall 10.3 / A1): log + cache + continue. Any error
// obtaining a connection, executing the pragma, or writing settings is
// logged via slog.WarnContext but never propagated — the function always
// returns a (status, durationMs) pair. The Dashboard card (CONTEXT D-03)
// surfaces the failure via the destructive variant.
//
// Reader-pool invariant (Pitfall 10.1): uses db.Reader.Conn(ctx) with one
// pinned connection. Readers never block the size-1 writer pool in WAL
// mode. Do NOT switch to db.Writer — that would wedge every concurrent
// write for the check's duration.
//
// DSN invariant (Pitfall 10.2): integrity_check is NOT added to
// pragmaDSNValues. Every new connection would otherwise pay the cost.
//
// auditRec may be nil (tests, bootstrap-only paths). When nil, audit
// emission is skipped entirely — the caching behavior still runs.
func RunIntegrityCheckNow(ctx context.Context, db *DB, settings *SettingsRepo, auditRec AuditRecorder, source string) (string, int64) {
	start := time.Now()

	// Acquire a pinned reader connection. Failure here means the pool is
	// closed/unavailable — cache the failure and continue.
	conn, err := db.Reader.Conn(ctx)
	if err != nil {
		status := "boot_conn_failed: " + err.Error()
		slog.WarnContext(ctx, "db.integrity_check.conn_failed", "err", err, "source", source)
		dur := time.Since(start)
		writeIntegrityCache(ctx, settings, status, dur, source)
		emitIntegrityAudit(ctx, auditRec, false, status, dur, source)
		return status, dur.Milliseconds()
	}
	defer func() { _ = conn.Close() }()

	// PRAGMA integrity_check(N) returns a single row "ok" on pass; up to N
	// rows of error descriptions on failure [sqlite.org/pragma.html].
	rows, qerr := conn.QueryContext(ctx, "PRAGMA integrity_check(256)")
	if qerr != nil {
		status := "boot_query_failed: " + qerr.Error()
		slog.WarnContext(ctx, "db.integrity_check.query_failed", "err", qerr, "source", source)
		dur := time.Since(start)
		writeIntegrityCache(ctx, settings, status, dur, source)
		emitIntegrityAudit(ctx, auditRec, false, status, dur, source)
		return status, dur.Milliseconds()
	}
	defer func() { _ = rows.Close() }()

	var lines []string
	for rows.Next() {
		var s string
		if serr := rows.Scan(&s); serr == nil {
			lines = append(lines, s)
		}
	}
	if rerr := rows.Err(); rerr != nil {
		status := "boot_scan_failed: " + rerr.Error()
		slog.WarnContext(ctx, "db.integrity_check.scan_failed", "err", rerr, "source", source)
		dur := time.Since(start)
		writeIntegrityCache(ctx, settings, status, dur, source)
		emitIntegrityAudit(ctx, auditRec, false, status, dur, source)
		return status, dur.Milliseconds()
	}

	duration := time.Since(start)
	status := strings.Join(lines, "\n")
	if status == "" {
		status = "unknown"
	}

	writeIntegrityCache(ctx, settings, status, duration, source)
	emitIntegrityAudit(ctx, auditRec, status == "ok", status, duration, source)

	slog.InfoContext(ctx, "db.integrity_check",
		"status", status,
		"duration_ms", duration.Milliseconds(),
		"source", source,
	)
	return status, duration.Milliseconds()
}

// writeIntegrityCache persists the three common cache rows (status,
// checked_at, duration_ms) and, when source="manual", the additional
// last_manual_at row. Errors are logged but not returned — per Pitfall
// 10.3 the boot path must never propagate; the manual path follows the
// same convention so a partial settings write does not cause the POST
// goroutine to leave the lease in an inconsistent state.
func writeIntegrityCache(ctx context.Context, settings *SettingsRepo, status string, duration time.Duration, source string) {
	if settings == nil {
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if err := settings.Set(ctx, SettingDBIntegrityCheckStatus, status); err != nil {
		slog.WarnContext(ctx, "db.integrity_check.cache_status_failed", "err", err, "source", source)
	}
	if err := settings.Set(ctx, SettingDBIntegrityCheckCheckedAt, now); err != nil {
		slog.WarnContext(ctx, "db.integrity_check.cache_checked_at_failed", "err", err, "source", source)
	}
	if err := settings.Set(ctx, SettingDBIntegrityCheckDurationMs,
		strconv.FormatInt(duration.Milliseconds(), 10)); err != nil {
		slog.WarnContext(ctx, "db.integrity_check.cache_duration_failed", "err", err, "source", source)
	}
	// Manual runs also stamp last_manual_at so the GET /admin/db/health
	// endpoint has a cross-restart fallback for its last_manual_run_at
	// response field (plan 10-02 reads this when the in-process lease is
	// empty, e.g. after a restart that lost the lease's lastRunAt).
	if source == "manual" {
		if err := settings.Set(ctx, SettingDBIntegrityCheckLastManualAt, now); err != nil {
			slog.WarnContext(ctx, "db.integrity_check.cache_last_manual_at_failed", "err", err, "source", source)
		}
	}
}

// emitIntegrityAudit emits exactly one audit event. ok=true → completed;
// ok=false → failed. source is echoed into details.source so plan 10-03's
// .triggered→.completed/.failed pairs are filterable by source in the
// audit UI. Nil-safe on auditRec.
func emitIntegrityAudit(ctx context.Context, auditRec AuditRecorder, ok bool, status string, duration time.Duration, source string) {
	if auditRec == nil {
		return
	}
	kind := auditKindIntegrityCheckCompleted
	if !ok {
		kind = auditKindIntegrityCheckFailed
	}
	auditRec.Record(ctx, kind, map[string]any{
		"source":      source,
		"status":      status,
		"duration_ms": duration.Milliseconds(),
	})
}
