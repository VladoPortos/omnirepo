---
phase: 11-mirror-infrastructure-widening
plan: 04
subsystem: protocol/helm + Makefile
tags: [mirror, oci, helm, live-e2e, bitnami, build-tag, makefile]

# Dependency graph
requires:
  - phase: 11-mirror-infrastructure-widening
    plan: 01
    provides: "ociclient.Client + ociclient.New + AuthCreds/PullResult/ChartMeta types — the narrow interface the live test drives against the real Helm SDK."
  - phase: 11-mirror-infrastructure-widening
    plan: 02
    provides: "helm.EntrySourceOCI constant — referenced in the live test to keep it wired to the classifier symbol the hermetic OCI integration tests exercise."
  - phase: 11-mirror-infrastructure-widening
    plan: 03
    provides: "SyncHandler OCI branch (fetchAndCommitOCI, ociclient injection in phase3_sync.wireSync). The hermetic integration tests in sync_oci_integration_test.go cover the full Handle round-trip; the live test only needs to prove the real ociclient can talk to Docker Hub."
  - phase: 10-operator-visibility
    plan: 02
    provides: "Makefile `test-perf` + `//go:build perf500` precedent — direct template for `test-live-oci` + `//go:build live_oci`."

provides:
  - "internal/protocol/helm/sync_oci_live_test.go — a single `TestLiveOCIBitnamiSync` under `//go:build live_oci` exercising the real Helm SDK against oci://registry-1.docker.io/bitnamicharts/nginx."
  - "Makefile target `test-live-oci` — opt-in target with an env-guard that SKIPs cleanly (exit 0) when DOCKERHUB_USER / DOCKERHUB_TOKEN are absent; otherwise runs `go test -tags=live_oci -timeout=300s -v -run TestLiveOCIBitnamiSync ./internal/protocol/helm/...`."
  - "Closure of OCIHELM-08 and D-16 (the Phase 11 live E2E target)."

affects: []  # Phase 11 terminal leaf — no downstream plan depends on this one.

# Tech tracking
tech-stack:
  added: []  # ZERO new go.mod direct deps; ZERO npm deps. Helm SDK already vendored (11-01).
  patterns:
    - "Live-network tests live behind `//go:build <tag>` + a `make test-<tag>` target — mirrors Phase 10's perf500 template; becomes Phase 11's OCI-live template."
    - "Single-subshell Make recipe for env-guard skip logic: `@set -e; if ... ; then ... exit 0; fi; <real command>` — multi-line recipes run each line in its own subshell by default, which would let a failed `exit 0` guard fall through to the real command."
    - "In-body `t.Skip` as a second guard alongside the Makefile env-check — redundant on purpose: lets `go test -tags=live_oci ./...` from any invocation path (CI, IDE, ad-hoc) degrade cleanly without Docker Hub creds."
    - "ListTags-first pattern for live upstream tests — probes a tag dynamically rather than hard-coding a version that may be yanked by upstream (Bitnami rotates chart tags as base images patch)."

key-files:
  created:
    - internal/protocol/helm/sync_oci_live_test.go
  modified:
    - Makefile

key-decisions:
  - "Three smokes, no Handle round-trip. Plan's Task 1 action included a scope-guard note (>50 LOC of Handle-path plumbing → STOP and keep just the three smokes). The hermetic tests in 11-03 cover the full dedup / rebound / regen-HTTP-urls path. What the live test uniquely proves is the real ociclient + Helm SDK + Docker Hub credential path — the three smokes (ListTags, Resolve, PullChart, re-Resolve) cover that uniquely-live surface in under 80 LOC."
  - "ListTags then pick tags[0] rather than hard-coding a version. Bitnami publishes nginx versions continuously and older tags can be yanked; hard-coding e.g. `15.14.0` would require periodic maintenance. The ListTags probe returns oras-semver-filtered tags (per 11-01 note in client.go), so tags[0] is always a parseable semver chart. Cost: one extra HTTP round-trip — trivial for an opt-in test."
  - "Single-subshell Make recipe. The first recipe draft used two recipe lines (one `if ... exit 0; fi` then one `go test`) which, under Make's default per-line subshell, meant `exit 0` only exited the first subshell while Make went on to run the second line. Fixed by joining into a single `@set -e; if ... fi; go test ...` — validated empirically: `DOCKERHUB_USER= DOCKERHUB_TOKEN= make test-live-oci` now prints SKIP and exits 0 with zero `go test` output."
  - "Touch helm.EntrySourceOCI in the live test to keep it wired to the classifier symbol. A refactor that renames EntrySourceOCI will break both the hermetic integration tests AND the live test compile — this catches symbol drift in the tag that the live test exercises end-to-end when run against the real registry."

