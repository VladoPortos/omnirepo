# Next-session seed — as of end of 2026-04-17 S3 walkthrough session

## Status of the previous carry-forward list

Everything from the prior seed (S-3 S3 walkthrough) is now committed:

- **Finding F-S3-A — missing REST bucket-provision endpoint**: `gofakes3`
  `CreateBucket` is disabled in production (DefaultProjectID=0) and the only
  way to create a bucket was a direct SQLite INSERT (used by the conformance
  helper; not a real-user path). Added
  `POST /api/v1/projects/{name}/s3-buckets` + `GET /api/v1/projects/{name}/s3-buckets`
  in `internal/api/s3_buckets.go`, gated by `auth.ActionS3BucketWrite`
  (member-or-super-admin). Emits a new audit event
  `s3.bucket.create`. Added `internal/api/s3_buckets_test.go` (8 cases
  incl. duplicate/invalid-name/non-member/super-admin-bypass/project-scoped-list).
  Wired `S3Backend` through `api.Deps` and constructed it above `api.Mount`
  in `app.go`.
- **SigV4 round-trip (live server)**: `test/walkthrough/s3_live_test.go`
  (build-tag `walkthrough`) now exercises the real server:
    - `TestLiveS3_PutListGetDelete` — small-object round-trip, byte-exact
    - `TestLiveS3_Multipart6MiB` — 6 MiB multipart, `-N` ETag suffix, byte-exact fetch
    - `TestLiveS3_SignatureHeaderVisible` — valid SigV4 → 200; bogus SigV4 → 403
    - `TestLiveS3_CleanupMultipartObject` — housekeeping for the 6 MiB blob
  Run with `OMNI_S3_{ENDPOINT,BUCKET,AKID,SECRET}` env; otherwise the tests skip.
- **Storage aggregation check**: dashboard `used_bytes` correctly includes S3
  object bytes (9,552,860 = 3,261,404 docker img + 6,291,456 S3 multipart;
  drops back to 3,261,404 after DeleteObject). F-5's
  `SUM(o.size_bytes) JOIN s3_objects o` roll-up is accurate.
- **GC interaction**: S3 `DeleteObject` hard-unlinks synchronously (no trash
  path). `POST /api/v1/admin/gc` still works end-to-end and is a clean no-op
  for S3 — last run: `status=done, bytes_freed=0, trash_entries_deleted=0`.
- **Conformance suite**: `go test -tags=conformance ./test/conformance/s3/...`
  green in 1.5 s (10 subtests including DinD AWS-CLI copy).

---

## Outstanding findings from the walkthrough

### F-S3-B — Dashboard/project storage breakdown hides S3 buckets

**What's wrong:** the aggregate `used_bytes` in
`GET /api/v1/dashboard/storage` includes S3, but the per-item `repos` array
does not — so when the dashboard shows "9.1 MB used" and the breakdown lists
only "scan / img (docker) 3.1 MB", there's an unexplained 6 MB gap from the
user's point of view. Same gap on `/projects/{name}` Overview tab's Storage
card: it shows `3.1 MB / 1.0 GB` while the dashboard shows 9.1 MB.

**Suggested fix:** extend `storageDetailResponse.Repos` with an optional
`kind` discriminator (`repo` vs `bucket`) and have `handleStorageDetail`
emit one row per non-deleted bucket with `type="s3"`,
`size_bytes = SUM(s3_objects.size_bytes)` grouped by bucket. UI then renders
bucket rows alongside repo rows (same treatment as today).

**Scope:** ~30 LOC Go + small UI change. Low-risk, additive.

### F-S3-C — Project page's S3 tab is a stub

Visible at `/projects/{name}` → S3 tab: always renders the empty-state card
"No buckets — S3 buckets are provisioned separately via the S3 API" no matter
how many buckets the project actually owns. This was written before
`/api/v1/projects/{name}/s3-buckets` existed (which is the finding that
motivated the fix this session).

**Suggested fix:** wire the S3 tab to `useQuery(['projects', name, 'buckets'])`
against the new endpoint; render a bucket list with `Create bucket` button
calling POST. Mirror the Docker/RPM tab layouts. No per-object drill-in yet
(see F-S3-D).

**Scope:** ~80 LOC TypeScript + shadcn Form/Dialog boilerplate. Includes
Playwright verification.

### F-S3-D — No per-bucket object browser UI

Even after F-S3-C, there's no bucket detail page. Clicking a bucket should
land on `/projects/{name}/s3/{bucket}` with an object list, keyed off the
same `s3_objects` table used by storage aggregation. Supporting API:
something like `GET /api/v1/projects/{name}/s3-buckets/{bucket}/objects`.

**Scope:** bigger — ~150 LOC server + ~200 LOC UI + pagination. Consider
together with F-S3-C so the Create flow lands next to the browse flow.

### F-ADMIN-A — `GET /admin/gc/status` documented but not implemented

`internal/api/openapi.yaml` lists `/admin/gc/status` but no handler is
mounted. `curl /api/v1/admin/gc/status` returns 404. The admin UI doesn't
depend on it yet; either remove the spec entry or ship the handler (reads
`sync_jobs WHERE kind='gc' ORDER BY id DESC LIMIT 1`). Small, ~20 LOC.

---

## Small leftovers still carried forward

- **Docker storage overestimate across shared blobs** (from earlier seed):
  storage sum still counts shared blobs fully in every repo that references
  them. Revisit only if billing/quota work needs it.
- **DEB pool-path reconstruction** (`resolveDebPoolPath`) assumes the
  standard `pool/<component>/<letter-or-lib-prefix>/<pkg>/<filename>`
  layout. Not tested against exotic repos. Low priority.
- **Codex rescue review** for F-S3-A + the walkthrough tests was skipped
  because the prior Codex run hung for ~1 h
  (`~/.claude/projects/.../memory/feedback_codex_rescue_hangs.md`). If
  Codex behaves, a rescue pass across `internal/api/s3_buckets.go` and
  `test/walkthrough/s3_live_test.go` would be a good fit.

---

## Dev harness notes (unchanged, still useful)

- Live server started with
  `bin/omnirepo serve --config /tmp/omni-p1p2.yaml`, HTTP port 18080.
- Admin login: `admin` / `admin-pw-12345`.
- `dataroot/s3/<bucket>/...` is the on-disk layout — easy to eyeball
  contents without hitting the API.
- `test/walkthrough/s3_live_test.go` is the quickest way to re-run the
  SigV4 round-trip on demand; envs:
  - `OMNI_S3_ENDPOINT=http://localhost:18080`
  - `OMNI_S3_BUCKET=walkthrough-2026-04-17`
  - `OMNI_S3_AKID=...` / `OMNI_S3_SECRET=...` from the S3 keys endpoint
