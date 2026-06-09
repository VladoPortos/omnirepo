# Changelog

All notable changes to OmniRepo are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project follows a milestone-based versioning scheme — minor versions
bundle a coherent set of phases (typically 3–8), patch versions are reserved
for security fixes against an active minor.

## [Unreleased]

### Added
- GitHub Actions security automation: CodeQL (Go + JS/TS), Trivy (filesystem
  + Dockerfile + image), govulncheck, OpenSSF Scorecard, Dependabot.
- Repo metadata: `LICENSE` (Apache-2.0), `SECURITY.md`, `NOTICE`,
  `CONTRIBUTING.md`, `CHANGELOG.md`, PR template, `.editorconfig`,
  `.gitattributes`.

## [v1.0.1] — 2026-06-09

Security and correctness patch against v1.0, plus a dead-code / duplication
cleanup pass. No new features; no functional change to any documented config.

### Security
- **S3 object delete** now rejects path-traversal keys. `DeleteObject` /
  `DeleteMulti` joined the raw object key into a filesystem path and removed
  it with no validation, and the DB delete is idempotent (no matching row
  required) — an authenticated project S3 key could delete a file outside its
  bucket (e.g. `../../certs/server.key`).
- **S3 multipart abort** now rejects path-traversal upload ids. The abort
  cleanup `RemoveAll`'d the per-upload staging directory even for an unknown
  upload, so a crafted `uploadId` could recursively delete an arbitrary
  directory tree.

### Fixed
- **Drift purge**: trash moves are deferred until after the purge transaction
  commits, so a rolled-back multi-row purge can no longer strand restored rows
  against already-trashed files (rpm/deb/pypi/helm).
- **Git audit**: push (`git.refs.synced`) and fetch (`git.fetch`) events now
  carry the authenticated actor (`actor_user_id` / `actor_api_key_id`);
  previously authenticated Git activity appeared anonymous in the audit log.
- **OCI**: failed `pull_external` jobs now emit a terminal
  `oci.pull_external.failed` audit event — a failure previously left a
  `started` event with no resolution in the trail.

### Removed
- Dead config knobs that were parsed but never read: `auth.docker_jwt_ttl`
  (the live knob is `docker.jwt_ttl_seconds`), `scan.auto_scan_default`,
  `scan.db_warn_age_days` (read from the settings table, not config), and the
  `air_gap` block / `air_gap.allow_external_actions` (never enforced).
- Internal dead-code and duplication cleanup: unused exported identifiers,
  methods, and struct fields removed; per-protocol drift-audit emission and
  registry-shutdown helpers consolidated.

## [v1.8] — 2026-04-26

Walkthrough #4 close-out. 5 findings opened, 5 closed (3 BLOCKERs +
2 R-bugs).

### Fixed
- **F-04.1** — last-super-admin demotion / deletion guard.
- **F-04.2** — minimum password floor enforcement.
- **F-06.1** — RPM mirror rejected upstreams whose `primary.xml` was
  compressed with `xz` or `zst`; codec list now matches dnf.
- **F-12.1** — S3 multipart uploads now emit `Last-Modified` on
  `HeadObject` / `GetObject` for every object, including parts.

### Verified
- `go test ./...` green across 39 packages.
- Docker image rebuilt; protocol round-trips re-verified against live
  upstreams (Docker Hub, charts.bitnami.com, etc.) and Trivy scan
  pipeline confirmed end-to-end.

## [v1.6] — 2026-04-25

QA hardening release — 7 phases, 48 requirements, 14 audit findings
closed, independently peer-reviewed.

### Phases shipped
- **Phase 1** — Lifecycle correctness (audits #1, #8, #9, #18).
- **Phase 2** — S3 protocol hardening (audits #2, #10, #11, #14).
- **Phase 3** — Audit attribution (audit #7).
- **Phase 4** — Atomic delete ordering (audit #6).
- **Phase 5** — Streaming I/O for large artifacts (audits #3, #4, #5).
- **Phase 6** — Operational consistency (audits #12, #17).
- **Phase 7** — Frontend correctness (audits #13, #15).

## [v1.5] — 2026-04-25

Stability & RBAC depth — 6 phases, 28 plans shipped 2026-04-23 → 2026-04-25.

### Added
- **Phase 1** — E2E state isolation: `POST /api/v1/admin/_reset` (gated
  on `OMNIREPO_DEV`); `workers: 1` in `playwright.config.ts` is now
  load-bearing for the reset contract. 92/97 e2e green post-Phase-1.1.
- **Phase 2** — Maintainer / viewer RBAC split: `project_members.role`
  column, `RequireCanWith(ActionUpdateRepo)` middleware, surface on
  `/me` and `/projects/{name}`.

## [v1.4] — 2026-04-23

Walkthrough #3 (WT3) fix batch — 134 commits since v1.3, 15 batches
signed off.

### Added
- **Admin** — TLS cert upload + history + hot-reload (BATCH 14).
- **Admin** — SQLite health card, manual integrity check, rate-limiter.
- **Admin** — Trash: soft-deleted projects listed + collision-aware
  restore + repo-gate `HardDelete` + panic-safe audit.
- **Admin** — Audit filter facets via `/admin/audit/facets`; dropdowns
  match real data.

## [v1.3] — 2026-04-21

Walkthrough #2 fix batch — 41 commits since v1.2.

### Fixed
- **F-13** — severity filter case-mismatch in search.
- **F-14** — `/admin/*` route-level super-admin gate.
- **F-16** — Git browse handled refs containing `/` (e.g. `feature/x`,
  `release/v1.2`).

## [v1.2] — 2026-04-20

"Polish & Stability" — close the quality debt accumulated during v1.1.

### Added
- **Sync success pill** — `SyncNowButton` renders
  `Sync complete · N files · X MB` with healthy-variant `StatusBadge`,
  8s auto-dismiss, `role="status"` + `aria-live`.
- Two new lint gates.
- End-to-end verification of PyPI and Helm mirror protocols against
  live upstreams (parity with the APT/RPM/Docker live tests from v1.1).

## [v1.0] — 2026-04-17

MVP. Single Go binary serving OCI/Docker, RPM/YUM, APT/Debian, PyPI,
Helm, RAW, S3 (SigV4), and Git on one HTTP/HTTPS port.

### Added
- Embedded React 19 SPA.
- Trivy-powered vulnerability scanning.
- Project-scoped access control.
- Hard "no outbound network at runtime" invariant.

### Stats
- 5 phases, 52 plans, 175 requirements, 230 commits
  (2026-04-14 → 2026-04-17).

[Unreleased]: https://github.com/VladoPortos/omnirepo/compare/v1.8...HEAD
[v1.8]: https://github.com/VladoPortos/omnirepo/releases/tag/v1.8
[v1.6]: https://github.com/VladoPortos/omnirepo/releases/tag/v1.6
[v1.5]: https://github.com/VladoPortos/omnirepo/releases/tag/v1.5
[v1.4]: https://github.com/VladoPortos/omnirepo/releases/tag/v1.4
[v1.3]: https://github.com/VladoPortos/omnirepo/releases/tag/v1.3
[v1.2]: https://github.com/VladoPortos/omnirepo/releases/tag/v1.2
[v1.0]: https://github.com/VladoPortos/omnirepo/releases/tag/v1.0
