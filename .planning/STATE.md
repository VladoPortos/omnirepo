---
gsd_state_version: 1.0
milestone: v1.1
milestone_name: immediate-product-polish
status: Roadmap approved — awaiting phase 6 discuss/plan
last_updated: "2026-04-17T09:30:00Z"
progress:
  total_phases: 5
  completed_phases: 0
  total_plans: 0
  completed_plans: 0
  percent: 0
previous_milestone:
  version: v1.0
  shipped_at: "2026-04-17T03:40:00Z"
  git_tag: v1.0
  archived_artifacts:
    roadmap: milestones/v1.0-ROADMAP.md
    requirements: milestones/v1.0-REQUIREMENTS.md
    audit: v1.0-MILESTONE-AUDIT.md
---

# STATE: OmniRepo

**Last updated:** 2026-04-17

## Project Reference

- **Core value**: A single container that hosts every artifact type a corporate team produces or consumes — Docker images, Linux packages, Python wheels, Helm charts, raw blobs, S3 objects, Git repos — with vulnerability scanning, project-scoped access control, and zero outbound network calls at runtime.
- **Current focus**: v1.1 "Immediate Product Polish" — UI/UX quality-of-life pass (client snippets, empty states, health dashboard, failure messaging, saved filters, repo overview pages, visual-language polish). No core protocol reworks; additive backend endpoints only where needed to power the new UI.
- **Granularity**: coarse (5 phases, numbered 6 through 10, continuing from v1.0's last phase 5)

## Current Position

Phase: 6 — Error Envelope & Visual Foundation
Plan: —
Status: Not started (awaiting discuss/plan)
Last activity: 2026-04-17 — v1.1 roadmap approved, 57/57 requirements mapped to phases 6–10

## Phase Map

| Phase | Name | Requirements | Depends on |
|-------|------|--------------|------------|
| 6 | Error Envelope & Visual Foundation | 16 (ERR-01..07, VISUAL-01..09) | Nothing beyond v1.0 |
| 7 | Client Snippets & Empty States | 17 (SNIPPET-01..09, EMPTY-01..08) | Phase 6 |
| 8 | Favorites, Saved Filters & Recents | 7 (FAV-01..07) | Phase 6 |
| 9 | Health & Status Dashboard | 9 (HEALTH-01..09) | Phase 6 |
| 10 | Repository Overview Pages | 8 (OVERVIEW-01..08) | Phases 6, 7, 9 |

## Scope reference

Scope is derived from `improvements.md` (in the repo root) — specifically
"Phase 1: Immediate Product Polish" plus the tightly-related UI sections.
Formal REQ-IDs live in `.planning/REQUIREMENTS.md`; phase breakdown in
`.planning/ROADMAP.md`.

**Explicitly deferred** to later milestones per `improvements.md`'s own
prioritization: retention/quotas, promotion/release pipeline, policy
engine, bulk administration, audit enrichment, notification hooks,
backup/recovery UX, air-gap export-import, HA, SBOM/provenance,
scoped tokens, LDAP/OIDC.

## Accumulated Context

### Decisions for v1.1

- **Phases continue numbering from v1.0** — v1.1 starts at Phase 6, not Phase 1. Preserves traceability across milestones in the same `.planning/` tree.
- **ERR envelope lands in Phase 6 as a foundation** — every SNIPPET/HEALTH/OVERVIEW surface renders its errors through the new envelope; putting ERR late would force rework across phases 7–10.
- **VISUAL is not a trailing-polish phase** — the design-system primitives (status tokens, skeletons, badges, copy-to-clipboard, button hierarchy) ship alongside ERR in Phase 6 so every later UI phase consumes shared components instead of re-implementing them.
- **FAV lives in its own phase (8)** — schema migration + server-side persistence + nav surfacing is independent enough to parallelize after Phase 6 lands, rather than bolting it onto an already-large foundation phase.
- **OVERVIEW (Phase 10) depends on SNIPPET (Phase 7) and HEALTH (Phase 9)** — OVERVIEW-02 reuses snippet components, and OVERVIEW's scan/sync summary cards share patterns with HEALTH cards. Scheduling it last avoids duplicate component drift.

### Decisions carried forward from v1.0

- Single Go binary, local filesystem only, zero outbound at runtime.
- `make grep-cdn` stays green — no runtime CDN.
- Stack frozen at Go 1.25 + React 19 + Vite + Tailwind 4 for v1.1.
- `oapi-codegen/v2` types-only generation; chi routes hand-written.
- modernc.org/sqlite + argon2id + Trivy-as-subprocess remain.
- All UI assets bundled via `//go:embed`.

### Todos

- Run `/gsd-plan-phase 6` to decompose Phase 6 into plans.

### Blockers

(none)

### Research Flags

- **v1.1 scope source is `improvements.md`** — research step skipped
  because the document is already a researched product-direction brief.
  If phase planning surfaces unknowns, `/gsd-research-phase` or the
  research step inside `/gsd-plan-phase` is still available.
- **Error envelope shape (Phase 6)** — worth a short spike inside plan-phase
  to confirm the envelope is compatible with existing OpenAPI 3.1 components
  and the oapi-codegen types pipeline.

## Session Continuity

- **Next action**: `/gsd-plan-phase 6` — decompose Phase 6 (Error Envelope & Visual Foundation) into plans, starting with the ERR envelope contract and the design-system token/component scaffolding.
- **Artifacts on disk**:
  - `.planning/PROJECT.md` (Current Milestone: v1.1)
  - `.planning/REQUIREMENTS.md` (57 v1.1 REQs, traceability populated)
  - `.planning/ROADMAP.md` (v1.1 roadmap: phases 6–10)
  - `.planning/STATE.md` (this file)
  - `.planning/milestones/v1.0-ROADMAP.md` + `v1.0-REQUIREMENTS.md` (archived)
  - `.planning/v1.0-MILESTONE-AUDIT.md` (v1.0 audit report)
  - `.planning/config.json` (granularity=coarse, parallelization=true, model_profile=quality)
  - `improvements.md` (repo root — v2.0 product-direction brief; v1.1 scope source)

---
*v1.1 milestone roadmap approved — ready to plan Phase 6*
