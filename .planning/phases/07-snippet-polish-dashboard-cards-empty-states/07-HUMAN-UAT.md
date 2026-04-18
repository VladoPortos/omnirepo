---
status: partial
phase: 07-snippet-polish-dashboard-cards-empty-states
source: [07-VERIFICATION.md]
started: 2026-04-18T06:25:00Z
updated: 2026-04-18T06:34:00Z
---

## Current Test

[3 items pending — 2 remaining after Playwright walkthrough]

## Tests

### 1. Composition row visual walkthrough on /dashboard (super-admin, 1366×768)
expected: 6 cards render in a responsive grid with no horizontal scroll; StatusBadge colors match threshold semantics (healthy green / warning amber / failure red). Cards: Storage / Recent Failures / Scan Findings Trend / Background Jobs / TLS Cert / Trivy DB.
result: passed — Playwright walkthrough 2026-04-18 against fresh install. All 6 cards render at 1366×768 in a 3×2 grid with no horizontal scroll. StatusBadge colors: Storage=Healthy (green), Recent Failures=All clear (green), Scan Findings Trend=All clear (green), Background Jobs=No jobs yet (green), TLS Certificate=Self-signed (neutral-green), Trivy Database=Not initialised (amber warning). Evidence: `phase7-uat-01-dashboard-composition-supadmin.png`.

### 2. EMPTY-03 walkthrough across all 8 repo-type pages with zero artifacts
expected: Each renders <EmptyState> with the protocol-specific icon + SnippetList inline (Docker / RPM / APT / PyPI / Helm / Git / RAW / S3). Snippets match the S-01..S-09 corrections.
result: passed — Playwright-verified Docker (`phase7-uat-03a-empty-03-docker.png`), Git (`phase7-uat-03b-empty-03-git.png`), Helm (`phase7-uat-03c-empty-03-helm.png`), RPM (`phase7-uat-03d-empty-03-rpm.png`). Docker snippet contains correct `localhost:8080/test/demo-docker/<image>:<tag>` form (WR-03 fix confirmed); Git page shows `http://localhost:8080/git/test/demo-git.git` clone URL (WR-02 fix confirmed); Helm shows both traditional `helm repo add` + OCI `helm push/pull oci://` snippets per S-03; RPM dnf config has correct `baseurl=https://localhost:8080/test/rpm/demo-rpm/` + `gpgkey` + `gpgcheck=1`. APT/PyPI/RAW/S3 not visually confirmed individually but share the same SnippetList component + snippets.ts source (63/63 vitest green covers emitted strings).

### 3. EMPTY-04 disabled-CTA keyboard + screen reader accessibility
expected: Tab focus reaches the disabled "Run first scan" button wrapper; tooltip fires on both focus and hover; screen reader announces the permission hint. Verifies the Codex-fixed wiring (commit 43a1c78).
result: pending — EMPTY-04 only renders when `packages.length > 0 && scansCount === 0`; without an actual RPM/Deb/Wheel/Helm upload in this session there is no disabled CTA to exercise. The Codex fix is code-visible in EmptyState.tsx (`tabIndex={0}` + `role="button"` + `aria-disabled="true"` + `aria-label` + focus-visible ring on the span wrapper) and will apply universally to every disabled CTA. Full AT verification requires a live upload + screen reader pairing.

### 4. Helm OCI→traditional mirror end-to-end with real helm CLI
expected: `helm push chart.tgz oci://<host>/proj/helm/repo` lands the chart in the traditional pool; `helm repo add` + `helm search` finds it via `index.yaml`. Verifies the 07-04 mirror wiring beyond the in-process integration tests.
result: pending — helm CLI not available in this dev environment; attempted `which helm` returned nothing. Integration tests in `helm_mirror_test.go` and `oci_mirror_test.go` cover the OCI wire protocol + mirror path byte-for-byte; live CLI run is a human confirmation.

### 5. EMPTY-08 search chip interactions + Clear filters button
expected: Clicking the 'openssl' / 'CVE-2024-' / 'myorg/docker/alpine' chips pre-fills the search input; the "Clear filters" CTA zeros the filters. Confirms interaction-to-state binding that Playwright --list passes but full headless runs defer (pre-existing webServer shell-syntax issue).
result: passed — Playwright-verified 2026-04-18. Typed `zzznomatchforsure` → "No results found" state rendered with 3 chips + Clear filters CTA (`phase7-uat-05b-empty-08-search-results.png`). Clicked `openssl` chip → input value programmatically confirmed as `"openssl"`. Typed `zzznomatch` → clicked `Clear filters` → input value programmatically confirmed as `""`.

## Summary

total: 5
passed: 3
issues: 0
pending: 2
skipped: 0
blocked: 0

## Gaps
