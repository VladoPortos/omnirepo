# Walkthrough #3 — Pre-release Full End-to-End UAT

> **Goal:** Exercise every normal-user path in the OmniRepo UI and backend
> before public release. Catch every bug, warning, and console spew; fix
> each at the root (no workarounds); verify with Codex; retest with
> Playwright until every batch is flawless.

## Scope

This walkthrough covers the app as of **2026-04-22** (post v1.3, post Git
mirror + Helm OCI minor versions). It treats the app as a black box from
the user's perspective and exercises every feature surface via Playwright
MCP + direct API + protocol clients (docker, helm, git, aws-cli, curl).

## Batch map

Execute batches in order. Each batch builds state (users, projects, repos)
that later batches reuse — **do not wipe the data root between batches**
unless the batch explicitly asks for it.

| # | File | Area | Status |
|---|------|------|--------|
| 01 | [batch-01-install-bootstrap.md](batch-01-install-bootstrap.md) | Install, first-run setup, login, logout, session | ✅ |
| 02 | [batch-02-user-mgmt.md](batch-02-user-mgmt.md) | User CRUD, password changes, admin force-reset | ⬜ |
| 03 | [batch-03-profile-keys.md](batch-03-profile-keys.md) | Profile, self-service, API keys, S3 keys, delete account | ⬜ |
| 04 | [batch-04-projects-members.md](batch-04-projects-members.md) | Projects, members, access control, upstream creds | ⬜ |
| 05 | [batch-05-docker-oci.md](batch-05-docker-oci.md) | Docker/OCI: push, browse, scan, pull-external, severity gate | ⬜ |
| 06 | [batch-06-rpm-apt.md](batch-06-rpm-apt.md) | RPM & APT: upload, mirror, sync, metadata regen, delete | ⬜ |
| 07 | [batch-07-pypi.md](batch-07-pypi.md) | PyPI: upload, PEP 503 simple index, mirror, sync, delete | ⬜ |
| 08 | [batch-08-helm-http.md](batch-08-helm-http.md) | Helm HTTP: upload, index.yaml, mirror (charts.bitnami HTTP) | ⬜ |
| 09 | [batch-09-helm-oci.md](batch-09-helm-oci.md) | **Helm OCI (NEW v1.3+)**: oci:// upstream, cred gate, tag-rebound | ⬜ |
| 10 | [batch-10-git-hosting.md](batch-10-git-hosting.md) | Git hosting (non-mirror): clone/push/fetch, browse, blame, compare | ⬜ |
| 11 | [batch-11-git-mirror.md](batch-11-git-mirror.md) | **Git mirrors (NEW v1.3+)**: sync, LFS gate 501, receive-pack 403, badge | ⬜ |
| 12 | [batch-12-raw-s3.md](batch-12-raw-s3.md) | Raw blobs + S3 buckets + SigV4 + object CRUD | ⬜ |
| 13 | [batch-13-scanning.md](batch-13-scanning.md) | Trivy DB, rescan, SBOM, severity gates (all 5 protocols) | ⬜ |
| 14 | [batch-14-admin.md](batch-14-admin.md) | TLS, audit log, trash/restore, GC, maintenance, **DB health (NEW)** | ⬜ |
| 15 | [batch-15-cross-cutting.md](batch-15-cross-cutting.md) | Search, dashboard, API docs, error envelopes, a11y, console cleanliness | ⬜ |

Legend: ⬜ not started · 🟨 in progress · ✅ passed clean · 🟥 blocked · ♻ retest needed

## Rules of engagement

1. **One batch at a time.** Finish the batch's sign-off before starting the next. If interrupted, leave the batch in 🟨 and the checkboxes reflect exactly where you are.
2. **Every UI click goes through Playwright MCP.** Record console errors, network failures (anything ≥ 400 except intentional 401/403/404 paths), and backend log errors as findings.
3. **Real fixes only.** If a finding is discovered, fix the root cause — no try/catch silencers, no `console.log` suppressors, no comment-and-skip. The fix is a commit, with Codex verification on top.
4. **Codex after every batch** — after all findings in a batch are fixed, invoke the Codex rescue subagent per CLAUDE.md global rule. Report back blockers/real-issues in the batch file.
5. **Retest after every fix.** A fix is not done until the original repro is run again and the finding is confirmed gone.
6. **Console cleanliness is a merge gate.** A clean batch has zero console errors, zero unexpected warnings, zero unhandled promise rejections, and zero backend panics/ERROR log lines for the flows tested.
7. **Work across sessions.** Progress lives in these files. A fresh session should be able to read the batch file, see where testing stopped, and resume.

## Common setup

See [TESTING-PROTOCOL.md](TESTING-PROTOCOL.md) for:
- How to start a clean server
- How to capture backend + Playwright console logs
- How to seed bootstrap data reliably
- How to drive Playwright MCP
- How to run Codex verification
- How to classify & file findings

## Findings log

All findings from all batches roll up into [FINDINGS.md](FINDINGS.md). Each
finding gets a stable ID `F-<batch>.<n>` (e.g. `F-05.3` = 3rd finding in
batch 05) and carries severity, repro, root cause, fix commit, Codex
verdict, and retest status.

## Release gate

Release to public is gated on:

- [ ] All 15 batches ✅
- [ ] FINDINGS.md has zero blockers and zero unfixed real-bugs
- [ ] `make test` green
- [ ] `make e2e` green
- [ ] `make test-airgap` green
- [ ] One final cold-start smoke: fresh data root → setup → login → dashboard → create project → create repo → push/upload → pull → scan → delete → trash restore → zero console/backend errors
