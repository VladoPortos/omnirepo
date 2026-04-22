# Batch 12 — Raw blobs + S3 buckets

**Status:** ⬜ Not started
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

_(F-12.N)_

## Sign-off

- [ ] All cases passed
- [ ] Final state:
  - [ ] `acme/raw/files` has at least one blob
  - [ ] `acme` has `b1x` bucket with at least one object
  - [ ] `alice-s3` key active
- [ ] All F-12.* closed
- [ ] README.md batch 12 status flipped to ✅