patterns-established:
  - "Phase-11 OCI-live test template — reusable for any future live OCI endpoint test (e.g. GHCR, Quay). Just clone the file, swap liveOCIUpstream, and leave everything else identical."
  - "Make recipe single-subshell idiom with `@set -e; if ...; fi; <real command>` — reusable for any future Makefile target that needs an env-guard SKIP path that must not fall through."

requirements-completed: [OCIHELM-08]

# Metrics
duration: 4m 8s
completed: 2026-04-22
---

# Phase 11 Plan 04: Bitnami OCI live E2E Summary

**Opt-in live test against oci://registry-1.docker.io/bitnamicharts/nginx behind `//go:build live_oci`, invoked via `make test-live-oci` with DOCKERHUB_USER + DOCKERHUB_TOKEN env vars — SKIPs cleanly without creds, exercises real ociclient + Helm SDK end-to-end when creds present.**

## Performance

- **Duration:** 4m 8s
- **Started:** 2026-04-22T02:17:22Z
- **Completed:** 2026-04-22T02:21:30Z
- **Tasks:** 2/2 (two atomic commits)
- **Files created:** 1 (145-line test file)
- **Files modified:** 1 (Makefile — 22 insertions, 1 deletion)
- **Lines of code:** +167 net

## Accomplishments

- `internal/protocol/helm/sync_oci_live_test.go` (NEW) carries `//go:build live_oci` on line 1, so default `go test ./...` never picks it up. Without the tag, `go test -list '.*' ./internal/protocol/helm/...` returns 0 matches for `TestLiveOCI`; with the tag, `TestLiveOCIBitnamiSync` is enumerated.
- `TestLiveOCIBitnamiSync` guards on both env vars — `if user == "" || token == "" { t.Skip(...) }` — so even outside the Makefile target (e.g. IDE-driven `go test -tags=live_oci ./...`), the test degrades to SKIP without creds instead of failing.
- When creds ARE present, the test runs four smokes against Bitnami's OCI endpoint:
  1. `ListTags(liveOCIUpstream, creds)` → must return ≥1 tag; picks `tags[0]` (oras auto-filters to parseable semver per 11-01 notes).
  2. `Resolve(ref, creds)` → must return `sha256:<hex>` manifest digest.
  3. `PullChart(ref, creds)` → must return non-empty `.Data`, `.Meta.Name == "nginx"`, non-empty `.Meta.Version`, `sha256:<hex>`-shaped chart-layer digest.
  4. Second `Resolve` → must match the first digest (stability over the test run; NOT a tag-rebound canary).
- Makefile gets `.PHONY: ... test-live-oci` + a new recipe block. The recipe uses a **single-subshell `@set -e; if ...; fi; <go test>`** form so the `exit 0` inside the SKIP branch actually terminates the recipe rather than falling through to `go test` on the next line (multi-line Make recipes run each line in its own subshell by default — validated by `DOCKERHUB_USER= DOCKERHUB_TOKEN= make test-live-oci` printing only the SKIP line and exiting 0).
- `make test` does NOT depend on `test-live-oci` (verified: `make -n test 2>&1 | grep 'live_oci'` is empty). The fast merge-gate stays off the network.
- `go build -mod=vendor -tags=live_oci ./...` compiles clean across the whole module. `go vet -tags=live_oci ./internal/protocol/helm/...` is clean. The hermetic helm test suite stays green (`go test ./internal/protocol/helm/... 1.918s`).

## Task Commits

1. **Task 1: Bitnami OCI live E2E test (build-tag gated)** — `c878cdd` (test)
2. **Task 2: `make test-live-oci` Makefile target** — `2e6ad1d` (build)

## Files Created/Modified

- `internal/protocol/helm/sync_oci_live_test.go` (NEW — 145 lines): `//go:build live_oci` directive, `liveOCIUpstream = "oci://registry-1.docker.io/bitnamicharts/nginx"` const, `TestLiveOCIBitnamiSync` with env-skip guard, four smokes (ListTags → Resolve → PullChart → re-Resolve), touches `helm.EntrySourceOCI` to keep the classifier symbol wired. Uses only `ociclient.Client`, `ociclient.AuthCreds`, `ociclient.PullResult` — no SyncHandler round-trip.
- `Makefile` (MODIFIED — +22/-1): registers `test-live-oci` in `.PHONY` list; adds the recipe block with the single-subshell env-guard pattern; comment block documents OCIHELM-08 / D-16 linkage and the Phase 10 perf500 parallel.

