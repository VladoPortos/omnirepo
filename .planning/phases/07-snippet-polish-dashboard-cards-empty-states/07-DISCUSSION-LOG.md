# Phase 7: Snippet Polish, Dashboard Cards & Empty States - Discussion Log

> **Audit trail only.** Do not use as input to planning, research, or execution agents.
> Decisions are captured in `07-CONTEXT.md` — this log preserves the alternatives considered.

**Date:** 2026-04-17
**Phase:** 07-snippet-polish-dashboard-cards-empty-states
**Areas discussed:** Snippet placeholder conventions, Dashboard 4th card (jobs alternative), EmptyState component API + layout, Walkthrough micro-fixes to fold in

---

## Snippet placeholder conventions

### Q1 — APT: signed-by convention

| Option | Description | Selected |
|--------|-------------|----------|
| /etc/apt/keyrings/ + signed-by= | Modern Debian 12+/Ubuntu 22.04+ convention. Repo-scoped key trust. | |
| /etc/apt/trusted.gpg.d/*.asc | Legacy/simpler. Globally trusts the key. Deprecated in Debian 12+. | |
| Both — show two variants in the snippet | Double the snippet length; covers both modern and older hosts. | ✓ |

**User's choice:** Both variants

---

### Q2 — APT suite/component

| Option | Description | Selected |
|--------|-------------|----------|
| `<suite> <component>` | Pure placeholders. Matches ROADMAP wording. | |
| `stable main` literal | Current v1.0 behavior. Copy-paste-and-run. | ✓ |
| `$SUITE $COMPONENT` | Shell-var style. Atypical for apt sources.list. | |

**User's choice:** `stable main` literal
**Notes:** Deviates from ROADMAP success criterion 1 phrasing; acceptance test needs adjustment.

---

### Q3 — Helm push mechanism (initial)

| Option | Description | Selected |
|--------|-------------|----------|
| chartmuseum/helm-push plugin | Canonical plugin for non-OCI repos. | |
| OCI-mode `helm push` (native) | Needs OCI support on the server. | ✓ |
| curl PUT fallback | Works anywhere; least idiomatic. | |

**User's choice:** OCI mode, with a demand to verify + fix anything missing
**Notes:** Triggered a sub-investigation. User: "OCI mode and we need to support OCI mode, so if we don't, we need to implement it."

---

### Q3b — Helm OCI↔traditional mirror scope

| Option | Description | Selected |
|--------|-------------|----------|
| Forward mirror only (OCI → traditional) | On OCI push, mirror to traditional layout + regen index.yaml. | ✓ |
| Bidirectional | Both directions. Adds reverse mirror (traditional PUT → synthesize OCI manifest). | |
| Forward + document asymmetry (recommended) | Same as A but call out the asymmetry explicitly in docs. | |
| Abort — use cm-push plugin | Walk back the OCI ambition. | |

**User's choice:** Forward mirror only
**Notes:** Reverse direction deferred to v1.2. Confirmed the manifestMediaType() function at manifests.go:111 already honors client Content-Type, so the mediaType-whitelist fix originally suspected is not needed.

---

### Q4 — S3 region + access-key surfacing

| Option | Description | Selected |
|--------|-------------|----------|
| Explicit `<region>` placeholder + access-key comment | Surface both requirements. | ✓ |
| Default `us-east-1` + no access-key comment | Minimal friction; silently binds region. | |
| Full aws.config block | Full `~/.aws/config` stanza. More thorough, longer snippet. | |

**User's choice:** Explicit `<region>` placeholder + access-key comment

---

### Q5 — Git snippet auth-hint

| Option | Description | Selected |
|--------|-------------|----------|
| Leading comment + clone line | One snippet, explanatory comment. | |
| Two labeled snippets: 'Clone' + 'Authenticate' | Two blocks; authenticate shows credential.helper config. | ✓ |
| URL with userinfo | Inline credentials. Discouraged for credential leakage. | |

**User's choice:** Two labeled snippets

---

### Q6 — RAW auth-hint + download treatment

| Option | Description | Selected |
|--------|-------------|----------|
| Upload + download both use `-u`, with placeholder hint | Symmetric, works for public + private. | ✓ |
| Upload `-u`; download anonymous | Shorter but fails on private repos. | |
| Upload `-u`; separate 'Authenticated download' option | Three lines; most thorough. | |

**User's choice:** Both use `-u` with placeholder hint

---

### Q7 — PyPI `.pypirc` block

| Option | Description | Selected |
|--------|-------------|----------|
| Add `.pypirc` block alongside existing pip/twine | Satisfies REQ-02 verbatim. | ✓ |
| Keep as-is — skip `.pypirc` | Current behavior; misses REQ wording. | |
| Replace twine with `.pypirc` + twine pointer | Shorter; removes `--repository-url` duplication. | |

**User's choice:** Add `.pypirc` block alongside existing

---

### Q8 — Docker snippet

| Option | Description | Selected |
|--------|-------------|----------|
| Keep as-is — login/pull/push with placeholders | No known gaps. | ✓ |
| Add `docker buildx build --push` variant | Multi-arch support, not in REQ. | |
| Show bare `<image>` without path prefix | Conflicts with OCI URL scheme. | |

**User's choice:** Keep as-is

---

### Q9 — RPM snippet

| Option | Description | Selected |
|--------|-------------|----------|
| Keep as-is — dnf .repo stanza with gpgcheck=1 | No known gaps. | ✓ |
| Add `enabled=1` + `module_hotfixes=true` | Nice-to-have; noise for common case. | |
| Add `yum-config-manager --add-repo` shortcut | Shorter; duplicates content. | |

**User's choice:** Keep as-is

---

### Q10 — SNIPPET-09 copy-with-confirmation

| Option | Description | Selected |
|--------|-------------|----------|
| Phase 6 CopyButton satisfies SNIPPET-09 — add assertion test | Reuse CopyButton aria-live; new Playwright spec. | ✓ |
| Add a visible toast or pulse on copy | Supplement with visual animation; may conflict with reduced-motion. | |
| Leave as-is, no new test | Risk of drift. | |

**User's choice:** Phase 6 CopyButton + new Playwright spec

---

### Q11 — SnippetPanel container UX

| Option | Description | Selected |
|--------|-------------|----------|
| Keep Sheet — no container change | Current shadcn Sheet (slide-out right); no known issues. | ✓ |
| Swap to inline expandable card on repo pages | Larger UX change. | |
| Keep Sheet + render inline on EMPTY-03 | Dual usage; EMPTY-03 uses inline variant. | |

**User's choice:** Keep Sheet
**Notes:** EMPTY-03's inline-snippet treatment is handled via the EmptyState children slot (E-03), separate from the Sheet container decision.

---

## Dashboard 4th card (jobs alternative)

### Q1 — Jobs card approach

| Option | Description | Selected |
|--------|-------------|----------|
| Derive 'Recent Activity' card from dashboard.recent_activity | Reuse existing data; zero new endpoints. | |
| GC Status card (super-admin only) | Inert for non-admins. | |
| Expose background-jobs count via a NEW thin endpoint | Violates ROADMAP SC #2 'zero new endpoints'. | ✓ |
| Drop to 3 cards + promote Scan Findings | Violates ROADMAP SC #2 'at least 4 cards'. | |

**User's choice:** New thin endpoint
**Notes:** Escalated to full scope relaxation — see Q3.

---

### Q2 — Storage growth card framing

| Option | Description | Selected |
|--------|-------------|----------|
| Current used / total + usage % + top-3 repos | No 'growth'; compact status. | ✓ |
| Growth inferred from audit log push-events | Softer signal. | |
| Client-side remembered last-seen bytes in localStorage | Breaks on multi-device/incognito. | |
| Add server-side daily storage snapshots | Scope expansion; v1.2 work. | |

**User's choice:** Current used/total + top-3 repos

---

### Q3 — ROADMAP SC #2 update

| Option | Description | Selected |
|--------|-------------|----------|
| Allow one new endpoint: /admin/jobs/summary only | Preserve intent, explicit exception. | |
| Rewrite SC #2 to drop the 'all endpoints in v1.0' clause entirely | Any new admin endpoint in scope. | ✓ |
| Keep SC #2 as-is and skip the new endpoint | Walks back the scope ask. | |

**User's choice:** Full relaxation
**Notes:** "You are free to add any endpoints for this Summa review: so admin and users have as much information as possible at first glance. We don't have to force no new endpoints policy because most of these endpoints will be just reading information from database, not writing anything. So it shouldn't break anything further down. So I'm open to adding a new endpoints as necessary for better UI experience for that dashboard."

---

### Q4 — Failures card source

| Option | Description | Selected |
|--------|-------------|----------|
| Audit events ending in `*.failed` / `*.error` | Uses existing recent_activity; per-actor scoped. | ✓ |
| HTTP 5xx incident_id occurrences from audit log | Needs audit tagging for 5xx envelopes. | |
| Scan-findings growth (new criticals this week) | Needs `first_seen_at` migration. | |

**User's choice:** Audit events ending `.failed` / `.error`

---

### Q5 — Certs + Trivy card visibility

| Option | Description | Selected |
|--------|-------------|----------|
| Admin-only, thresholds 30d/14d + 7d/30d, config-overridable | Sensible defaults + override path. | |
| Admin-only, hard-coded thresholds | Simpler; revisit if complaints. | |
| Visible to all, redacted for non-admins | 95% of users see useless content. | |
| Two separate cards (certs alone, Trivy alone) | Granular; 5+ total cards. | ✓ |

**User's choice:** Two separate cards

---

### Q6 — Layout + thresholds + trend source + card inventory (batched)

Q6a. Card placement — New 'Composition' row 2 above existing Storage section ✓
Q6b. Thresholds — Hard-coded defaults; editable via existing settings table ✓
Q6c. Scan-findings trend — Count `scan.*` audit events last 7d vs 7-14d ✓
Q6d. Final inventory (multiSelect) — All 4 items selected: Storage Status + Recent Failures + Scan Findings Trend + Jobs Summary/Cert Expiry/Trivy DB Freshness ✓

---

### Q7 — Jobs endpoint design

| Option | Description | Selected |
|--------|-------------|----------|
| `GET /api/v1/admin/jobs/summary` | Thin summary shape; super-admin gate. | ✓ |
| `GET /api/v1/admin/jobs` with filters | More ambitious; paginated. | |
| Embed summary inside existing /api/v1/dashboard response | Zero new routes; couples surfaces. | |

**User's choice:** Dedicated `/admin/jobs/summary` endpoint

---

## EmptyState component API + layout

### Q1 — Component API shape

| Option | Description | Selected |
|--------|-------------|----------|
| icon + title + description + primaryCTA | Matches REQ 'headline + single CTA'. | ✓ |
| icon + title + description + primaryCTA + secondaryCTA | Weakens 'single CTA selector' invariant. | |
| icon + title + description + children (slot) | Max flexibility; loses selector invariant. | |
| Compound-component pattern | Idiomatic but harder to Playwright-select. | |

**User's choice:** icon + title + description + primaryCTA
**Notes:** `children` slot added via E-03 decision for EMPTY-03 inline-snippet variant only.

---

### Q2 — Layout

| Option | Description | Selected |
|--------|-------------|----------|
| Borderless, centered, muted-foreground | Callers wrap in Card if needed. | ✓ |
| Card-wrapped (always) | Enforces consistency; double-wraps inside CardContent sites. | |
| Both via `variant` prop | More API surface. | |

**User's choice:** Borderless

---

### Q3 — EMPTY-03 zero-artifacts inline snippet

| Option | Description | Selected |
|--------|-------------|----------|
| Embed SnippetPanel body via children slot | Full per-protocol snippet list inline. | ✓ |
| Render a single CTA that opens the Sheet | Simpler but REQ says 'inlines SNIPPET'. | |
| Inline the first snippet only | Scannable; loses completeness. | |

**User's choice:** Embed body via children slot
**Notes:** Requires factoring SnippetPanel body into a shared `SnippetList` sub-component.

---

### Q4 — EMPTY-07 disposition

| Option | Description | Selected |
|--------|-------------|----------|
| Skip EMPTY-07 in Phase 7; mark deferred to v1.2 | Aligns with FAV deferral. | ✓ |
| Implement 'filter cleared' state on DataTables | Ad-hoc interpretation. | |
| Defer decision to plan review | Push decision downstream. | |

**User's choice:** Defer to v1.2
**Notes:** REQUIREMENTS.md needs EMPTY-07 moved to "Deferred to v1.2" during Phase 7 planning.

---

### Q5 — EMPTY-04 scan-trigger CTA

| Option | Description | Selected |
|--------|-------------|----------|
| Trigger a scan directly on the repo | Inline envelope feedback; permission-gated. | ✓ |
| Link to admin/trivy page | Wrong target for per-repo case. | |
| Explanatory text only, no CTA | If scans are event-driven only. | |

**User's choice:** Trigger scan directly

---

### Q6 — DashboardPage ad-hoc strings migration

| Option | Description | Selected |
|--------|-------------|----------|
| Migrate all 3 | 'Looking good!' becomes StatusBadge healthy. | ✓ |
| Migrate only the neutral two; keep 'Looking good!' | Preserve positive framing differently. | |
| Leave all 3 alone; EmptyState for new sites only | Less consistency. | |

**User's choice:** Migrate all 3

---

### Q7 — EMPTY-08 example queries

| Option | Description | Selected |
|--------|-------------|----------|
| Static 3-example list | Hardcoded clickable chips. | ✓ |
| Dynamic: top-3 recent repos from user's projects | Personalized but extra query. | |
| No examples, just 'Try a different search' + clear-filters CTA | Minimal. | |

**User's choice:** Static 3-example list (openssl / CVE-2024- / myorg/docker/alpine)

---

### Q8 — Playwright selector strategy

| Option | Description | Selected |
|--------|-------------|----------|
| `data-testid='empty-state'` + role='status' + aria-label | One helper for every spec. | ✓ |
| Semantic only: getByRole('heading', { name: title }) | More brittle to copy changes. | |
| Per-surface unique testid | More typing, more drift. | |

**User's choice:** Generic testid + role='status'

---

## Walkthrough micro-fixes to fold in

### Q1 — Which items from known sources?

| Option | Description | Selected |
|--------|-------------|----------|
| Codex rescue pass across S3 + admin batch | NEXT-SESSION-ISSUES.md item. | ✓ |
| Docker storage overestimate — ref-count shared blobs | NEXT-SESSION-ISSUES.md item; ~80 LOC. | ✓ |
| DEB pool-path reconstruction — read Release file | NEXT-SESSION-ISSUES.md item; correctness fix. | ✓ |
| None of the above — freeform | | |

**User's choice:** All three NEXT-SESSION-ISSUES items

---

### Q2 — Anything else?

| Option | Description | Selected |
|--------|-------------|----------|
| Close area — those 3 items are the list | Lock and move to CONTEXT.md. | ✓ |
| Name more items in freeform | Add your own list. | |
| Re-run walkthrough first before locking | Time trade-off. | |

**User's choice:** Close area

---

## Claude's Discretion

- Card-ordering within the Composition row (Storage / Failures / Scan Trend left-to-right)
- Exact SQL shape for the audit-events-for-failures query
- Which Git auth form (credential.helper vs http.extraHeader) ships in S-05
- Refactor shape for the SnippetPanel → SnippetList extraction
- Aggregated vs per-threshold settings keys for D-02

## Deferred Ideas

- Bidirectional Helm OCI↔traditional symmetry (reverse direction) — v1.2
- Server-side daily storage snapshots for real growth tracking — v1.2
- `first_seen_at` column on vulnerabilities for accurate new-CVEs-this-week — v1.2
- Dedicated Jobs page — v1.2 alongside HEALTH
- DEB fully-general pool-path solution — v2.0 (W-03 only covers Release-driven cases)
- Docker billing-grade per-repo byte attribution — v2.0 (W-02 ref-counts but doesn't split)
