# Requirements — Milestone v1.1 "Immediate Product Polish"

**Scope source:** `improvements.md` (repo root) — "Phase 1: Immediate
Product Polish" + tightly-related UI sections.
**Defined:** 2026-04-17
**Coverage target:** 100% of listed requirements mapped to a phase in
`ROADMAP.md` before execution begins.

Each requirement is user-centric ("User can…") or system-observable
("API returns…"), atomic, and independently testable. REQ-IDs use v1.1-
specific category prefixes that do not collide with v1.0's archived
prefixes (AUTH, REPO, etc.).

---

## Active Requirements (v1.1)

### SNIPPET — Client configuration snippets

- [x] **SNIPPET-01**: User can copy a Docker `login` / `pull` / `push` snippet pre-filled with this OCI repo's URL from the repo page
- [x] **SNIPPET-02**: User can copy a `pip install` + `.pypirc` block pre-filled with this PyPI repo's URL
- [x] **SNIPPET-03**: User can copy an APT `sources.list` entry pre-filled with this APT repo's URL, suite, component, and signing-key URL
- [x] **SNIPPET-04**: User can copy an RPM `.repo` config pre-filled with this RPM repo's baseurl and GPG key URL
- [x] **SNIPPET-05**: User can copy Helm `repo add` / push / pull commands pre-filled with this Helm repo's URL
- [x] **SNIPPET-06**: User can copy a Git `clone` / fetch URL for this Git repo (HTTPS form, includes auth hint)
- [x] **SNIPPET-07**: User can copy an `aws configure` + AWS CLI + SDK code snippet for this S3 bucket (endpoint URL, region, bucket name; access-key reminder)
- [x] **SNIPPET-08**: User can copy a `curl -u user:key -T file URL` snippet for a RAW repo
- [x] **SNIPPET-09**: Every snippet supports one-click copy-to-clipboard with visible confirmation feedback

### EMPTY — Context-aware empty states

- [x] **EMPTY-01**: When a project has zero repos, the project page shows a guided "Create first repo" empty state with a CTA
- [x] **EMPTY-02**: When a project has zero members beyond the creator, the members page shows an "Add your first teammate" empty state with a CTA
- [x] **EMPTY-03**: When a repo has zero artifacts, the repo page shows an "Upload your first artifact" empty state that surfaces the relevant client snippet inline
- [x] **EMPTY-04**: When scanning has never run on a repo that supports scanning, the scan surface shows an "Enable / run first scan" empty state with explanation
- [x] **EMPTY-05**: When no admin-uploaded TLS certificate exists (default self-signed), the TLS admin page shows a "Configure TLS" empty state with an upload CTA
- [x] **EMPTY-06**: When no trash items exist, the trash page shows an explanatory empty state instead of a blank table
- [x] **EMPTY-08**: When search returns no results, the UI shows "try a different term" guidance with example queries

### ERR — Better failure messaging

- [x] **ERR-01**: REST API errors return a stable envelope `{ code, message, hint?, class, incident_id? }` where `class ∈ {validation, permission, transient, operator_action_required}`
- [x] **ERR-02**: Web UI renders API errors using the envelope — class-appropriate icon, human-friendly message, and hint when present
- [x] **ERR-03**: Internal error strings (filesystem paths, driver messages, stack traces) are never returned verbatim to clients; server logs retain the internal detail keyed by request ID
- [x] **ERR-04**: Transient errors show a "retry" affordance in the UI
- [x] **ERR-05**: Operator-action-required errors direct the user to the specific admin page or action (e.g. "Trivy DB missing → go to Admin → Trivy")
- [x] **ERR-06**: Validation errors highlight the offending field(s) where the UI has field context (forms, edit modals)
- [x] **ERR-07**: Errors recorded in the audit log receive an `incident_id` that correlates the UI message, server log line, and audit entry

### VISUAL — Visual language and polish

- [x] **VISUAL-01**: All status indicators across the UI use a consistent color palette mapping to a named state set: healthy / warning / failure / disabled / maintenance
- [x] **VISUAL-02**: Status badges are consistent in shape, size, and wording across surfaces (a badge for "stale" looks the same on dashboard, repo overview, and health page)
- [x] **VISUAL-03**: Loading states use skeleton placeholders in known-shape surfaces (tables, cards, detail panels) rather than blank regions or spinners
- [x] **VISUAL-04**: Copy-to-clipboard affordances exist on URLs, commands, digests, and shown-once API keys
- [x] **VISUAL-05**: Primary and destructive buttons are visually distinct — destructive actions are clearly marked and never share identical weight with primary actions
- [x] **VISUAL-06**: UI renders cleanly at laptop resolutions (1366×768 and 1440×900) without horizontal scroll on admin pages
- [x] **VISUAL-07**: Spacing and typography hierarchy on admin pages follow a consistent scale across headings, body, and metadata
- [x] **VISUAL-08**: Text colors and status colors meet WCAG AA contrast on the default theme
- [x] **VISUAL-09**: Severity treatment for scan findings is consistent across scan summary, detail page, and drawer views