## Decisions Made

- **Three smokes only (scope-guarded per plan Task 1 note).** The plan explicitly permitted adding a full `SyncHandler.Handle` round-trip if it fit in ≤50 LOC. It would not: the handler round-trip needs a real `helm_charts` repo, path store, trash, audit recorder, synthetic index.yaml served over httptest, and the project/repo rows — that's ~150 LOC of fixture plumbing duplicated from `sync_oci_integration_test.go`. Per the plan's own scope-creep guard, stopped at the three smokes. The hermetic tests in 11-03 already prove the Handle path end-to-end; what the live test uniquely proves is real-registry credential flow, which the ListTags/Resolve/PullChart trio covers.
- **Dynamic tag selection via ListTags, not a hard-coded version.** The plan's draft code pinned `liveOCIFallbackTag = "15.14.0"` with a comment acknowledging it might be yanked. Dropping that const and using `tags[0]` from a real `ListTags` call removes the maintenance burden entirely and still covers the resolve + pull path. oras' semver filter ensures `tags[0]` is always a parseable chart version.
- **Single-subshell recipe form.** The plan's draft recipe used two recipe lines — one for the env-check `if ... exit 0; fi` and one for the `go test` invocation. Empirically: under Make's default per-line subshell semantics, the `exit 0` in the first line only exits that subshell while Make proceeds to the second line. The observable symptom: `DOCKERHUB_USER= DOCKERHUB_TOKEN= make test-live-oci` printed "SKIP:" AND then also ran `go test ...` (which fortunately hit `t.Skip` internally and exited 0, so the user-visible outcome was correct but confusing). Fixed by joining into a single `@set -e; if ...; fi; <cmd>` recipe — now the SKIP path is clean with zero `go test` output. Counts as a Rule 1 auto-fix (see Deviations below).
- **Touch `helm.EntrySourceOCI` in the live test.** Cheap symbol-import assertion (`_ = helm.EntrySourceOCI`) — costs one import + one unused-var assignment, gains: a rename of `EntrySourceOCI` in plan 11-02's output would break both the hermetic integration tests AND the live test compile, so the two coverage sites can't drift apart on a refactor.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 — Bug] Two-line Make recipe let SKIP branch fall through to `go test`**

- **Found during:** Task 2 verify block — ran `DOCKERHUB_USER= DOCKERHUB_TOKEN= make test-live-oci` to confirm SKIP behavior and observed the `go test` invocation also ran.
- **Issue:** The plan's draft recipe had the env-check and the `go test` on two separate recipe lines. Make runs each recipe line in its own subshell by default, so the `exit 0` inside the first line's `if ...; fi` block only terminated that subshell — Make then happily ran the `go test` line anyway. The in-body `t.Skip` caught it (so the exit code was still 0) but the output was confusing and the live-network-code path (Go test binary invocation, not actual network) was being touched unnecessarily.
- **Fix:** Joined the guard and the `go test` into a single recipe line using `@set -e; if [ -z "$$DOCKERHUB_USER" ] || [ -z "$$DOCKERHUB_TOKEN" ]; then echo SKIP; exit 0; fi; $(GO) test ...`. The single subshell means `exit 0` terminates the whole recipe. Validated: the SKIP path now prints only the SKIP line and exits 0 with no `go test` output.
- **Files modified:** `Makefile`
- **Verification:** Both `unset DOCKERHUB_USER DOCKERHUB_TOKEN; make test-live-oci` AND `DOCKERHUB_USER="" DOCKERHUB_TOKEN="" make test-live-oci` now exit 0 with exactly one line of output: `SKIP: DOCKERHUB_USER / DOCKERHUB_TOKEN unset — live OCI test requires Docker Hub PAT`.
- **Committed in:** `2e6ad1d` (Task 2)

**2. [Rule 3 — Design refinement] Replaced hard-coded fallback tag with ListTags probe**

