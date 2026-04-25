# Walkthrough #4 — Findings index

Status: 🟨 Open · ✅ Closed · 🟥 Rejected (disputed)

> Append entries as findings are discovered. Each finding lives in full in its
> batch file; this file is the master index for cross-batch visibility.

## Summary by batch

| Batch | Findings opened | Closed | Open | Blockers |
|---|---|---|---|---|
| 01 | 0 | 0 | 0 | 0 |
| 02 | 2 | 2 | 0 | 1 |
| 03 | 0 | 0 | 0 | 0 |
| 04 | 0 | 0 | 0 | 0 |
| 05 | 0 | 0 | 0 | 0 |
| 06 | 1 | 1 | 0 | 1 |
| 07 | 0 | 0 | 0 | 0 |
| 08 | 0 | 0 | 0 | 0 |
| 09 | 0 | 0 | 0 | 0 |
| 10 | 0 | 0 | 0 | 0 |
| 11 | 0 | 0 | 0 | 0 |
| 12 | 1 | 1 | 0 | 1 |
| 13 | 1 | 0 | 1 | 0 |
| 14 | 0 | 0 | 0 | 0 |
| 15 | 0 | 0 | 0 | 0 |
| 16 | 0 | 0 | 0 | 0 |
| **Total** | **5** | **4** | **1** | **3** |

## Detail

### F-04.1 Self-delete of last super-admin succeeds → instance bricked
- **Severity:** **B / blocker** (regression of wt3 F-02.3)
- **Area:** `internal/api/admin_phase1.go:593 handleDeleteMe`
- **Root cause:** `handleDeleteMe` called `Users.Delete` directly, bypassing the `DeleteEnforceLastSuperAdmin` invariant that the admin-delete sibling enforces.
- **Fix:** commit `a19a512` — switch to `DeleteEnforceLastSuperAdmin`; emit 409 + ErrConflict on `ErrLastSuperAdmin`. Two regression tests added.
- **Status:** ✅ Closed (retest passed end-to-end)

### F-04.2 Change-password + admin PATCH accept weak passwords (setup rejects)
- **Severity:** R / real-bug
- **Area:** `internal/api/admin_phase1.go:474` (change-password), `internal/api/admin_users_full.go:203` (admin PATCH), vs `internal/api/setup.go:85` (the only path with the check)
- **Root cause:** Inline `len < 8` check existed only in setup; siblings either had no check or only `req.New == ""`.
- **Fix:** commit `6bd799c` — `auth.PasswordValid()` + `PasswordMinLen=8` constant; wired into all 3 sites. New tests pin the floor.
- **Status:** ✅ Closed (all 3 sites return 422 on weak input)

### F-06.1 RPM mirror parser hardcodes gzip — fails on Fedora/EPEL/Rocky/Alma/Docker-CE upstreams
- **Severity:** **B / blocker**
- **Area:** `internal/protocol/rpm/upstream_parse.go:90` (pre-fix)
- **Root cause:** `gzip.NewReader` invoked unconditionally; ignored the codec advertised by `repomd.xml`'s `<location href="...primary.xml.{gz,xz,zst}">`. Every modern upstream uses `.xz` (Fedora/EPEL/Rocky/Alma) or `.zst` (Docker CE / Microsoft) — all failed.
- **Fix:** commit `25c1f7b` — `openPrimaryReader(href, body)` dispatches by suffix using `compress/gzip`, `ulikunitz/xz`, `klauspost/compress/zstd`. Three regression tests pin each codec.
- **Status:** ✅ Closed (mirror of `download.docker.com/linux/centos/9/x86_64/stable/` synced 34 docker-buildx packages, ~545 MB)

### F-12.1 S3 HeadObject + GetObject of multipart-uploaded objects missing Last-Modified header
- **Severity:** **B / blocker** (any aws s3 cp of a multipart-stitched object fails with `fatal error: 'LastModified'`)
- **Area:** `internal/protocol/s3/backend/backend.go:561 HeadObject` + `:514 GetObject`
- **Root cause:** Both methods returned `Metadata: unmarshalMeta(row.MetadataJSON, row.ContentType)`. Single-shot uploads work because gofakes3's PutObject path stamps `meta["Last-Modified"]` BEFORE the backend is called. Multipart's CompleteMultipartUpload reuses the metadata captured at CreateMultipartUpload (which is empty), so the persisted MetadataJSON never contained Last-Modified.
- **Fix:** commit `9ae53af` — `enrichMetaWithLastModified(m, t)` injects Last-Modified from `row.CreatedAt` (formatted via `http.TimeFormat`) when missing. Wired into both HeadObject and GetObject; new regression test `TestHeadObject_AlwaysHasLastModified`.
- **Status:** ✅ Closed (32 MB multipart download via `aws s3 cp` round-trips byte-identical)

### F-13.1 Trivy concurrent-first-scan races on schema-version write to cache-dir
- **Severity:** R / real-bug (operator-workaround exists; not blocking)
- **Area:** `internal/scan/trivy.go::baseFlags` + scan-pool concurrency
- **Symptom:** Burst of N concurrent scans against a freshly-uploaded Trivy DB → some succeed, others fail with `[vulndb] The first run cannot skip downloading DB`. Trivy v0.69 attempts a one-time "Adding schema version to the DB repository" write; concurrent invocations race on it.
- **Workaround:** push a single canary image first to warm the cache; then push the rest. Real-world operator pattern (DB-upload-then-paced-push) is unaffected.
- **Status:** 🟨 Open — filed for v1.8 follow-up. Mitigation candidates: serialize first scan against a fresh DB; pre-warm by invoking Trivy once after upload.
