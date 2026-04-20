# Phase 8 deferred items

## Pre-existing `make test` typography lint failures (NOT introduced by Phase 8)

`make test` fails the `lint-typography` gate on files that predate Phase 8
and were NOT touched by plan 08-01:

- web/src/App.tsx:57 — `font-medium`
- web/src/components/common/ArtifactDetail.tsx:163,170 — `font-medium`
- web/src/pages/repo/AptRepoPage.tsx:187 — `font-medium`
- web/src/pages/repo/ScanReportPage.tsx:186,206,297,298,299,300,301,302 — `font-medium`

These failures reproduce on the `main` commit immediately before Plan 08-01
started (`87dcdd8` stashed away), so they are a pre-existing regression in
the Phase-6 lint discipline. The paths belong to Phase 7 walkthrough
commits and Phase 5 UI work.

**Resolution path (out of scope for 08-01):** either fix the offending
call sites to use `font-semibold` (the Phase-6-approved alternative) or
add the file basenames to `scripts/typography-allowlist.txt` if the
legacy usage is intentional. Tracked here so a later plan (08-06 or a
standalone walkthrough micro-fix) can close it.

- Discovered: 2026-04-20 during 08-01 execution
- Pre-existing: verified via `git stash` on main at HEAD=87dcdd8

## Pre-existing `make grep-cdn` failures (NOT introduced by Phase 8 Plan 02)

`make grep-cdn` fails on external URL strings introduced by plan 08-01
(caf0a4a) in handler test files. These were not caught by the phase-08-01
close-out self-check because plan 08-01 didn't run the `grep-cdn` gate.

- internal/protocol/rpm/handler_test.go — `https://mirror.centos.org/centos/9` (mirror-guard integration test fixture)
- internal/protocol/deb/handler_test.go — `https://archive.ubuntu.com/ubuntu`
- internal/protocol/pypi/upload_legacy_test.go — `https://pypi.org/simple/`
- internal/protocol/pypi/upload_pep694_test.go — `https://pypi.org/simple/`
- internal/protocol/helm/handler_test.go — `https://charts.bitnami.com/bitnami`

Plan 08-02 does NOT introduce any new external URLs (the new
sync_progress_test.go files use only `httptest.NewServer`'s localhost URL).
Fix path: either replace these plan-08-01 fixture URLs with the whitelisted
`upstream.example` / `repo.example` hosts, or extend the grep-cdn allowlist
in the Makefile for domains commonly used as stable upstream examples.

- Discovered: 2026-04-20 during 08-02 self-check
- Pre-existing source: commit caf0a4a (plan 08-01 Task 3)

## Plan 08-04 UI-placeholder URLs (cosmetic, not fetched)

Plan 08-04's `MirrorConfigSection.tsx:protocolPlaceholder` uses the same
four upstream domains as `<input placeholder=...>` hints so the operator
knows what kind of URL to paste:

- `https://archive.ubuntu.com/ubuntu` (APT)
- `https://mirror.centos.org/centos/9/BaseOS/x86_64/os/` (RPM)
- `https://pypi.org/simple/` (PyPI)
- `https://charts.bitnami.com/bitnami` (Helm)

These strings are never fetched — they exist only as placeholder text for
empty form fields. The `grep-cdn` check is pattern-based and can't tell
the difference; the same fix paths listed above (allowlist known upstream
hosts, or refactor to generic `https://example.upstream/`) resolve both
08-01's test fixtures and 08-04's placeholders in one go.

- Discovered: 2026-04-20 during 08-04 lint check
- Source: commit 2e96abe (plan 08-04 Task 1)
