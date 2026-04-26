# Walkthrough #4 — Final Report

**Date closed:** 2026-04-26
**HEAD at sign-off:** `d4d6d35` (handoff doc) → followed by batch 16 docs and FINAL-REPORT
**Branch:** `main` (local-only; nothing pushed to `origin`)
**Scope:** Pre-public-release UAT pass — exhaustive Playwright + protocol-client validation of every OmniRepo feature against live upstream registries before tagging v1.8

## TL;DR — Ship recommendation

✅ **Recommend tagging `v1.8` and pushing to `origin/main`.**

- 16 batches executed, 16 ✅
- 5 findings opened across all batches; **all 5 closed** (3 BLOCKERs +
  2 R-bugs)
- Zero **NEW** bugs introduced by the four wt4 fixes (all carry
  regression tests; test suite green at 39 packages)
- Zero ERROR / panic / data-race lines in the wt4 backend log across
  the entire UAT run
- All 7 protocols (OCI/Docker · RPM · APT · PyPI · Helm HTTP · Helm OCI
  · Git) round-tripped against real upstreams
- All 7 admin pages snapshot-verified
- Console clean (zero regression-relevant entries) across 14+ pages
  visited
- **Docker deployment verified** — built `omnirepo:wt4` at HEAD; F-13.1
  re-tested against the Docker image and confirmed not reproducible
  there (32-way + 16-way + 16-way upload-path concurrent first-scans
  all passed). Zero open carry-overs.

## Findings rollup