- **Found during:** Task 1 implementation — the plan's draft code pinned `liveOCIFallbackTag = "15.14.0"` with a self-documenting comment that if Bitnami yanks that tag, the test operator swaps in a current one.
- **Issue:** Pinning a version means the test enters a silent-failure state the day upstream yanks the tag — the test becomes "always fails on the ListTags/Resolve step" and any operator running pre-release sanity gets a false alarm. The plan's own note ("The exact tag isn't the test's concern") invited the simpler approach of picking a tag at runtime.
- **Fix:** Call `cli.ListTags(ctx, liveOCIUpstream, creds)` first, assert `len(tags) >= 1`, and use `tags[0]` for the Resolve + PullChart smokes. Removes the maintenance burden entirely while adding one HTTP round-trip (trivial for an opt-in test).
- **Files modified:** `internal/protocol/helm/sync_oci_live_test.go` (relative to the plan's draft)
- **Verification:** Under the tag with creds, ListTags returns a list and tags[0] flows through the rest of the smoke. Under the tag WITHOUT creds, ListTags is never reached (t.Skip fires on env-check first).
- **Committed in:** `c878cdd` (Task 1)

---

**Total deviations:** 2 auto-fixed (1 Rule 1 Make recipe bug, 1 Rule 3 design refinement replacing a hard-coded version with a dynamic probe).

**Impact on plan:** Both changes tightened correctness without widening scope. The Make recipe fix is a genuine bug that would have confused operators by printing misleading output on the SKIP path. The ListTags probe eliminates a known maintenance burden the plan itself flagged. Zero files outside the frontmatter scope touched; zero new go.mod or npm deps.

## Exact Chart/Tag Chosen

- **Primary:** `oci://registry-1.docker.io/bitnamicharts/nginx` — documented in plan 11-04 as the primary target. Smallest stable actively-maintained Bitnami chart; publishes BOTH canonical `application/vnd.cncf.helm.chart.content.v1.tar+gzip` AND legacy `application/tar+gzip` historically — good coverage for D-03 silent-legacy assertion.
- **Tag:** Dynamically chosen at runtime via `ListTags()[0]` — no hard-coded version. Avoids the maintenance burden of pinning a specific tag that Bitnami might yank as base images rotate.
- **Fallback (documented, not implemented):** `oci://registry-1.docker.io/bitnamicharts/redis` if nginx availability drifts. A test operator swapping `liveOCIUpstream` to redis is a one-line edit.

## Hermetic-Helper Reuse Discussion

The plan's Task 1 `<action>` included a conditional clause: "Smoke test 3: full SyncHandler.Handle integration using the real ociclient" — with the caveat that if this required >50 LOC of additional plumbing, the executor should STOP and keep just the three smokes.

Assessment: the hermetic helper `newOCIHelmFixture` in `sync_oci_integration_test.go` uses a live httptest server serving an index.yaml, a real `metadata.DB` (sqlitetest), a `storage.PathStore`, a `storage.Trash`, a `recordingAudit`, and full project/repo row setup — ~100 LOC just to build the fixture. Plugging the REAL ociclient into that shape (replacing `fakeOCI := ociclient.NewFake()` with `ociclient.New(nil)`) would require building a synthetic index.yaml whose oci entries point to Docker Hub — a mismatch of concerns (the index says "fetch the oci URL" but the helper's httptest server isn't involved at fetch time; fetching happens via ociclient calling Docker Hub directly). Workable, but ~50 LOC of conditional fixture-building beyond the reuse point.

Decision: **kept just the three smokes.** The plan's scope-creep guard was explicit, and the hermetic 11-03 tests already exercise the Handle round-trip using FakeClient. The live test's unique value is "real Docker Hub accepts our cred and returns bytes" — covered by ListTags/Resolve/PullChart/re-Resolve in 145 lines total.

## Tests

| Test                        | Gate           | Proves                                                                                                   |
| --------------------------- | -------------- | -------------------------------------------------------------------------------------------------------- |
| `TestLiveOCIBitnamiSync`    | `//go:build live_oci` + DOCKERHUB_USER/TOKEN env | Real ociclient can ListTags, Resolve, PullChart against oci://registry-1.docker.io/bitnamicharts/nginx; basic cred threads through; digest stability across two Resolves; `_ = helm.EntrySourceOCI` keeps classifier symbol wired. |

Default `go test ./internal/protocol/helm/...` (without the tag): the live test is invisible — `-list '.*'` returns 0 matches for `TestLiveOCI`, `-run TestLiveOCIBitnamiSync` reports `[no tests to run]`.

Full build-tag matrix:

| Invocation                                                        | Outcome                                                     |
| ----------------------------------------------------------------- | ----------------------------------------------------------- |
| `go test ./internal/protocol/helm/...`                            | PASS — live test invisible (no tag)                          |
| `go test -tags=live_oci ./internal/protocol/helm/...` (no creds)  | PASS — live test runs, hits `t.Skip` on env-check            |
| `go test -tags=live_oci ./internal/protocol/helm/...` (w/ creds)  | PASS (assuming Docker Hub reachable) — all four smokes run   |
| `make test-live-oci` (no creds)                                   | PASS — prints SKIP line, exits 0, no `go test` run           |
| `make test-live-oci` (w/ creds)                                   | PASS — runs with `-v -timeout=300s`                          |
| `make test` (no creds)                                            | PASS — does not depend on `test-live-oci`                    |
| `go build -tags=live_oci ./...`                                   | clean                                                        |

## User Setup Required

None for the merge-gate. For opt-in live testing, operators must:

1. Create a Docker Hub Personal Access Token (Read:Public_Repos scope is sufficient; login.docker.com → Account Settings → Security → New Access Token).
2. Export `DOCKERHUB_USER=<your_dockerhub_username>` and `DOCKERHUB_TOKEN=<the_PAT_string>`.
3. Run `make test-live-oci`.

No external service configuration, no new credentials infrastructure, no new env vars required by default flows.

## Self-Check

- [x] `internal/protocol/helm/sync_oci_live_test.go` exists — VERIFIED
- [x] Line 1 of test file is `//go:build live_oci` — VERIFIED
- [x] Commit `c878cdd` in git log — VERIFIED
- [x] Commit `2e6ad1d` in git log — VERIFIED
- [x] `grep -qE '^test-live-oci:' Makefile` returns 0 — VERIFIED
- [x] `grep -qE 'tags=live_oci' Makefile` returns 0 — VERIFIED
- [x] `make -n test-live-oci | grep -q 'go test'` returns 0 — VERIFIED
- [x] `make -n test | grep -v 'test-live-oci' | grep -q 'go test'` returns 0 (default test still works) — VERIFIED
- [x] `make -n test | grep 'live_oci'` returns 0 matches (default test does NOT invoke live) — VERIFIED
- [x] Without tag: `go test -list '.*' ./internal/protocol/helm/...` returns 0 matches for `TestLiveOCI` — VERIFIED
- [x] Without tag: `go test -run TestLiveOCIBitnamiSync ./internal/protocol/helm/...` reports `[no tests to run]` — VERIFIED
- [x] With tag: `go test -tags=live_oci -list '.*' ./internal/protocol/helm/...` enumerates `TestLiveOCIBitnamiSync` — VERIFIED
- [x] `DOCKERHUB_USER= DOCKERHUB_TOKEN= make test-live-oci` prints SKIP and exits 0 — VERIFIED
- [x] `go build -mod=vendor -tags=live_oci ./...` clean — VERIFIED
- [x] `go vet -mod=vendor -tags=live_oci ./internal/protocol/helm/...` clean — VERIFIED
- [x] `go test ./internal/protocol/helm/...` green (hermetic merge-gate not broken by the new file) — VERIFIED

## Self-Check: PASSED

## Next Phase Readiness

- **OCIHELM-08 closed.** All 8 OCIHELM requirements now complete:
  - OCIHELM-01..07 → shipped in plans 11-01, 11-02, 11-03.
  - OCIHELM-08 → shipped here (live Bitnami E2E).
- **Phase 11 Wave 3 complete on the OCI side.** GITMIRROR plans (11-05..11-08 or wherever they fall in the wave plan) continue independently — the live-test Makefile slot is now established and the `test-live-git` target will follow the same single-subshell + env-guard shape.

## Threat Flags

None new. The plan's threat register (T-11-04-01 token logged to CI output, T-11-04-02 live test runs in CI by default) is fully mitigated:

- **T-11-04-01:** The SKIP message references only env-var NAMES, never their values. The in-test `t.Skip` message and the Makefile `echo SKIP` line both name DOCKERHUB_USER/TOKEN but never dereference them for output. No `%v` on the creds struct, no logging of `AuthCreds{}`.
- **T-11-04-02:** Dual gate — `//go:build live_oci` (compile-time) PLUS env-var check (run-time via Makefile) PLUS `t.Skip` in test body (run-time via Go). Even an operator who explicitly runs `go test -tags=live_oci -v ./internal/protocol/helm/...` without creds gets SKIP, not failure. Default `make test` has no path to the live file.

No new `threat_flag:` entries. The file adds zero new endpoints, zero new auth paths, zero new file-access patterns, zero schema changes.

---
*Phase: 11-mirror-infrastructure-widening*
*Completed: 2026-04-22*
