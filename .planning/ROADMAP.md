# OmniRepo Roadmap

## Shipped milestones

- **v1.0 — MVP** (shipped 2026-04-17) — 5 phases, 52 plans, 175 requirements. Single Go binary serving OCI, RPM, APT, PyPI, Helm, RAW, S3 (SigV4), and Git on one port with embedded React SPA, Trivy scanning, and a hard no-outbound-network invariant. See [`milestones/v1.0-ROADMAP.md`](milestones/v1.0-ROADMAP.md).

## Active milestone

_None yet._ Start the next milestone with `/gsd-new-milestone` — that command
walks questioning → research → requirements → roadmap and produces a fresh
`.planning/REQUIREMENTS.md` plus per-phase plan skeletons.

## Backlog

_Forward-looking ideas not yet scheduled into a milestone live in
`NEXT-SESSION-ISSUES.md` at the repo root._ Current entries (carried from v1.0
closing audit):

- Docker shared-blob storage overestimate — revisit when billing/quota work begins.
- DEB `resolveDebPoolPath` assumes standard Debian pool layout; exotic layouts may 404.
- Codex rescue pass across the 2026-04-17 shipping batch (S3 bucket REST, admin GC status, UI rewrite).