### MIRROR — Upstream mirror & Docker clone (Phase 8)

- [ ] **MIRROR-01**: User can mark an APT/RPM/PyPI/Helm repo as a mirror of an upstream at creation time (is_mirror flag + upstream URL + filter + cred + scan_on_sync)
- [ ] **MIRROR-02**: System rejects edits that try to change is_mirror or mirror_upstream_url on an existing repo (400 code=mirror_url_immutable)
- [ ] **MIRROR-03**: System rejects mirror repo upload attempts on all 5 protocols (OCI, APT, RPM, PyPI, Helm) with 403 code=repo_is_mirror
- [ ] **MIRROR-04**: System enforces one-in-flight-sync-per-repo with 409 code=sync_already_running
- [ ] **MIRROR-05**: System rejects bodies on /sync when the target repo is a mirror (400 code=mirror_overrides_not_allowed)
- [ ] **MIRROR-06**: System bounds /sync request bodies at 16 KiB and rejects oversized/malformed payloads with 400 code=invalid_request_body
- [ ] **MIRROR-07**: System validates mirror_cred_id belongs to the same project as the repo (400 code=mirror_cred_wrong_project)
- [x] **MIRROR-08**: API GET /jobs/{id} returns progress_bytes, total_bytes, and current_step alongside status
- [x] **MIRROR-09**: System throttles sync-progress DB writes to at most one per 200 ms per job with change detection
- [x] **MIRROR-10**: System emits byte-level progress for OCI pull-external (layer N of M · done / total bytes)
- [x] **MIRROR-11**: System emits byte-level progress for APT / RPM / PyPI sync (step = "pulling {artifact}" · done / total bytes)
- [x] **MIRROR-12**: System emits step-based progress for Helm sync (chart N of M · total_bytes = 0 because index.yaml lacks sizes)
- [x] **MIRROR-13**: User can pull an external Docker image into an OmniRepo docker repo via a UI modal with live byte-level progress
- [x] **MIRROR-14**: UI Docker clone modal renders three phases (form → progress → result) and surfaces failure via ErrorEnvelopeRenderer with retry
- [x] **MIRROR-15**: UI polls the per-repo sync-job endpoint every 500 ms while a job is active and stops polling on done/failed
- [x] **MIRROR-16**: User can create a mirror APT/RPM/PyPI/Helm repo from the UI CreateRepoDialog (upstream URL, filter widget, cred picker, scan-on-sync toggle)
- [x] **MIRROR-17**: UI shows protocol-specific filter widgets (APT Suites/Components/Arches/Names/Globs; RPM Names; PyPI Names; Helm Names/Globs) using the PascalCase wire format that matches Go's default JSON encoding of SyncFilter
- [x] **MIRROR-18**: User can click "Sync now" on a mirror repo page to trigger POST /sync with live progress bar
- [x] **MIRROR-19**: UI disables the Sync now button while a sync is in-flight and surfaces 409 errors inline via ErrorEnvelopeRenderer
- [x] **MIRROR-20**: User can edit filter, cred, and scan_on_sync on a mirror repo from RepoSettingsTab (URL stays read-only with CopyInline)
- [x] **MIRROR-21**: UI never sends is_mirror or mirror_upstream_url in PATCH bodies (structural enforcement + backend guard)
- [ ] **MIRROR-22**: User can create, edit, and delete upstream credentials from a ProjectSettingsPage Upstream credentials tab
- [ ] **MIRROR-23**: UI never displays passwords or tokens after submission; PATCH preserves existing secrets when fields are left blank
- [ ] **MIRROR-24**: Deleting a credential referenced by a mirror repo triggers a confirmation dialog that warns the next sync will fail with "credential missing" envelope
- [ ] **MIRROR-25**: Each of the 5 protocols has a fake-upstream integration test proving first-sync ingest + idempotent second-sync + progress completion
- [ ] **MIRROR-26**: Playwright spec proves the mirror-upload-rejected envelope renders correctly for at least one protocol (APT via real PUT /{project}/deb/{repo}/pool/* route)
- [ ] **MIRROR-27**: Phase 8 passes a Codex rescue pass with real-issue findings applied and the result recorded in 08-06-CODEX-RESCUE.md

---

## Deferred to v1.2 (dropped from v1.1 on 2026-04-17)

On 2026-04-17 the user decided v1.1 should ship as a tight polish milestone
after Phase 7. Phases 8 (FAV), 9 (HEALTH), and 10 (OVERVIEW) were removed
from v1.1's active scope and deferred to v1.2. The REQ wording below is
preserved verbatim so the v1.2 planner can pick them up as-is (or refine).
These 24 REQs stay in the traceability table below under the "Deferred to
v1.2" column, not as v1.1 coverage.

### HEALTH — Health / status dashboard (deferred to v1.2)

- [ ] **HEALTH-01**: User (admin) can open a dedicated "Health" / "System Status" page in the UI
- [ ] **HEALTH-02**: Health page shows disk usage (used / free / total) for the data volume, with a visual warning band at a configurable threshold
- [ ] **HEALTH-03**: Health page shows the SQLite DB size plus growth trend over the last 7 and 30 days
- [ ] **HEALTH-04**: Health page shows current background-job status — running / queued / failed counts and a link to recent runs
- [ ] **HEALTH-05**: Health page shows Trivy DB freshness (last update timestamp, age, stale/fresh indicator)
- [ ] **HEALTH-06**: Health page shows TLS certificate expiry summary (days remaining; warning under configurable threshold)
- [ ] **HEALTH-07**: Health page shows long-running task history — last N (default 20) tasks with duration and, if failed, failure reason
- [ ] **HEALTH-08**: User can manually refresh the health page (one-click) and the page optionally auto-refreshes on a visible interval
- [ ] **HEALTH-09**: REST endpoints expose each health metric as JSON under `/api/v1/admin/health/*` (disk, db, jobs, trivy, tls, tasks, summary)

### FAV — Saved filters, favorites, and recents (deferred to v1.2)

- [ ] **FAV-01**: User can save a named filter on any table that supports filtering (projects, repos, artifacts, audit log, search results)
- [ ] **FAV-02**: User can pin a project as a favorite, surfaced in the top nav or sidebar for that user
- [ ] **FAV-03**: User can pin a repo as a favorite, surfaced alongside favorite projects
- [ ] **FAV-04**: System tracks and displays each user's last N visited projects and repos ("Recently visited")
- [ ] **FAV-05**: Saved filters and favorites persist per-user across sessions — stored server-side, survive browser-data reset
- [ ] **FAV-06**: User can rename and delete their saved filters
- [ ] **FAV-07**: User can reorder their favorites (drag-and-drop or explicit up/down controls)

### EMPTY — Context-aware empty states (deferred to v1.2)

- [ ] **EMPTY-07**: When no saved filters, favorites, or recents exist on a given surface, the UI shows guidance text rather than a silent empty section *(deferred — depends on FAV surfaces)*

### OVERVIEW — Better repository overview pages (deferred to v1.2)

- [ ] **OVERVIEW-01**: Every repo page has an "Overview" tab (default landing tab) that presents a control-center layout
- [ ] **OVERVIEW-02**: Overview shows the copyable client snippets for this repo type (reuses SNIPPET-\* implementations)
- [ ] **OVERVIEW-03**: Overview shows the latest artifacts (top 5–10) with timestamp and actor
- [ ] **OVERVIEW-04**: Overview shows recent uploads separately from "latest" — scoped by recency (last N hours / days)
- [ ] **OVERVIEW-05**: Overview shows sync status summary for repo types that support sync (PyPI / Helm / RPM / APT): last sync outcome, time, item counts
- [ ] **OVERVIEW-06**: Overview shows scan status summary — last scan run, severity counts, a "run scan" CTA if stale
- [ ] **OVERVIEW-07**: Overview shows visibility + policy placeholders — current `public_read` flag, a placeholder row for v2.0 immutability work
- [ ] **OVERVIEW-08**: Overview shows last-modified actors (recent audit entries filtered to this repo) with links into the full audit log

### Parked for v1.2 planning (not yet REQs)

- **Avatar style picker** — DiceBear already in deps (`@dicebear/core` + `@dicebear/collection` v9.2.2); current UI only uses `initials`. Swap to user-choosable style (avataaars / bottts / personas / lorelei / notionists / pixel-art). Needs picker in profile settings + seed column on users table + migration.
- **Tamagotchi ASCII pet** — fun/morale feature. See `ROADMAP.md` backlog entry 999.1.

---

## Future Requirements (deferred beyond v1.2)

From `improvements.md` "Phase 2: Operational Maturity":

- Retention and quota policies (per-project, per-repo, keep-last-N, age-based)
- Job-model standardization (formal `queued / running / retrying / failed / completed / cancelled` lifecycle; retry policy visibility; user-facing logs)
- Hygiene / compliance reporting (public repos, stale repos, expiring certs, stale API keys, failing syncs, out-of-date Trivy DB, un-scanned artifacts)
- Notification hooks (email or webhook on failed syncs / scans / expiring certs / quota thresholds / maintenance mode / destructive admin actions)
- Safer destructive workflows (dry-run, impact previews, soft locks, optional approval flow, reason-for-change fields)
- Better backup and recovery UX (status page, restore guidance, config export summary, "last successful backup" display)

From `improvements.md` "Phase 3: Enterprise Differentiation":

- Promotion / release pipeline model (named lifecycle stages, allowed paths, required comments, promotion audit trail, artifact lineage)
- Immutable release controls (protected versions / tags, snapshot vs release repo distinction, legal-hold markers)
- Audit-trail enrichment (before / after values for settings changes, correlation API ↔ job ↔ result, rich filtering, exportable reports, user vs system action distinction)
- Air-gapped export / import bundles (signed transfer manifests, offline promotion packages, integrity verification)
- Policy engine (require scan pass before promotion, block public read on specific project classes, enforce naming conventions, require immutable mode on release repos)

From `improvements.md` "Phase 4: Strategic Platform Evolution":

- HA direction (metadata/storage plane split, HA-safe jobs, shared-storage posture)
- Provenance / SBOM / attestation UX (SBOM attachment, attestation visibility, build-source linkage, dependency-graph views)
- Bulk administration (bulk repo edits, bulk project membership, bulk API-key review/revoke, bulk retention changes, bulk artifact cleanup)

---

## Out of Scope

Explicitly excluded from v1.1. Reasons inherited from v1.0's original
Out of Scope list plus the user's v1.1 direction ("no real reworks of
core functionality, just UI polish and user-friendly stuff, plus
endpoints to support the dashboard").

- **Core protocol reworks** — OCI, RPM, APT, PyPI, Helm, RAW, S3 SigV4, Git: no changes to the protocol layer in v1.1.
- **Authentication model changes** — no LDAP / OIDC / SSO, no TOTP 2FA, no scoped personal tokens, no self-registration.
- **Storage backend changes** — local filesystem only; no S3-as-backend or cloud backends.
- **Multi-tenant database or multi-process backend** — single-binary stays.
- **New outbound network calls at runtime** — air-gap invariant preserved; Trivy DB still uploaded or pulled only on explicit admin action.
- **Login rate limiting / lockout** — deferred.
- **Artifact signing (verification only)** — deferred.
- **Cross-instance replication** — deferred.
- **Stack upgrades** — Go / React / Vite / Tailwind versions frozen at the v1.0 baseline for v1.1.

---

## Traceability

Every active v1.1 REQ-ID is mapped to exactly one phase in `ROADMAP.md`.
Deferred REQs carry a "v1.2" target and will re-map to concrete phases when
the v1.2 ROADMAP is created. Phases numbered 6–7 continue from v1.0's
phases 1–5.

### Active v1.1 (59 REQs)

| REQ-ID | Phase | Status |
|--------|-------|--------|
| ERR-01 | Phase 6 | Complete |
| ERR-02 | Phase 6 | Complete |
| ERR-03 | Phase 6 | Complete |
| ERR-04 | Phase 6 | Complete |
| ERR-05 | Phase 6 | Complete |
| ERR-06 | Phase 6 | Complete |
| ERR-07 | Phase 6 | Complete |
| VISUAL-01 | Phase 6 | Complete |
| VISUAL-02 | Phase 6 | Complete |
| VISUAL-03 | Phase 6 | Complete |
| VISUAL-04 | Phase 6 | Complete |
| VISUAL-05 | Phase 6 | Complete |
| VISUAL-06 | Phase 6 | Complete |
| VISUAL-07 | Phase 6 | Complete |
| VISUAL-08 | Phase 6 | Complete |
| VISUAL-09 | Phase 6 | Complete |
| SNIPPET-01 | Phase 7 | Complete |
| SNIPPET-02 | Phase 7 | Complete |
| SNIPPET-03 | Phase 7 | Complete |
| SNIPPET-04 | Phase 7 | Complete |
| SNIPPET-05 | Phase 7 | Complete |
| SNIPPET-06 | Phase 7 | Complete |
| SNIPPET-07 | Phase 7 | Complete |
| SNIPPET-08 | Phase 7 | Complete |
| SNIPPET-09 | Phase 7 | Complete |
| EMPTY-01 | Phase 7 | Complete |
| EMPTY-02 | Phase 7 | Complete |
| EMPTY-03 | Phase 7 | Complete |
| EMPTY-04 | Phase 7 | Complete |
| EMPTY-05 | Phase 7 | Complete |
| EMPTY-06 | Phase 7 | Complete |
| EMPTY-08 | Phase 7 | Complete |
| MIRROR-01 | Phase 8 | In progress |
| MIRROR-02 | Phase 8 | In progress |
| MIRROR-03 | Phase 8 | In progress |
| MIRROR-04 | Phase 8 | In progress |
| MIRROR-05 | Phase 8 | In progress |
| MIRROR-06 | Phase 8 | In progress |
| MIRROR-07 | Phase 8 | In progress |
| MIRROR-08 | Phase 8 Plan 02 | Delivered |
| MIRROR-09 | Phase 8 Plan 02 | Delivered |
| MIRROR-10 | Phase 8 Plan 02 | Delivered |
| MIRROR-11 | Phase 8 Plan 02 | Delivered |
| MIRROR-12 | Phase 8 Plan 02 | Delivered |
| MIRROR-13 | Phase 8 | Shipped (2026-04-20 · plan 08-03) |
| MIRROR-14 | Phase 8 | Shipped (2026-04-20 · plan 08-03) |
| MIRROR-15 | Phase 8 | Shipped (2026-04-20 · plan 08-03) |
| MIRROR-16 | Phase 8 | ✅ Complete |
| MIRROR-17 | Phase 8 | ✅ Complete |
| MIRROR-18 | Phase 8 | ✅ Complete |
| MIRROR-19 | Phase 8 | ✅ Complete |
| MIRROR-20 | Phase 8 | ✅ Complete |
| MIRROR-21 | Phase 8 | ✅ Complete |
| MIRROR-22 | Phase 8 | In progress |
| MIRROR-23 | Phase 8 | In progress |
| MIRROR-24 | Phase 8 | In progress |
| MIRROR-25 | Phase 8 | In progress |
| MIRROR-26 | Phase 8 | In progress |
| MIRROR-27 | Phase 8 | In progress |

### Deferred to v1.2 (25 REQs — re-map at v1.2 planning)

| REQ-ID | Target | Status |
|--------|--------|--------|
| HEALTH-01 | v1.2 | Deferred |
| HEALTH-02 | v1.2 | Deferred |
| HEALTH-03 | v1.2 | Deferred |
| HEALTH-04 | v1.2 | Deferred |
| HEALTH-05 | v1.2 | Deferred |
| HEALTH-06 | v1.2 | Deferred |
| HEALTH-07 | v1.2 | Deferred |
| HEALTH-08 | v1.2 | Deferred |
| HEALTH-09 | v1.2 | Deferred |
| FAV-01 | v1.2 | Deferred |
| FAV-02 | v1.2 | Deferred |
| FAV-03 | v1.2 | Deferred |
| FAV-04 | v1.2 | Deferred |
| FAV-05 | v1.2 | Deferred |
| FAV-06 | v1.2 | Deferred |
| FAV-07 | v1.2 | Deferred |
| EMPTY-07 | v1.2 | Deferred |
| OVERVIEW-01 | v1.2 | Deferred |
| OVERVIEW-02 | v1.2 | Deferred |
| OVERVIEW-03 | v1.2 | Deferred |
| OVERVIEW-04 | v1.2 | Deferred |
| OVERVIEW-05 | v1.2 | Deferred |
| OVERVIEW-06 | v1.2 | Deferred |
| OVERVIEW-07 | v1.2 | Deferred |
| OVERVIEW-08 | v1.2 | Deferred |

**Coverage:** 59/59 active v1.1 REQ-IDs mapped to exactly one v1.1 phase
(32 shipped in Phases 6–7 + 27 MIRROR IDs being delivered across Phase 8
plans 08-01..08-06); 25 REQ-IDs deferred to v1.2 with phase mapping TBD.

---

## Quality Criteria

Good v1.1 requirements are:

- **Specific and testable** — "User can copy pip-install snippet from PyPI repo page" (not "improve PyPI UX")
- **User-centric or system-observable** — "User can…" / "API returns…" / "UI renders…"
- **Atomic** — one capability per requirement (no "…and also…")
- **Independent** — minimal coupling across requirements so phases can parallelize where safe

Requirements that cannot be tested end-to-end (pure visual polish) are
satisfied by a Playwright screenshot walkthrough plus explicit manual
UAT on the targeted laptop resolutions.
