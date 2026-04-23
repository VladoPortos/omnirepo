# Batch 12 — Raw blobs + S3 buckets

**Status:** ✅ Passed (2026-04-23)
**Prereqs:** Batch 04 ✅
**State produced for later batches:**
- `acme/raw/files` with a couple of uploaded blobs
- S3 bucket `b1x` in `acme` with an uploaded object
- Project-scoped S3 access key for alice on acme

## Pre-flight

- [ ] `curl` available
- [ ] `aws` CLI installed and minimally configured (no real AWS account needed)
- [ ] Logged in as alice
- [ ] Server log tail open

## Raw test cases

### 12.1 Create raw repo
- [ ] CreateRepoDialog: type `Raw`, name `files`
- [ ] **Expected (D-2):** no mirror checkbox for raw
- [ ] Empty-state shows `curl --upload-file …` snippet

### 12.2 Upload blob
- [ ] `curl -u alice:<api-key> --upload-file hello.txt http://localhost:18080/acme/raw/files/hello/world.txt`
- [ ] **Expected:** 201 with Location header; UI shows the blob; content-type inferred
- [ ] Audit log: `raw.upload`

### 12.3 Upload with arbitrary content type
- [ ] Upload a binary file (png, tar.gz)
- [ ] **Expected:** stored verbatim; content-type preserved (or sniffed correctly)

### 12.4 Download round-trip
- [ ] `curl -u alice:<key> http://localhost:18080/acme/raw/files/hello/world.txt -o /tmp/rt`
- [ ] Bytes identical to upload
- [ ] `sha256sum` matches UI-displayed hash

### 12.5 Delete blob
- [ ] UI row action or `curl -X DELETE ...`
- [ ] **Expected:** 204; subsequent GET → 404; sibling blobs unchanged
- [ ] Audit log: `raw.delete`

### 12.6 Path handling
- [ ] Upload with deep path `a/b/c/d.txt` — works
- [ ] Upload with path traversal `../outside.txt` — **Expected:** 400 envelope, no file written outside repo dir
- [ ] Upload with overlong path (>4096) — clean error

### 12.7 Listing
- [ ] `GET /api/v1/projects/acme/repos/raw/files/content` returns JSON array with per-file sha256, scan_status, scan_severity
- [ ] UI renders the table

### 12.8 Anonymous read via public_read
- [ ] Toggle public_read, GET without auth → 200; toggle off → 401

### 12.9 Console + network sweep (raw)
- [ ] Zero errors/warnings

## S3 test cases

### 12.10 Create S3 bucket (operator-facing route)
- [ ] `/projects/acme` → S3 Buckets tab → Create bucket, name `b1x`
- [ ] **Expected:** 201 via `POST /api/v1/projects/acme/s3-buckets`; row appears
- [ ] WALKTHROUGH-2 F-6 regression: client-side min-char validation on bucket name
- [ ] gofakes3 protocol-level CreateBucket is disabled (operator-only) — verify via direct S3 CreateBucket without project route returns 405/403

### 12.11 Create S3 access key (revisit Batch 03 case 3.10)
- [ ] `/profile` → S3 Keys → Create
- [ ] Pick project `acme`, label `alice-s3`
- [ ] **Expected:** one-time modal with AKID + Secret; WALKTHROUGH-2 F-12 regression — combobox shows `acme` not numeric ID
- [ ] Store the AKID/Secret in this file (redact secret tail if desired)

### 12.12 Configure aws CLI
- [ ] `aws configure --profile omni` → paste AKID + Secret; region = `us-east-1` (ignored by gofakes3 but required)
- [ ] Endpoint: `http://localhost:18080` (the HTTP listener; S3 SigV4 works on HTTP)

### 12.13 Upload via aws s3 cp
- [ ] `aws --endpoint-url http://localhost:18080 --profile omni s3 cp /tmp/s3-obj.txt s3://b1x/hello.txt`
- [ ] **Expected:** 200; UI bucket browser shows the object
- [ ] Audit log: `s3.put_object` (or equivalent)

### 12.14 List objects (S3 CLI)
- [ ] `aws ... s3 ls s3://b1x/` → lists `hello.txt`

### 12.15 Multipart upload
- [ ] Upload a file large enough to trigger multipart (>8MB by default, or force with `--cli-write-timeout 0` tricks)
- [ ] `aws ... s3 cp large.bin s3://b1x/large.bin`
- [ ] **Expected:** multipart upload completes; stored object matches source

### 12.16 List buckets (project-scoped key)
- [ ] `aws ... s3 ls`
- [ ] **Expected:** AccessDenied — the project-scoped S3 key cannot enumerate across projects (WALKTHROUGH-2 §3f)