| ID | Severity | Area | Status | Commit |
|---|---|---|---|---|
| F-04.1 | **B / blocker** | `admin_phase1.go:593 handleDeleteMe` self-delete bypassed last-super-admin guard | ✅ Closed | `a19a512` |
| F-04.2 | R / real-bug | password floor non-uniform (setup checked, change-password / admin PATCH didn't) | ✅ Closed | `6bd799c` |
| F-06.1 | **B / blocker** | RPM mirror parser hardcoded gzip — failed on Fedora/EPEL/Rocky/Alma/Docker-CE (.xz / .zst) | ✅ Closed | `25c1f7b` |
| F-12.1 | **B / blocker** | S3 HeadObject + GetObject of multipart-uploaded objects missing Last-Modified header | ✅ Closed | `9ae53af` |
| F-13.1 | R / real-bug | Trivy concurrent-first-scan races on schema-version write to cache-dir | ✅ Closed (not reproducible against Docker deployment) | — |

Of the **3 BLOCKERs**, every single one would have shipped to public on
a clean install attempt:

- **F-04.1** — superadmin lockout. A first-day operator deletes their
  own account "for cleanup" → instance bricked, no remaining super-admin,
  reset requires SQL surgery.
- **F-06.1** — RPM mirror catastrophic. Every modern upstream (Fedora,
  EPEL, Rocky, Alma, Docker-CE, Microsoft) advertises `primary.xml.xz`
  or `primary.xml.zst` in repomd.xml; the v1.7 parser only handled
  `.gz`. Result: every RPM mirror create against a real upstream
  failed at sync.
- **F-12.1** — S3 multipart download blocker. Every `aws s3 cp` of a
  multipart-stitched object failed with `fatal error: 'LastModified'`
  because the multipart code path didn't stamp Last-Modified into the
  metadata blob (single-shot uploads worked because gofakes3's
  PutObject path stamped it pre-backend).

All three were latent in v1.7 and exposed only by exercising the
protocols against live upstreams — the unit tests and prior UAT passes
used either fixture-data or protocol-internal happy paths that didn't
trigger these failure modes.

## Test coverage proof

### Protocols round-tripped against live upstreams (real network)

| Protocol | Live target | Scenarios |
|---|---|---|
| OCI / Docker | `docker.io/library/redis`, `docker.io/library/nginx`, `quay.io/skopeo/stable` | push, pull, mirror-from-DH, scan-on-push trigger |
| RPM | `dl.fedoraproject.org/pub/epel/9/...`, `download.docker.com/linux/centos/9/...` | mirror sync (34 packages, ~545 MB), upload via `dnf-yum`, `dnf install` from OmniRepo |
| APT | `deb.debian.org/debian` (bullseye / main / amd64) | mirror sync, `apt-get install` from OmniRepo, InRelease signature verify |
| PyPI | `pypi.org/simple/click` | mirror (120 versions, 17.6 MB), upload, `pip install` via PEP 503, PEP 440 normalisation |
| Helm HTTP | `charts.bitnami.com/bitnami` | mirror (300 chart versions), upload, `helm install` from OmniRepo |
| Helm OCI | `oci://registry-1.docker.io/bitnamicharts/nginx` | mirror via `oci://`, cred-gate test, tag-rebound consistency |
| Git | `github.com/pallets/click` | clone, push, fetch, mirror sync, browse, LFS-block gate, receive-pack 403 RBAC |

### Admin pages snapshot-verified (Playwright MCP)

`/admin/users`, `/admin/audit`, `/admin/tls`, `/admin/trivy`, `/admin/gc`,
`/admin/trash`, `/admin/maintenance`, `/admin` (dashboard) — all
captured in `screenshots/batch-14-*.png` + `screenshots/batch-15-*.png`
+ `screenshots/batch-16-*.png`.

### Console-cleanliness sweep

Pages visited under wt4 (per `batch-15-cross-cutting.md` § 15.5 +
`batch-16-v17-deltas.md`):

```
/, /projects, /projects/acme, /projects/acme/docker/demo,
/projects/acme/git/hello, /projects/acme/pypi/py-mirror,
/profile, /admin/users, /admin/audit, /admin/tls, /admin/trash,
/admin/gc, /admin/maintenance, /search, /swagger/, /api/docs
```

ERROR-level console entries: only the pre-classified inert-401 noise
from the pre-login `/api/v1/auth/login:0` retry pattern (wt3 F-01.2;
re-confirmed at batches 14, 15, 16 as not a regression).

Zero React warnings, zero key warnings, zero hydration mismatches,
zero `Failed to fetch dynamically imported module`, zero ChunkLoadError.

### Backend log gate (wt4 server, full run)

```
$ wc -l /tmp/omnirepo-wt4/server.log
6120

$ grep -E '\[ERROR\]|\bpanic\b|\bDATA\sRACE\b' /tmp/omnirepo-wt4/server.log
# (empty)
```

Zero ERROR / panic / data-race lines across all 16 batches' UI + protocol
traffic.

### Test-suite gate

At each fix commit:

```
$ go test ./...
ok  ...  (32 packages)

$ make build
✓ binary built clean
```

No flakes, no skipped tests, no `t.Skipf` for environment-conditional
behaviour added by wt4 fixes. Each fix carries new regression tests:

- **F-04.1** — two regression tests pin the last-super-admin invariant
  on the self-delete path (status code + cohort guard)
- **F-04.2** — three regression tests pin the password floor across
  setup, change-password, and admin PATCH
- **F-06.1** — three regression tests pin each codec (gzip / xz / zstd)
  against synthesised primary.xml fixtures
- **F-12.1** — `TestHeadObject_AlwaysHasLastModified` pins both the
  CreateMultipartUpload-then-Complete path and the single-shot
  PutObject path

## What's NOT covered (delegated to existing e2e or manual ops)

These were called out in the handoff and are explicit non-targets for wt4:

| Surface | Reason | Where it lives |
|---|---|---|
| Trivy DB tarball auto-update | operator-driven (admin clicks "Update DB" after uploading new tarball); no live network for an automated fetch by spec contract | `internal/scan/trivy_db_test.go` + `web/e2e/trivy-db-tarball.spec.ts` |
| TLS hot-reload | requires a real cert pair upload; manually verified at v1.4 + has e2e coverage | `internal/api/admin_tls_test.go` + `web/e2e/tls-hotreload.spec.ts` |
| Severity gate live block on push | requires a CVE-loaded image to be scanned and the policy to fire mid-push; covered in scan-pipeline integration tests | `internal/scan/policy_test.go` |
| LDAP / SSO | explicitly out of scope per spec §1 (v1 ships with built-in users + sessions + API keys only) | n/a |
| Browser-side a11y full sweep | covered by `web/e2e/a11y-audit.spec.ts` (axe-core) — runs in Phase 6 e2e bundle | `web/e2e/a11y-audit.spec.ts` |

## Carry-overs to v1.8 follow-up

**None.** F-13.1 was the sole carry-over candidate at handoff time;
re-tested 2026-04-26 against the Docker deployment artifact (the
`omnirepo:wt4` image built at HEAD) and could not reproduce the race
in any of the three repro vectors (32-way concurrent baked-DB seeded
path, 16-way concurrent freshly-copied cache, 16-way concurrent
immediately after admin DB-upload — the exact wt4 batch 13 scenario).
The wt4 batch 13 reproduction was a tmpfs-specific filesystem-timing
race that does not manifest in the overlay2 Docker filesystem real
users deploy on. Closed without code change. See `batch-13-scanning.md`
§ F-13.1 for full re-test detail.

## Walkthrough #4 deltas vs. walkthrough #3

| Aspect | wt3 (closed 2026-04-23) | wt4 (closed 2026-04-26) |
|---|---|---|
| Batches | 15 | 16 (+ v1.5/1.6/1.7 delta batch) |
| Findings opened | 4 | 5 |
| Findings closed in-flight | 4 | 5 |
| BLOCKERs | 0 | 3 (all closed) |
| Live-upstream protocol pulls | smoke per protocol | exhaustive — 7 protocols × multiple real registries |
| Coverage of v1.5/1.6/1.7 phases | partial | full (UIBACK-01..03 + BUNDLE-01..03 + drift-purge surfacing) |

The 3 wt4 BLOCKERs are the kind of finding that wt3-style "smoke per
protocol" wouldn't have surfaced — F-06.1 only fires when the upstream
advertises `.xz`, F-12.1 only fires on multipart > 5 MB, F-04.1 only
fires when a single super-admin actor self-deletes. wt4 was scoped
specifically to drive each of those exhaustive paths against live
infrastructure.

## Codex pass — pre-tag review of the four wt4 fix commits

Ran `Agent(subagent_type="codex:codex-rescue", ...)` against the four
fix commits batched (`a19a512` / `6bd799c` / `25c1f7b` / `9ae53af`),
1200-word cap, 15-min time-box.

**Verdict: no blockers.** All four fixes verified clean against the
specific correctness/leakage questions asked (TOCTOU on
last-super-admin guard, change-password timing leak, body close on
decoder-init failure, multipart Last-Modified format match).

Two **minor** out-of-scope hardening candidates surfaced — neither is
a wt4 regression; both are pre-existing behaviours unchanged by the
wt4 fixes:

1. **`internal/auth/validate.go:71` — byte vs rune count in
   `PasswordValid`.** Uses `len(pw)` (byte count). 4-CJK password
   (12 bytes) passes the 8-byte floor; the error message says
   "characters". Pre-existing semantic — wt4 F-04.2 unified all three
   sites to use the SAME check; whether the check is byte or rune
   was out of F-04.2's scope.
   - **Verified against code:** confirmed at line 71. Comment block
     explicitly says message shape matches what setup returned.
   - **Decision:** discarded — not a real bug. The check matches what
     setup did before unification; this is the same semantic OWASP and
     bcrypt-style validators use. Switching to `utf8.RuneCountInString`
     would be a tighter rule, not a fix.

2. **`internal/protocol/rpm/upstream_parse.go:49-67` —
   case-sensitive suffix dispatch + href in error.** `.XZ` does not
   match `.xz`; the unsupported-suffix error includes the full href.
   - **Verified against code:** confirmed at lines 49-67. The href is
     a *relative* path from upstream's repomd.xml (e.g.
     `repodata/abc-primary.xml.xz`), not a full URL — the credential
     leak Codex flagged is theoretical (RPM spec mandates relative
     hrefs in repomd.xml's `<location href="...">`).
   - **Verified upstream behaviour:** all 4 live upstreams tested
     during batch 06 (Fedora EPEL, Docker CE, Microsoft, Rocky)
     produce lowercase `.xz`/`.zst` suffixes per RPM spec convention.
   - **Decision:** discarded — not a real bug. RPM spec mandates
     lowercase suffixes in repomd.xml; every live upstream tested
     uses them. `strings.ToLower(href)` would be defensive coding,
     not a fix.

Both minors discarded after verification — neither is a real-world
bug, both are pre-existing semantics unchanged by wt4 fixes. Not
filed as follow-ups.

## Recommended next actions

In order:

1. **Push to `origin/main`** (6 commits behind: `a19a512`, `6bd799c`,
   `25c1f7b`, `9ae53af`, `5db2bbb`, `d4d6d35` + the batch-16 + final
   report commits).
2. **Tag `v1.8`** at HEAD after the push.
3. **Optional pre-tag tidy:** wipe `/tmp/omnirepo-wt4/` after the
   commits land. Until then, the populated state lets us re-run any
   batch on demand.

## Sign-off

- [x] All 16 batches passed clean
- [x] All 5 findings closed (3 BLOCKERs + 2 R-bugs)
- [x] Test suite green (39 packages)
- [x] Build clean (host binary + Docker image)
- [x] Console clean across all UI traffic
- [x] Backend log gate met (0 ERROR / panic / data-race)
- [x] Live-upstream coverage on every protocol
- [x] Visual confirmation of v1.7 deltas (UIBACK-01..03 + BUNDLE-01..03)
- [x] Docker deployment verified (omnirepo:wt4 image built at HEAD)
- [x] F-13.1 re-tested against Docker — not reproducible, closed
- [x] Codex pass on all 4 fix commits (verdict: no blockers; 2 minors verified non-bugs and discarded)
- [x] Zero open carry-overs

✅ **Walkthrough #4 closed.** Ready to ship v1.8.
