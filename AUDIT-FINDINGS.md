# OmniRepo v1.0 Pre-Release Audit — Consolidated Findings

**Date:** 2026-04-16
**Sources:** Codex deep review + external auditor report, cross-verified against codebase.
**Status:** All findings verified. Nothing fixed yet — this file is for triage.

---

## Summary

| Severity | Count |
|----------|-------|
| Critical | 1     |
| High     | 7     |
| Medium   | 14    |
| Low      | 4     |
| **Total** | **26** |

---

## Critical

### CR-01. gzip.Close() error ignored on git push body decode
- **File:** `internal/protocol/git/gogit/server.go:110`
- **Source:** Auditor C1
- **Issue:** `defer gz.Close()` on a `gzip.Reader` wrapping the request body silently drops close errors. For a decompression stream this can mask a truncated/corrupt push body — the handler processes partial data without knowing the stream never completed.
- **Fix:** Check close error and fail the request with 400.

---

## High

### HI-01. Sessions not invalidated on password change or admin reset
- **File:** `internal/api/admin_phase1.go:387`, `internal/api/admin_users_full.go:190`
- **Source:** Both Codex #3 and Auditor H1 (overlap)
- **Issue:** `handleChangePassword` and admin password reset update the hash but never delete existing sessions. A stolen session cookie stays valid after password rotation, undermining the `must_change_password` flow.
- **Fix:** After `Users.UpdatePasswordHash()`, call `Sessions.DeleteAllForUser(ctx, userID)`.

### HI-02. Orphaned files on upload DB failure (5 handlers)
- **File:** `internal/protocol/raw/put.go:61`, `internal/protocol/rpm/put.go:101`, `internal/protocol/helm/put.go:126`, `internal/protocol/pypi/upload_legacy.go:200`, `internal/protocol/pypi/upload_pep694.go:431`
- **Source:** Both Codex #2 and Auditor (partial overlap on theme)
- **Issue:** These upload paths write artifact bytes to disk via `pathStore.Put()` BEFORE the metadata DB transaction commits. If the DB commit fails, the file remains on disk with no corresponding row. Repeated failures fill the volume with orphans.
- **Fix:** Add cleanup (`os.Remove`) in the DB-commit error path for each handler, or restructure to write-after-commit with a temp staging area.

### HI-03. S3 multipart: DB commit before os.Rename
- **File:** `internal/protocol/s3/backend/multipart.go:307-328`
- **Source:** Codex #1
- **Issue:** The code intentionally commits the `s3_objects` row BEFORE `os.Rename`. The comment says "if the tx fails, the temp file is cleaned up." But if the rename fails AFTER the DB commit, the DB advertises an object that doesn't exist on disk. S3 GET/HEAD will return errors for a row that appears valid.
- **Fix:** Reverse the order (rename first, then DB commit) or add a compensating DB delete in the rename error path.

### HI-04. Host header injection in OCI Bearer challenge
- **File:** `internal/protocol/oci/token_verify.go:43`
- **Source:** Both Codex #4 and Auditor L2 (overlap — Codex rated High, Auditor rated Low)
- **Issue:** `r.Host` reflected directly into `WWW-Authenticate: Bearer realm="..."`. Behind a permissive proxy, attacker can redirect registry clients to a malicious token endpoint. Code comment acknowledges this as "WR-01 partial fix."
- **Fix:** Use configured `ExternalURL` from config; only fall back to `r.Host` when request arrives via a trusted proxy.

### HI-05. Wrong permission guard on admin user endpoints
- **File:** `internal/api/admin_users_full.go:24-29`
- **Source:** Auditor H2
- **Issue:** `handleListUsers`, `handleGetUser`, `handlePatchUser` are guarded with `auth.ActionTriggerGC` (a garbage collection permission). Today super-admin bypass masks the effect, but granting `TriggerGC` to an ops role would accidentally hand them user-edit powers.
- **Fix:** Use `auth.ActionCreateUser` or introduce a dedicated `ActionAdminUserManage`.

