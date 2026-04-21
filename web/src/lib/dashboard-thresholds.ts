/**
 * dashboard-thresholds — pure functions mapping numeric health signals
 * to StatusVariant per Phase 7 UI-SPEC §Status token → threshold mapping
 * and CONTEXT §D-02.
 *
 * Consumed by the Phase 7 Composition row cards on DashboardPage
 * (C-1 Storage, C-2 Recent Failures, C-3 Scan Findings Trend, C-4
 * Background Jobs, C-5 TLS Cert Expiry, C-6 Trivy DB Freshness).
 *
 * Every function is side-effect-free and accepts an optional per-card
 * overrides object so admins can tweak thresholds via the existing
 * `settings` table without changing any rendering code.
 *
 * Design notes:
 *   - All return values are members of StatusVariant. The Jobs card
 *     intentionally maps ONLY onto healthy/warning/failure: D-02 locks
 *     that set, and inventing 'disabled'/'maintenance' returns here
 *     would expand the StatusBadge variant enum without a CONTEXT
 *     decision.
 *   - Six individual override objects (one per function) were chosen
 *     over a single aggregated `DashboardThresholds` blob per
 *     RESEARCH Open Question 3 — simpler to test, simpler for admins
 *     to edit, and typed per-function so you can't accidentally
 *     pass Trivy overrides to storageVariant.
 *   - Boundary semantics follow the UI-SPEC table verbatim: warn
 *     boundaries are inclusive of the warn bucket, fail boundaries
 *     are inclusive of the fail bucket. Unit tests in
 *     __tests__/dashboard-thresholds.test.ts lock each edge.
 */

import type { StatusVariant } from '@/components/common/StatusBadge';

// -----------------------------------------------------------------------------
// Storage (C-1)
// -----------------------------------------------------------------------------

export interface StorageOverrides {
  /** Ratio at which healthy flips to warning. Default 0.70. */
  warnRatio?: number;
  /** Ratio ABOVE which warning flips to failure. Default 0.90. */
  failRatio?: number;
}

/**
 * storageVariant — maps used/total bytes to a StatusVariant per D-02.
 *
 *   total <= 0         → disabled (no storage configured / divide-by-zero)
 *   ratio <  warnRatio → healthy  (default warn: 0.70)
 *   ratio <= failRatio → warning  (default fail: 0.90, inclusive)
 *   ratio >  failRatio → failure
 */
export function storageVariant(
  used: number,
  total: number,
  overrides: StorageOverrides = {},
): StatusVariant {
  if (total <= 0) return 'disabled';
  const ratio = used / total;
  const warn = overrides.warnRatio ?? 0.7;
  const fail = overrides.failRatio ?? 0.9;
  if (ratio > fail) return 'failure';
  if (ratio >= warn) return 'warning';
  return 'healthy';
}

// -----------------------------------------------------------------------------
// Recent Failures (C-2)
// -----------------------------------------------------------------------------

export interface FailuresOverrides {
  /** Upper bound (inclusive) of the warning bucket; counts above flip to failure. Default 5. */
  warnUpper?: number;
}

/**
 * failuresVariant — maps a raw failure count (e.g. `.failed` audit events
 * in the last 24h) to a StatusVariant per D-02.
 *
 *   count == 0               → healthy
 *   count <= warnUpper       → warning (default warnUpper: 5)
 *   count >  warnUpper       → failure
 */
export function failuresVariant(
  count: number,
  overrides: FailuresOverrides = {},
): StatusVariant {
  const warnUpper = overrides.warnUpper ?? 5;
  if (count === 0) return 'healthy';
  if (count <= warnUpper) return 'warning';
  return 'failure';
}

// -----------------------------------------------------------------------------
// Scan Findings Trend (C-3)
// -----------------------------------------------------------------------------

export interface ScanFindingsOverrides {
  /** Upper bound (inclusive) of the warning bucket for critical count. Default 5. */
  warnUpper?: number;
}

/**
 * scanFindingsVariant — maps current CRITICAL count + a "never scanned"
 * flag to a StatusVariant per D-02.
 *
 *   neverScanned == true        → disabled (scan pipeline hasn't run yet)
 *   currentCritical == 0        → healthy
 *   currentCritical <= warnUpper → warning (default warnUpper: 5)
 *   currentCritical >  warnUpper → failure
 */
export function scanFindingsVariant(
  currentCritical: number,
  neverScanned: boolean,
  overrides: ScanFindingsOverrides = {},
): StatusVariant {
  if (neverScanned) return 'disabled';
  const warnUpper = overrides.warnUpper ?? 5;
  if (currentCritical === 0) return 'healthy';
  if (currentCritical <= warnUpper) return 'warning';
  return 'failure';
}

// -----------------------------------------------------------------------------
// Background Jobs (C-4)
// -----------------------------------------------------------------------------

/**
 * jobsVariant — maps the admin/jobs/summary response shape (D-06) to
 * ONE of ONLY {healthy, warning, failure}. D-02 locks the three
 * variants for the Jobs card; introducing 'disabled' or 'maintenance'
 * returns here would expand the StatusBadge variant enum without a
 * CONTEXT decision. A future phase that needs a fourth semantic state
 * (e.g. 'idle-never-run') MUST add D-02b to CONTEXT.md first.
 *
 * Decision table:
 *   failedLast24h > 5                               → failure
 *   failedLast24h > 0 AND failed > running + queued → warning
 *   running > 0 OR queued > 0                       → healthy (jobs moving)
 *   all zero AND lastCompletedAt != null            → healthy (idle-and-healthy)
 *   all zero AND lastCompletedAt == null            → healthy (idle, never run yet)
 *
 * Note: `lastCompletedAt` is currently unused because every idle state
 * maps to healthy. It is kept in the signature so the card can show
 * a tooltip ("last successful run: <time>") without wiring a second
 * threshold function.
 */
