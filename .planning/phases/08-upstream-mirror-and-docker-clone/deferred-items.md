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