### HI-06. Temp file leaked on CAS put failure (OCI chunked + monolithic)
- **File:** `internal/protocol/oci/blobs.go:372-376` (blobUploadPut), `internal/protocol/oci/blobs.go:472-475` (blobMonolithicPost)
- **Source:** Auditor H3, H4
- **Issue:** When `h.cas.PutFromPath(tmpPath, …)` fails, the temp file at `tmpPath` is never removed. CAS leaves the source file on error. Every failed finalize leaks a full-size blob on disk.
- **Fix:** Add `_ = os.Remove(tmpPath)` in both error branches.

### HI-07. S3 key last_used_at fire-and-forget with context.Background()
- **File:** `internal/protocol/s3/keys/keys.go:96-100`
- **Source:** Auditor H6
- **Issue:** Every S3 request spawns `go func(){ TouchLastUsed(context.Background(), ...) }()`. Under load these accumulate; on shutdown the process can hang or lose the DB reference.
- **Fix:** Use a timeout context derived from the server lifecycle context, or batch updates via a single dispatcher goroutine.

---

## Medium

### ME-01. Login endpoint has no request-size limit
- **File:** `internal/api/admin_phase1.go:297`
- **Source:** Auditor M1
- **Issue:** `json.NewDecoder(r.Body).Decode()` on `/auth/login` without `http.MaxBytesReader`. Unauthenticated attacker can POST multi-GB body to tie up memory.
- **Fix:** `r.Body = http.MaxBytesReader(w, r.Body, 8192)` before decoding.

### ME-02. Change-password endpoint has no request-size limit
- **File:** `internal/api/admin_phase1.go:364`
- **Source:** Auditor M5
- **Issue:** Same as ME-01 but on `/auth/change-password`.
- **Fix:** `http.MaxBytesReader(w, r.Body, 4096)` before decoding.

### ME-03. Search filters (severity, project) ignored by backend
- **File:** `internal/api/search.go:47-53`, `internal/metadata/search.go:42-110`
- **Source:** Codex #6
- **Issue:** API accepts `severity` and `project` params and populates SearchParams, but `SearchAll` only applies `Kind` filter — `Severity` and `Project` are never used in the SQL. Frontend filters show but don't work.
- **Fix:** Add WHERE clauses for severity and project in the SearchAll query builder.

### ME-04. Search results missing entity_id field
- **File:** `internal/api/search.go:25-31`, `web/src/api/types.ts:248`, `web/src/pages/SearchPage.tsx:213`
- **Source:** Codex #8
- **Issue:** Backend `searchResultItem` struct doesn't include `entity_id` in JSON. Frontend `SearchResult` type requires it and uses it as React key. All keys become `kind-undefined`.
- **Fix:** Add `EntityID` field to the API response struct.

### ME-05. useDebounce uses useMemo for timer side effect
- **File:** `web/src/pages/SearchPage.tsx:40-49`
- **Source:** Codex #9
- **Issue:** `useMemo` is used to set a timer; the cleanup function is returned but never called. Old timers are never cancelled, producing stale/out-of-order results.
- **Fix:** Replace `useMemo` with `useEffect`.

### ME-06. Maintenance banner invisible to non-admin users
- **File:** `web/src/hooks/useMaintenance.ts:6`, `internal/api/admin_maintenance.go:20`
- **Source:** Codex #10
- **Issue:** The maintenance status endpoint is admin-only (`RequireCan(ActionTriggerGC)`). Non-admin users never see the maintenance banner.
- **Fix:** Add a public `/api/v1/maintenance/status` endpoint that returns only the boolean flag.

