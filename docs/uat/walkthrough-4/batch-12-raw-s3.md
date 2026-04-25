# Batch 12 — Raw + S3 + SigV4 + literal `%` in paths

**Status:** ✅ Passed (1 fix shipped, 0 open)
**Prereqs:** Batch 04 ✅
**State produced for later batches:**
- `acme/raw/demo` with `blob.txt` and `has%percent.txt` (literal `%` in filename)
- `acme/s3/my-bucket` with `test.txt` (48 B) and `big.bin` (32 MB multipart-uploaded)
- Alice's S3 access key id `AKIACYV4DBDC5I4R4QSS`

## Test cases

### 12.1 Raw upload + GET ✅
- `PUT /acme/raw/demo/blob.txt` (basic-auth API key) → 201; `GET` returns the body byte-identical.

### 12.2 v1.7 RAWFIX-01..03 — literal `%` in path ✅
- `PUT /acme/raw/demo/has%25percent.txt` (URL-encoded `%`) → 201.
- `GET /acme/raw/demo/has%25percent.txt` → 200 with body `literal-percent`.
- v1.7 fix holding — chi already URL-decodes once; the prior double-decode would have rejected this path with `validation.failed`.

### 12.3 S3 bucket create via API ✅
- `POST /api/v1/projects/acme/s3-buckets {name:"my-bucket"}` → 201.

### 12.4 S3 access key mint via API ✅
- `POST /api/v1/me/s3-keys {project_id:1}` → 201 with `{access_key_id, secret_access_key, project_id, label, created_at}`.
- Initial wrong path `POST /api/v1/projects/acme/s3-keys` → 404 (route doesn't exist; correct path is `/me/s3-keys` because S3 keys are user-scoped, project-pinned).

### 12.5 S3 endpoint discovery ✅
- AWS CLI default `--endpoint-url http://localhost:28080` hits `GET /` for ListBuckets — that's the SPA fallback handler returning HTML, AWS-CLI reports as 500.
- Correct invocation: `--endpoint-url http://localhost:28080/s3`. With this, all S3 ops work.
- Polish opportunity: when SigV4 headers are present on a `/` request, the server could detect and route to S3 ListBuckets or return an XML envelope. Logged for v1.8.

### 12.6 S3 single-shot upload + ListObjects + GetObject ✅
- `aws s3 cp /tmp/obj.txt s3://my-bucket/test.txt` → success.
- `aws s3 ls s3://my-bucket/` → `2026-04-25 23:28:40  48 test.txt`.
- `aws s3 cp s3://my-bucket/test.txt /tmp/back.txt` → byte-identical.

### 12.7 S3 multipart upload + ListObjects ✅
- 32 MB file via `aws s3 cp` auto-shards (parts of 8 MiB by default) → "upload: ../../../tmp/big.bin to s3://my-bucket/big.bin".
- `aws s3api list-objects` shows `ETag: "d97fb2de959ca8371f0482bcdfd1dcd3-4"` (4-part multipart ETag, well-formed).

### 12.8 S3 multipart download — F-12.1 BLOCKER 🟥 → ✅ fixed
- **Pre-fix:** `aws s3 cp s3://my-bucket/big.bin /tmp/back.bin` → `fatal error: 'LastModified'`. Investigation: HeadObject on multipart objects returned no Last-Modified field; AWS-CLI's high-level downloader requires it. Single-shot objects had it (gofakes3 stamps in PutObject path). Multipart-completed objects carried whatever metadata was captured at CreateMultipartUpload time (none) into HeadObject.
- **Fix shipped** (commit `9ae53af`): added `enrichMetaWithLastModified(m, t)` that injects `Last-Modified` formatted via `http.TimeFormat` from `row.CreatedAt` when missing. Wired into both `HeadObject` and `GetObject`.
- **Test:** `TestHeadObject_AlwaysHasLastModified` simulates multipart-shape MetadataJSON (no Last-Modified key) and asserts HeadObject returns a parseable `http.TimeFormat` value.
- **End-to-end re-verification:** 32 MB upload-then-download via aws s3 cp completes; SHA-256 byte-identical.

### 12.9 SigV4 gating ✅
- Anonymous request to `/s3/my-bucket/test.txt` → `HTTP 403` with proper XML envelope `<Error><Code>InvalidAccessKeyId</Code>...`.
- Wrong creds → `HTTP 403` Forbidden.
- All `/s3/*` requests pass through `RejectNonSigV4` + `SigV4Middleware` + `RequireBucketAccess`.

### 12.10 Drift purge ⬜ deferred to batch 16

## Findings

### F-12.1 S3 HeadObject + GetObject of multipart-uploaded objects missing Last-Modified header
- **Severity:** **B / blocker** (any aws s3 cp of multipart-stitched object fails)
- **Area:** `internal/protocol/s3/backend/backend.go:561 HeadObject` + `:514 GetObject`
- **Symptom:** `aws s3 cp s3://b/big-file /local` → `fatal error: 'LastModified'`. HeadObject XML response was missing the `<LastModified>` element for any object uploaded via multipart.
- **Root cause:** Both methods returned a `gofakes3.Object{Metadata: unmarshalMeta(row.MetadataJSON, row.ContentType)}`. Single-shot uploads work because gofakes3's PutObject path stamps `meta["Last-Modified"]` BEFORE our backend's PutObject is called. Multipart's CompleteMultipartUpload reuses the metadata captured at CreateMultipartUpload (which is empty), so the persisted MetadataJSON never contained Last-Modified.
- **Fix:** commit `9ae53af` — `enrichMetaWithLastModified(m, t)` injects Last-Modified from `row.CreatedAt` when missing. Wired into both HeadObject and GetObject.
- **Codex verify:** ⬜ Pending
- **Retest:** ✅ aws s3 cp multipart download now works byte-for-byte.
- **Status:** ✅ Closed

## Sign-off
- [x] All in-scope cases marked
- [x] Backend log gate: 0 hits
- [ ] Codex batch-end review (will batch with 13–14)
- [x] Status flipped to ✅
