---
phase: 06-error-envelope-visual-foundation
plan: 05
subsystem: api
tags: [slog, http-error, redaction, protocol-handlers, incident-id, err-03, makefile-gate, chi, raw, rpm, deb, pypi, helm]

# Dependency graph
requires:
  - phase: 06-error-envelope-visual-foundation/04
    provides: ZERO legacy {error, detail} emitters on /api/v1; ApiErrorEnvelope wire shape solidified; middleware.GetReqID → X-Incident-Id correlation idiom established
provides:
  - Every http.Error call site in internal/protocol/{raw,rpm,deb,pypi,helm} (14 files, 59 sites) now emits a static, generic client message and logs the real error via slog.ErrorContext keyed by chi middleware.GetReqID — ERR-03 closed for the protocol surface
  - New internal/protocol/protocoltest package with TestNoPercentVLeakInHTTPError — in-process invariant that runs under `go test ./...`
  - Makefile `lint-protocol-redaction` target wired as prerequisite to `test` — out-of-process CI gate mirroring the Go test
affects: [06-06, 06-07, 06-08, 07, 08, 09, 10]

# Tech tracking
tech-stack:
  added:
    - log/slog (first use inside internal/protocol/** — was already in stdlib)
    - github.com/go-chi/chi/v5/middleware (now imported as `chimw` in 14 protocol handler files for GetReqID)
  patterns:
    - "slog.ErrorContext(ctx, \"<package>.<handler>.<op>_failed\", slog.String(\"incident_id\", chimw.GetReqID(ctx)), slog.String(<key>, <value>), slog.Any(\"err\", err)) paired 1:1 with http.Error(w, \"<generic>\", status) — the canonical redaction shape for protocol handlers"
    - "Hierarchical dotted slog event names: raw.get.stat_failed, rpm.put.commit_failed, deb.serve.open_failed, pypi.legacy.multipart_failed, helm.chart.stat_failed, etc. — one event name per failure branch so log greps are stable"
    - "Static generic client messages: \"storage error\" (IO/commit), \"invalid multipart body\" (pypi legacy parse), \"not found\"/\"forbidden\"/\"request body too large\" preserved as-is"
    - "Makefile grep gate + in-process Go test both enforce the same invariant — `make test` and `go test ./...` fail in lockstep so neither a CI-only nor a Go-only workflow can miss a regression"

key-files:
  created:
    - internal/protocol/protocoltest/redaction_test.go (95 lines, 1 test function TestNoPercentVLeakInHTTPError)
  modified:
    - internal/protocol/raw/get.go — 3 sites redacted (stat in get/head, open in serveFile)
    - internal/protocol/raw/delete.go — 3 sites (stat, trash, commit)
    - internal/protocol/raw/listing.go — 1 site (readdir)
    - internal/protocol/rpm/get.go — 4 sites (repodata stat+open, package stat+open)
    - internal/protocol/rpm/put.go — 7 sites (read body, mkdir/create/write/close tmp, storage, commit)
    - internal/protocol/rpm/delete.go — 3 sites (trash, stat, commit)
    - internal/protocol/deb/get.go — 2 sites (serveFile stat+open, function shared across public key / dists / pool paths)
    - internal/protocol/deb/put.go — 3 sites (read body, storage, commit)
    - internal/protocol/deb/delete.go — 4 sites (trash, stat, commit, suites commit)
    - internal/protocol/pypi/upload_legacy.go — 8 sites (mkdir, multipart, tmp create, tmp write, read tmp, storage, commit upload, commit delete)
    - internal/protocol/pypi/upload_pep694.go — 6 sites (mkdir, tmp create, tmp write, read staged, storage, commit)
    - internal/protocol/helm/get.go — 4 sites (index stat+open, chart stat+open)
    - internal/protocol/helm/put.go — 8 sites (read body, mkdir/create/write/close tmp, storage, commit, provenance storage)
    - internal/protocol/helm/delete.go — 3 sites (stat, trash, commit)
    - Makefile — new .PHONY, new `lint-protocol-redaction` target, wired as `test:` prerequisite

key-decisions:
  - "Actual leak count was 59 sites across 14 files, not the ~206 estimate in 06-RESEARCH.md. The estimate was based on a broader grep (`http\\.Error` alone, which catches non-%v calls too); the real %v-interpolation count is 1/3 of that. Documented as Deviation #1 (Rule 1 — plan estimate vs reality)."
  - "Acceptance criterion `slog.ErrorContext >= 100` was proportional to the 206-leak estimate — with 59 actual leaks and 1:1 pairing, the invariant it's checking (every redacted site gets a paired log call) holds completely. Met the spirit, not the literal threshold. Documented as Deviation #2."
  - "deb/get.go's shared `serveFile(w, r, abs, ct)` package-level helper was kept with the existing `*http.Request` parameter — no signature change needed, as `r` was already threaded through. Both its callers (servePublicKey's fallthrough and serveDistsFile / servePoolPackage) route through the helper, so both inherit the redaction without per-handler churn."
  - "OCI (`internal/protocol/oci`), S3 (`internal/protocol/s3`), and Git (`internal/protocol/git`) handlers had ZERO %v leaks — they delegate to library handlers (go-containerregistry, gofakes3, go-git v6 backend) that emit protocol-native errors, and their wrapping code already uses protocol-specific error helpers. Sweep was a no-op for those three packages; grep-confirmed before the sweep started."
  - "Task 2 (Makefile gate) was labeled `tdd=\"true\"` in the plan but Makefile recipes don't meaningfully split into RED/GREEN commits. Shipped as a single `chore(06-05): ...` commit. The companion in-process Go test `protocoltest` DID follow RED (test commit) → GREEN (sweep commit) properly, which is the meaningful TDD cycle for this plan."
  - "Generic client message chosen per error category: storage/IO/tx failures → \"storage error\"; pypi multipart parse failure → \"invalid multipart body\" (400, distinct from storage-side 500). Kept existing generic messages untouched: \"not found\", \"forbidden\", \"unauthenticated\", \"request body too large\", \"invalid path\", \"invalid filename\", \"no signing key\", etc."

patterns-established:
  - "Canonical protocol-handler redaction: 4-line slog.ErrorContext (ctx, event, incident_id, contextual-key, err) + 1-line http.Error(w, generic, status). Replaces the 1-line `http.Error(w, fmt.Sprintf(\"<op>: %v\", err), status)` anti-pattern."
  - "In-process invariant-test package (`internal/protocol/protocoltest`) hosts cross-package greps as Go tests — catches regressions at `go test ./...` time without needing a CI shell wrapper."
  - "Makefile grep gates follow the established `grep-cdn` idiom: set -e, echo what's being scanned, negate `grep` exit, print helpful fix hint on failure, echo `clean` on success, `exit 1` from the recipe."

requirements-completed: [ERR-03]

# Metrics
duration: 11 min
completed: 2026-04-17
---

# Phase 06 Plan 05: Protocol Handler ERR-03 Redaction Summary

**59 `%v`-interpolated `http.Error` leaks across 14 protocol handler files (raw/rpm/deb/pypi/helm) redacted to static generic messages with paired `slog.ErrorContext` logging keyed by `middleware.GetReqID`; in-process `protocoltest.TestNoPercentVLeakInHTTPError` + Makefile `lint-protocol-redaction` gate prevent regression via both `go test ./...` and `make test`.**

## Performance

- **Duration:** 11 min
- **Started:** 2026-04-17T12:48:37Z
- **Completed:** 2026-04-17T12:59:50Z
- **Tasks:** 2 (Task 1 TDD RED → GREEN; Task 2 single chore commit)
- **Files created:** 1 (`internal/protocol/protocoltest/redaction_test.go`)
- **Files modified:** 15 (14 handler files + Makefile)
- **Commits:** 3 (RED test, GREEN sweep, Makefile gate)

## Accomplishments

- ERR-03 closed for the protocol surface — no `%v`-interpolated Go error value reaches a protocol client's wire body anymore.
- Every redacted site logs the real error server-side via `slog.ErrorContext` keyed by `chimw.GetReqID(r.Context())`, so operators retain full internal detail correlated with the `X-Incident-Id` header the client saw (or would have seen, on protocols that don't echo it).
- Dual-gate regression prevention: Go-side `TestNoPercentVLeakInHTTPError` (runs under `go test ./...`) + Makefile `lint-protocol-redaction` wired into `test:` (runs under `make test`). Both use identical grep patterns and identical `*_test.go` exclude rules so they pass/fail in lockstep.
- Full protocol test suite green under `-race` after the sweep: `ok` for raw/rpm/deb/pypi/helm + oci/s3/git/regen + s3/backend/keys/sigv4 + protocoltest. No tests asserted the old leaky error text, so zero pre-existing tests needed updating.
- OCI/S3/Git verified to be clean before the sweep started — they delegate to library handlers that emit protocol-native errors, so no changes were needed there.

## Task Commits

1. **Task 1 RED: protocoltest failing invariant** — `8096ba8` (test)
   - New `internal/protocol/protocoltest/redaction_test.go`
   - Fails with 59 offender file:line entries in 14 files (raw/rpm/deb/pypi/helm)

2. **Task 1 GREEN: sweep across 14 handler files** — `f884e1c` (fix)
   - 59 sites redacted; 14 files modified; 386 insertions / 66 deletions
   - Adds `log/slog` + `chimw "github.com/go-chi/chi/v5/middleware"` imports where they weren't already present
   - Protocol-native error shapes preserved — clients see the same HTTP status codes and plain-text bodies they always saw

3. **Task 2: Makefile gate** — `e2e5303` (chore)
   - `.PHONY` updated to include `lint-protocol-redaction`
   - New recipe mirrors the `grep-cdn` idiom with a helpful error message + fix hint
   - `test:` target now carries `lint-protocol-redaction` as a prerequisite

**Plan metadata:** (to follow — docs commit with SUMMARY + STATE + ROADMAP)

## Files Created/Modified

### Created

- `internal/protocol/protocoltest/redaction_test.go` (95 lines)
  - `TestNoPercentVLeakInHTTPError` — greps `internal/protocol/**/*.go` (excluding `*_test.go`) for `http.Error\([^)]*%v` via `exec.Command("grep", ...)` and fails with the full offender list if any match
  - `repoRoot(t)` walks up from `runtime.Caller` to find `go.mod` so the test works regardless of where `go test` is invoked from

### Modified

| File | Sites redacted | Redaction highlights |
|------|----------------|----------------------|
| `internal/protocol/raw/get.go` | 3 | GET stat, HEAD stat, `serveFile` open |
| `internal/protocol/raw/delete.go` | 3 | stat, trash, tx commit |
| `internal/protocol/raw/listing.go` | 1 | readdir |
| `internal/protocol/rpm/get.go` | 4 | repodata stat+open, package stat+open |
| `internal/protocol/rpm/put.go` | 7 | read body, mkdir/create/write/close tmp, storage, tx commit |
| `internal/protocol/rpm/delete.go` | 3 | trash, stat, tx commit |
| `internal/protocol/deb/get.go` | 2 | `serveFile` stat+open (shared across public key / dists / pool) |
| `internal/protocol/deb/put.go` | 3 | read body, storage, tx commit |
| `internal/protocol/deb/delete.go` | 4 | trash, stat, delete tx commit, `patchSuites` tx commit |
| `internal/protocol/pypi/upload_legacy.go` | 8 | mkdir, multipart, tmp create/write, read tmp, storage, upload commit, delete commit |
| `internal/protocol/pypi/upload_pep694.go` | 6 | mkdir, tmp create/write, read staged, storage, row commit |
| `internal/protocol/helm/get.go` | 4 | index stat+open, chart stat+open |
| `internal/protocol/helm/put.go` | 8 | read body, mkdir/create/write/close tmp, storage, chart commit, provenance storage |
| `internal/protocol/helm/delete.go` | 3 | stat, trash, tx commit |
| `Makefile` | — | added `lint-protocol-redaction` target + `.PHONY` + wired into `test:` prerequisite |
| **Total** | **59** | — |

## Decisions Made

Documented inline in the frontmatter `key-decisions:` array. Summary:

1. **Actual leak count 59, not 206.** The 06-RESEARCH.md estimate was over by 3.5×. Same order of magnitude, same conclusion (sweep is tractable), same file list. Recorded as a deviation to the acceptance-criteria threshold `slog.ErrorContext >= 100`.
2. **Generic messages by category.** `"storage error"` for IO/tx; `"invalid multipart body"` for pypi multipart parse (400 path); existing concise messages (`"not found"`, `"forbidden"`, etc.) kept.
3. **OCI/S3/Git were already clean.** No changes needed — their wrapping code emits protocol-native errors (OCI JSON, S3 XML, Git pkt-line) via per-package helpers; library handlers (`go-containerregistry`, `gofakes3`, `go-git v6`) write their own errors to the wire.
4. **Task 2 TDD doesn't split.** Makefile recipes don't meaningfully divide into RED/GREEN commits. The meaningful TDD cycle (Task 1) was followed properly; Task 2 is a single chore commit.
5. **deb `serveFile` signature unchanged.** Shared helper already took `r *http.Request`, so redaction cost was 2 additional slog calls inside the function — no callsite churn.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Plan estimate drift] Acceptance criterion threshold vs actual leak count**
- **Found during:** Task 1 baseline enumeration (`grep -rnE 'http\.Error\([^)]*%v' internal/protocol/`)
- **Issue:** Plan frontmatter and acceptance criteria were sized for a ~206-leak estimate (from 06-RESEARCH.md Q2). Actual count was 59 leaks in 14 files (raw/rpm/deb/pypi/helm only — OCI/S3/Git were already clean). The explicit acceptance criterion `grep -rn "slog.ErrorContext" internal/protocol/ | wc -l | awk '$1 >= 100 { exit 0 }'` therefore fails (74 total slog calls including 15 pre-existing ones).
- **Fix:** Completed the sweep as-planned — every %v site paired with a slog.ErrorContext call. Documented the threshold divergence in SUMMARY rather than adding artificial slog calls to meet an arbitrary number. The underlying invariant ("every former %v leak is paired with a log call") holds: 59 leaks → 59 paired slog calls, plus 15 pre-existing non-leak-related ones = 74 total.
- **Files modified:** No source changes required beyond the plan's sweep; this is a documentation deviation.
- **Verification:** `grep -rnE 'http\.Error\([^)]*%v' internal/protocol/` returns zero; `grep -rnE 'http\.Error\([^)]*fmt\.Sprintf' internal/protocol/` returns zero; incident_id logged at 59 sites (≥50 threshold met); all protocol tests green.
- **Committed in:** N/A (documentation-only)

**2. [Rule 1 - Plan TDD shape mismatch] Task 2 is a single commit, not a RED/GREEN split**
- **Found during:** Task 2 planning
- **Issue:** Plan frontmatter labels Task 2 (`Makefile` grep gate) as `tdd="true"`, but a Makefile recipe addition doesn't split into meaningful RED/GREEN commits — there's no unit-testable behavior in the recipe itself. The plan's "behavior" list for Task 2 is all about the recipe contents, not a feature under test.
- **Fix:** Shipped Task 2 as a single `chore(06-05): ...` commit. The meaningful TDD cycle for the plan (RED commit = failing invariant test; GREEN commit = sweep that makes it pass) was followed for Task 1 properly.
- **Files modified:** Makefile only
- **Verification:** Regression smoke confirmed — seeded a scratch leak file, `make lint-protocol-redaction` exited non-zero and printed the expected error with fix hint; removed the seed, gate returned to clean exit 0.
- **Committed in:** `e2e5303` (Task 2 chore commit)

---

**Total deviations:** 2 auto-fixed (2 Rule 1 — plan vs reality calibration)
**Impact on plan:** No scope creep. The redaction sweep landed exactly as specified; only the planning-level counts and TDD-split expectation diverged from the plan's assumptions. Core invariants (zero %v leaks, every site paired with slog, incident_id logged, tests green, gate catches regressions) all satisfied.

## Issues Encountered

- **Pre-existing OCI vet warnings.** `go vet ./internal/protocol/oci_test` prints 7 "using resp before checking for errors" warnings at `handler_test.go:321,339` and `token_verify_test.go:24,40,58,72,94`. These existed before this plan — unrelated to the redaction sweep (no files touched in `internal/protocol/oci/` by this plan). Out of scope per SCOPE BOUNDARY; not fixed.

## User Setup Required

None — pure-code plan, no external services, no configuration knobs, no DB migration, no env vars.

## Next Phase Readiness

- **ERR-03 is fully closed.** Wave 1 (plans 06-01..06-04) covered `/api/v1`; this plan covers the protocol surface. Every user-visible error path in the OmniRepo binary now either ships the ApiErrorEnvelope (`/api/v1`) or a static generic message with the real error in slog (`internal/protocol/**`).
- **Dual-gate regression prevention.** Future changes that introduce new `%v` leaks fail both `go test ./...` (via `TestNoPercentVLeakInHTTPError`) and `make test` (via `lint-protocol-redaction`).
- **Wave 2 plan 06-05 → Phase 06 finale.** Plans 06-06 (visual primitives), 06-07 (skeleton/loading), and 06-08 (status-badge consistency / copy-inline / destructive confirm) are the remaining Phase 06 work. None depend on protocol-handler internals; they consume the ApiErrorEnvelope shape already shipped by Wave 1.
- **No blockers.**

## Self-Check: PASSED

Disk-state verification:

- `internal/protocol/protocoltest/redaction_test.go` — FOUND (95 lines)
- `internal/protocol/raw/get.go` — MODIFIED (contains `slog.ErrorContext` + `chimw.GetReqID`)
- `internal/protocol/raw/delete.go` — MODIFIED
- `internal/protocol/raw/listing.go` — MODIFIED
- `internal/protocol/rpm/get.go` — MODIFIED
- `internal/protocol/rpm/put.go` — MODIFIED
- `internal/protocol/rpm/delete.go` — MODIFIED
- `internal/protocol/deb/get.go` — MODIFIED
- `internal/protocol/deb/put.go` — MODIFIED
- `internal/protocol/deb/delete.go` — MODIFIED
- `internal/protocol/pypi/upload_legacy.go` — MODIFIED
- `internal/protocol/pypi/upload_pep694.go` — MODIFIED
- `internal/protocol/helm/get.go` — MODIFIED
- `internal/protocol/helm/put.go` — MODIFIED
- `internal/protocol/helm/delete.go` — MODIFIED
- `Makefile` contains `^lint-protocol-redaction:` — FOUND
- `Makefile` `test:` line contains `lint-protocol-redaction` prerequisite — FOUND

Commit verification:

- `8096ba8` (Task 1 RED: add failing protocoltest) — FOUND in `git log`
- `f884e1c` (Task 1 GREEN: sweep across 14 files) — FOUND
- `e2e5303` (Task 2: Makefile gate) — FOUND

Plan-level verification re-run:

- `grep -rnE 'http\.Error\([^)]*%v' internal/protocol/` → 0 matches (PASS)
- `grep -rnE 'http\.Error\([^)]*fmt\.Sprintf' internal/protocol/` → 0 matches (PASS)
- `grep -rn "slog.ErrorContext" internal/protocol/ | wc -l` → 74 (underlying invariant PASS; literal threshold 100 documented as Deviation #1)
- `grep -rn "chimw.GetReqID\|middleware.GetReqID" internal/protocol/ | wc -l` → 59 (PASS — threshold was 50)
- `go test -race ./internal/protocol/... -count=1` → all 16 packages `ok` (PASS)
- `go vet ./internal/protocol/raw/... ./internal/protocol/rpm/... ./internal/protocol/deb/... ./internal/protocol/pypi/... ./internal/protocol/helm/... ./internal/protocol/protocoltest/...` → clean (PASS; OCI test vet warnings pre-existed and are out of scope)
- `go build ./...` → clean (PASS)
- `make lint-protocol-redaction` → exits 0 (PASS)
- Regression smoke: seeded `internal/protocol/raw/__temp_leak.go` with `%v` → `make lint-protocol-redaction` exits non-zero with offender listed + fix hint; removing the seed returns the gate to exit 0 (PASS)

## TDD Gate Compliance

Plan frontmatter is `type: execute` (not `type: tdd`), so plan-level RED/GREEN/REFACTOR gate enforcement does not apply. Per-task TDD adherence:

- **Task 1** (`tdd="true"`): RED `8096ba8` (test) → GREEN `f884e1c` (fix) — compliant. The RED commit contains only the failing invariant test; the GREEN commit contains the sweep that makes it pass. No REFACTOR needed (initial redaction shape was already minimal/canonical).
- **Task 2** (`tdd="true"`): Shipped as single `chore(06-05): ...` commit. Documented as Deviation #2 — Makefile recipe additions don't split into meaningful RED/GREEN.

---
*Phase: 06-error-envelope-visual-foundation*
*Completed: 2026-04-17*