### ME-07. TLS history handler always returns empty (directory structure mismatch)
- **File:** `internal/api/admin_tls_history.go:39-53`, `internal/tls/upload.go:77-78`
- **Source:** Codex #11
- **Issue:** Uploads archive to `certs/uploaded/<timestamp>/server.crt` (subdirectory per upload). History handler scans `certs/uploaded/` but skips directories (`entry.IsDir() → continue`), so it never finds anything.
- **Fix:** Recurse into timestamp subdirectories, or change archive structure to flat files with timestamp in filename.

### ME-08. TLS current cert missing fingerprint_sha256 and source
- **File:** `internal/api/admin_tls_history.go:104-109`, `web/src/api/types.ts:284-298`, `web/src/pages/admin/TLSPage.tsx:118,181`
- **Source:** Codex #12
- **Issue:** Backend returns only 5 fields (subject, issuer, not_before, not_after, dns_names). Frontend expects `fingerprint_sha256` and `source`. Page renders undefined values.
- **Fix:** Compute SHA256 fingerprint from cert.Leaf.Raw and add `source` field to response.

### ME-09. S3 "Create Repository" type always rejected (422)
- **File:** `web/src/pages/ProjectDetailPage.tsx:43`, `internal/api/admin_phase1.go:735`
- **Source:** Codex #13
- **Issue:** Frontend offers S3 as a repo type in the create dialog, but backend `validRepoTypes` excludes `s3` (S3 buckets are a separate resource). Creating an S3 repo is a guaranteed 422.
- **Fix:** Remove `s3` from the repo type list in the frontend, or add a note that S3 buckets are managed separately.

### ME-10. Audit page contract mismatch (nullable fields, outcome values)
- **File:** `internal/api/admin_audit.go:105-116`, `web/src/pages/admin/AuditPage.tsx:218`
- **Source:** Codex #14
- **Issue:** Backend returns nullable fields (`*string` with `omitempty`) and uses outcome `ok`. Frontend expects non-null strings and renders anything other than literal `success` as a failure badge. Anonymous/system events render incorrectly.
- **Fix:** Align frontend types with backend nullable fields; map `ok` to success display.

### ME-11. Dashboard swallows DB errors silently
- **File:** `internal/api/dashboard.go:92-108`
- **Source:** Codex #15
- **Issue:** All dashboard count queries use `_ =` to discard errors, returning zero/partial data. A genuine DB failure looks like an "empty dashboard" rather than an error.
- **Fix:** Check errors and return 500 on DB failure, or at minimum log them.

### ME-12. Disk-full on OCI upload returns 500, not 507
- **File:** `internal/protocol/oci/blobs.go:720` (appendChunk)
- **Source:** Auditor M2
- **Issue:** `io.Copy` with `O_APPEND` — when volume is full, returns generic 500. Docker/crane clients aggressively retry, making it worse. OCI spec defines 507 for this.
- **Fix:** Detect `errors.Is(err, syscall.ENOSPC)` and return 507 with OCI `SIZE_INVALID` error code.

### ME-13. Parent-directory fsync silently skipped on Open failure
- **File:** `internal/storage/atomic.go:63-67`
- **Source:** Auditor M3
- **Issue:** `WriteAndRename` only fsyncs the parent dir inside `if pf, err := os.Open(dir); err == nil { … }`. If Open fails, fsync is dropped silently — a crash after rename but before fsync can lose the rename on ext4/xfs.
- **Fix:** Return the Open error or log loudly.

### ME-14. gzip.Close() error ignored on RPM upstream parse
- **File:** `internal/protocol/rpm/upstream_parse.go:87`
- **Source:** Auditor M6
- **Issue:** `defer func() { _ = gz.Close() }()` swallows the close error. Failed close on a gzip reader means truncated/corrupt upstream metadata — we'd import a half-parsed primary.xml.
- **Fix:** Capture close error into named return and fail the parse.

---

## Low

### LO-01. Profile PATCH does email and avatar as separate writes
- **File:** `internal/api/profile.go:54-71`
- **Source:** Codex #5
- **Issue:** `PATCH /me` updates email and avatar_seed in separate DB calls. If first succeeds and second fails, returns 500 after partial commit. Audit diff is inaccurate.
- **Fix:** Wrap both updates in a single transaction.

