# Walkthrough #4 — Pre-Public-Release UAT

> **Goal:** exhaustive Playwright + protocol-client validation of every
> OmniRepo feature against **live upstream registries** (Docker Hub,
> charts.bitnami.com, pypi.org, deb.debian.org, GitHub) before tagging
> v1.8 / public release. Zero deferred bugs at the finish line.

## State at start

- **Date:** 2026-04-25
- **HEAD:** `2daa446` (post v1.7 phases 1–5, manualChunks bundle landed)
- **Branch:** `main`, working tree clean
- **Server:** `./bin/omnirepo serve` on `28080/28443`, data root `/tmp/omnirepo-wt4`
- **Log:** `/tmp/omnirepo-wt4/server.log`
- **Base URL (UI):** `https://localhost:28443` (HTTPS, self-signed) and `http://localhost:28080` (HTTP)
- **Base URL (protocol clients):** `http://localhost:28080`

## Batch map

| # | File | Area | Live upstream / client | Status |
|---|------|------|------------------------|--------|
| 01 | [batch-01-install-bootstrap.md](batch-01-install-bootstrap.md) | Install · setup · auth · sessions | curl + Playwright | ✅ |
| 02 | [batch-02-user-mgmt.md](batch-02-user-mgmt.md) | User CRUD · password · last-admin | Playwright + API | ✅ (2 fixes) |
| 03 | [batch-03-profile-keys.md](batch-03-profile-keys.md) | Profile · API keys · S3 keys | Playwright + curl | ✅ |
| 04 | [batch-04-projects-members.md](batch-04-projects-members.md) | Projects · members · RBAC · upstream creds | Playwright + API | ✅ |
| 05 | [batch-05-docker-oci.md](batch-05-docker-oci.md) | Docker / OCI: push · pull · scan · clone-from-DH | `docker push/pull` | ✅ |
| 06 | [batch-06-rpm-apt.md](batch-06-rpm-apt.md) | RPM + APT: upload · mirror · drift purge | `dnf` + `apt-get` | ✅ (1 fix) |
| 07 | [batch-07-pypi.md](batch-07-pypi.md) | PyPI: upload · PEP 503 · mirror · PEP 440 | `pip` | ✅ |
| 08 | [batch-08-helm-http.md](batch-08-helm-http.md) | Helm HTTP: upload · index.yaml · mirror | `helm` | ✅ |
| 09 | [batch-09-helm-oci.md](batch-09-helm-oci.md) | Helm OCI: oci:// · cred gate · tag-rebound | `helm` + Docker Hub | ✅ |
| 10 | [batch-10-git-hosting.md](batch-10-git-hosting.md) | Git hosting: clone/push/fetch · browse | `git` | ✅ |
| 11 | [batch-11-git-mirror.md](batch-11-git-mirror.md) | Git mirror: sync · LFS gate · receive-pack 403 | `git` | ✅ |
| 12 | [batch-12-raw-s3.md](batch-12-raw-s3.md) | Raw + S3 + SigV4 + literal `%` paths | `curl` + `aws-cli s3` | ✅ (1 fix) |
| 13 | [batch-13-scanning.md](batch-13-scanning.md) | Trivy DB + auto-scan + SBOM + severity gates | Trivy DB + Playwright | ✅ (1 follow-up) |
| 14 | [batch-14-admin.md](batch-14-admin.md) | TLS · audit · trash · GC · DB health | Playwright + curl | ✅ |
| 15 | [batch-15-cross-cutting.md](batch-15-cross-cutting.md) | Search · dashboard · API docs · a11y · console | Playwright | ✅ |
| 16 | [batch-16-v17-deltas.md](batch-16-v17-deltas.md) | Drift surfacing · % threshold · bundle cold-load | Playwright | ⬜ |

Legend: ⬜ not started · 🟨 in progress · ✅ passed clean · 🟥 blocker · ♻ retest needed

## Rules of engagement

(Lifted from walkthrough-3 with the new ports — **the protocol does not change**.)

1. One batch at a time. State carries forward (don't wipe the data root).
2. Every UI click goes through Playwright MCP.
3. After every flow: console messages + network requests + backend log gate.
4. Real fixes only — no try/catch silencers, no comment-and-skip.
5. Codex verify after each batch via `Agent(subagent_type="codex:codex-rescue", ...)`.
6. Retest after every fix.
7. Console cleanliness is a merge gate.
8. Findings indexed in `FINDINGS.md` with IDs `F-<batch>.<n>`.

See `TESTING-PROTOCOL.md` for full procedure (copied from walkthrough-3 with wt4 ports).