export function jobsVariant(
  running: number,
  queued: number,
  failedLast24h: number,
  _lastCompletedAt: string | null,
): StatusVariant {
  if (failedLast24h > 5) return 'failure';
  if (failedLast24h > 0 && failedLast24h > running + queued) return 'warning';
  // jobs moving, idle-and-healthy, or idle-never-run — all healthy.
  return 'healthy';
}

// -----------------------------------------------------------------------------
// TLS Cert Expiry (C-5)
// -----------------------------------------------------------------------------

export interface TLSOverrides {
  /** Days remaining at/below which healthy flips to warning. Default 30. */
  warnDays?: number;
  /** Days remaining below which warning flips to failure. Default 14. */
  failDays?: number;
}

/**
 * tlsVariant — maps days remaining on the active TLS cert to a
 * StatusVariant per D-02. Self-signed (no uploaded cert) is disabled
 * because expiry monitoring is only meaningful against an operator-
 * uploaded cert.
 *
 *   !hasUploadedCert              → disabled (self-signed default)
 *   daysRemaining >= warnDays     → healthy  (default warnDays: 30)
 *   daysRemaining >= failDays     → warning  (default failDays: 14)
 *   daysRemaining <  failDays     → failure  (includes negative/expired)
 */
export function tlsVariant(
  daysRemaining: number,
  hasUploadedCert: boolean,
  overrides: TLSOverrides = {},
): StatusVariant {
  if (!hasUploadedCert) return 'disabled';
  const warn = overrides.warnDays ?? 30;
  const fail = overrides.failDays ?? 14;
  if (daysRemaining < fail) return 'failure';
  if (daysRemaining < warn) return 'warning';
  return 'healthy';
}

// -----------------------------------------------------------------------------
// Trivy DB Freshness (C-6)
// -----------------------------------------------------------------------------

export interface TrivyOverrides {
  /** Age in days above which healthy flips to warning. Default 7. */
  warnDays?: number;
  /** Age in days above which warning flips to failure. Default 30. */
  failDays?: number;
}

/**
 * trivyDBVariant — maps Trivy DB age (days since last update) to a
 * StatusVariant per D-02. An uninitialised Trivy DB is disabled because
 * no scans are running against stale data — the air-gap runtime may
 * simply not have pulled a DB yet.
 *
 *   !everInitialised          → disabled
 *   ageDays <= warnDays       → healthy  (default warnDays: 7)
 *   ageDays <= failDays       → warning  (default failDays: 30)
 *   ageDays >  failDays       → failure
 */
export function trivyDBVariant(
  ageDays: number,
  everInitialised: boolean,
  overrides: TrivyOverrides = {},
): StatusVariant {
  if (!everInitialised) return 'disabled';
  const warn = overrides.warnDays ?? 7;
  const fail = overrides.failDays ?? 30;
  if (ageDays > fail) return 'failure';
  if (ageDays > warn) return 'warning';
  return 'healthy';
}

// -----------------------------------------------------------------------------
// SQLite Health (C-7, admin-only — Phase 10 / plan 10-04)
// -----------------------------------------------------------------------------

/**
 * dbHealthVariant — maps the GET /api/v1/admin/db/health payload's
 * integrity.status + wal.bytes onto a 3-state StatusVariant per
 * Phase 10 CONTEXT D-03.
 *
 * Precedence rules (per D-03):
 *   1. integrity.status != 'ok' AND status != '' AND status != 'unknown'
 *      → 'failure' (dominant; corruption overrides WAL bloat).
 *   2. wal.bytes > walWarnOverBytes → 'warning' (operator can actionable-
 *      restart to trigger a WAL checkpoint).
 *   3. otherwise → 'healthy'.
 *
 * Notes on '' / 'unknown':
 *   CONTEXT D-03 explicitly rejects an 'unknown' variant. When the
 *   integrity_check hasn't run yet (empty string from the settings row)
 *   or the boot hook captured an 'unknown' status, the card still renders
 *   with the healthy/warning badge; the WAL threshold still applies so
 *   WAL bloat on a never-run DB still surfaces.
 *
 * walWarnOverBytes is required (not a default) because the server
 * sources it from the cached payload — the frontend never hardcodes
 * 100 MB, matching plan 10-04's no-hardcoded-threshold invariant.
 *
 * Returns a subset of StatusVariant ('healthy' | 'warning' | 'failure')
 * matching the 3-state locked-down set in D-03.
 */
export function dbHealthVariant(
  integrityStatus: string,
  walBytes: number,
  walWarnOverBytes: number,
): Extract<StatusVariant, 'healthy' | 'warning' | 'failure'> {
  if (
    integrityStatus &&
    integrityStatus !== 'ok' &&
    integrityStatus !== 'unknown'
  ) {
    return 'failure';
  }
  if (walBytes > walWarnOverBytes) return 'warning';
  return 'healthy';
}