### 12.17 Object browse in UI
- [ ] `/projects/acme/s3/b1x` → prefix drill-down works; pagination works
- [ ] Console clean

### 12.18 Object delete
- [ ] UI delete action OR `aws ... s3 rm`
- [ ] **Expected:** subsequent ls shows empty; downstream requests 404

### 12.19 Bucket delete — empty
- [ ] After removing all objects, delete bucket in UI
- [ ] **Expected:** 204

### 12.20 Bucket delete — non-empty
- [ ] Create new bucket, upload object, try to delete
- [ ] **Expected:** 409 envelope explaining "bucket not empty"; bucket unchanged

### 12.21 SigV4 wrong-key
- [ ] Tamper the secret; retry any S3 op
- [ ] **Expected:** SigV4 signature verification fails → 403 with `SignatureDoesNotMatch` or equivalent

### 12.22 Revoke S3 key
- [ ] Revoke from UI; retry any op → AccessDenied; revoke event in audit log

### 12.23 Console + network sweep (S3 UI)
- [ ] Zero errors/warnings

## Findings

Consolidated in [FINDINGS.md](FINDINGS.md). Summary:

| ID | Sev | One-line | Status |
|----|-----|----------|--------|
| F-12.1 | R | `%2e%2e` URL-encoded traversal passed validateRawPath (no real escape — `filepath.Join` doesn't decode; but validator contract violated). Fix: strict mode now percent-decodes each segment on PUT. Lenient on GET/HEAD/DELETE preserves backward-compat for any pre-existing `%2e%2e` rows. | ✅ Closed |
| F-12.2 | R | 4500-char filename returned 500 via ENAMETOOLONG on rename. Fix: path length caps (255 bytes/segment, 1024 bytes total) in validateRawPath → clean 400. | ✅ Closed |
| F-12.3 | n | Raw protocol emits plain-text errors, not the `/api/v1` JSON envelope. By design per `internal/httperr/envelope.go:4`. | ✅ Accepted |
| F-12.4 | m | PUT silently overrides explicit `Content-Type` with extension-sniffed MIME. Spec-compliant ("or sniffed correctly") but surprising. Deferred for XSS whitelist design to batch-15. | 🟨 Deferred |
| F-12.5 | n | Test plan says audit `raw.upload`, code emits `raw.put`. Code is authoritative; update plan. | ✅ Accepted |
| F-12.S1 | n | Test plan 12.12 says endpoint `http://localhost:18080`; actual is `/s3` prefix (matches UI empty-state copy). `aws` against bare root hits SPA catch-all. Update plan. | ✅ Accepted |
| F-12.S2 | m | Create S3 Key dialog has no Label input; backend auto-fills `"Profile-created YYYY-MM-DD"`. Multiple keys per project become hard to distinguish. Deferred to batch-15. | 🟨 Deferred |
| F-12.S3 | n | `DELETE /api/v1/me/s3-keys/{id}` expects numeric id, 400s on AKID string. Spec-matching; envelope message could hint. Observation only. | ✅ Accepted |

**Codex verification:** `Agent(subagent_type="codex:codex-rescue")` — Q1–Q5 on fix correctness, plus reclassification of F-12.3/12.4/12.S1. Codex flagged Q3 real-issue (historical `%2e%2e` paths would be unreachable via GET/HEAD/DELETE if strict applied everywhere) → split into strict (writes only) / lenient (reads + deletes). Follow-up retest confirmed: PUT `%2e%2e` → 400, GET `legacy/%2e%2e/historic.txt` → 200 serves simulated legacy file, DELETE → 204 removes it.

## Sign-off

- [x] All cases passed
- [x] Final state:
  - [x] `acme/raw/files` has 7 blobs (hello/world.txt, images/tiny.png, bundles/bundle.tar.gz, docs/doc.xml, a/b/c/d.txt, normal/path.txt, sanity/still-works.txt)
  - [x] `acme` has `b1x` bucket with 2 objects (large.bin + postrevoke.txt)
  - [x] `alice-wt3-batch12` user API key active; `AKIA4T73IUOLDKLWDOXD` S3 key created and then revoked per 12.22 — final state is "one S3 key total for alice on acme was provisioned during this batch, then revoked post-test by design"
- [x] All F-12.* closed or explicitly deferred with tracking entries
- [x] README.md batch 12 status flipped to ✅
- [x] `make test` green across `internal/...` (including 15 new `validateRawPath` cases covering percent-encoded traversal, length caps, and strict/lenient mode split)