### LO-02. Search result click always navigates to project page
- **File:** `web/src/pages/SearchPage.tsx:61-71`
- **Source:** Both Codex #16 and Auditor (overlap)
- **Issue:** `resultRoute()` always returns `/projects/${parts[0]}` regardless of result kind. Artifact/CVE results lose their specificity.
- **Fix:** Build more specific routes based on result kind and location.

### LO-03. "Load more" button on search page is a no-op
- **File:** `web/src/pages/SearchPage.tsx:256-261`
- **Source:** Both Codex #17 and Auditor (overlap)
- **Issue:** Button renders when `next_cursor` exists but has no `onClick` handler. Pagination never advances.
- **Fix:** Add click handler that advances the cursor and fetches next page.

### LO-04. Temp scratch file close error ignored in helm regen
- **File:** `internal/protocol/helm/regen.go:111`
- **Source:** Auditor L1
- **Issue:** `_ = tmpScratch.Close()`. Under failure storms this is a quiet FD leak.
- **Fix:** Log the close error.

---

## Findings Rejected as False Positives

| Report ID | Claim | Reason Rejected |
|-----------|-------|-----------------|
| Auditor H5 | Goroutine leaks in sync handlers (context.Background) | **Verified FALSE** — both `rpm/sync_handler.go` and `pypi/sync_handler.go` use proper timeout contexts from the parent, not `context.Background()`. |
| Auditor M4 | gzip.Close error-ordering in deb regen | **Verified FALSE** — `gz.Close()` error IS checked at line 247 and returns early before `gzBuf.Bytes()` is consumed at line 264. |
| Codex: API keys see empty data | API keys ignored for search/dashboard | **Deferred** — API key scoping is a v1.1 feature, not a bug. Current design is user-membership-based. |
| Codex: S3 multipart DB-first design | Intentional design risk | **Reclassified as HI-03** — the design comment acknowledges the trade-off, but rename-after-commit failure is still a real integrity bug. |

---

## Overlap Map (Codex ↔ Auditor)

| Codex Finding | Auditor Finding | Merged As |
|---------------|-----------------|-----------|
| Codex #3 (sessions survive password change) | Auditor H1 | HI-01 |
| Codex #4 (Host header OCI Bearer) | Auditor L2 | HI-04 |
| Codex #16 (search drops specificity) | — | LO-02 |
| Codex #17 (load more no-op) | — | LO-03 |
| — | Auditor C1 (gzip.Close git) | CR-01 |
| — | Auditor H2 (wrong permission) | HI-05 |
| — | Auditor H3/H4 (OCI temp leak) | HI-06 |
| — | Auditor H6 (S3 key background) | HI-07 |
| — | Auditor M1 (login MaxBytes) | ME-01 |
| — | Auditor M2 (disk-full 507) | ME-12 |
| — | Auditor M3 (fsync skip) | ME-13 |
| — | Auditor M5 (change-pw MaxBytes) | ME-02 |
| — | Auditor M6 (rpm gzip close) | ME-14 |

---

## Suggested Fix Order

**Wave 1 — Security (HI-01, HI-04, HI-05, ME-01, ME-02):**
Session invalidation, host header fix, permission guard, request size limits.

**Wave 2 — Data Integrity (CR-01, HI-03, HI-06, HI-02, ME-12, ME-13):**
gzip error handling, S3 multipart order, temp file leaks, orphan cleanup, disk-full, fsync.

**Wave 3 — Feature Correctness (ME-03 through ME-11, ME-14):**
Search filters, entity_id, useDebounce, maintenance banner, TLS history/current, S3 type, audit types, dashboard errors, RPM gzip.

**Wave 4 — Polish (LO-01 through LO-04):**
Profile partial write, search navigation, load more, helm close.
