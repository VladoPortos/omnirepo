# Next-session seed — as of end of 2026-04-17 session

## Status of the previous carry-forward list

Everything from the last seed is now committed on `main`:

- **P-1** Docker scan invalid-tar on attestation manifests → fixed via
  `scan.IsScannableManifest` + handler skip + enqueue-side filter
  (commit `a2b502e`).
- **P-2** Trivy `cache_path` default misaligned with DB layout → fixed
  default to `/var/lib/omnirepo/trivy`; invariant locked by
  `TestTrivyDefaults_CachePathContainsDBSubdir` (commit `a2b502e`).
- **D-1** Unused `scan.Handler.MarkNotImplemented` → deleted.
- **Scan Results tab** was rendering the placeholder on every repo page →
  wired `useRepoScans` + new `RepoScanResults` component inside
  `RepoPageLayout`; every repo type now shows the scan list by default.
- **S-1** RPM scans were no-ops → `extractRPM` walks past the RPM headers
  and unpacks the cpio payload (handles xz + gzip) so Trivy's filesystem
  scanner sees real files. New dep: `github.com/cavaliergopher/cpio`.
- **S-2** PyPI wheels only carried their own `name==version` in the
  synthesized `requirements.txt` → the wheel's `dist-info/METADATA` is
  now parsed for `Requires-Dist:` headers and each transitive dep is
  normalized to pip syntax and appended.

All fixes have unit tests in `internal/scan/` and pass `go test ./...`.

---

## Still outstanding

### S-3 — S3 walkthrough (deferred from last round and this round)

**What's missing:** the S3 protocol surface has never been exercised end-
to-end against the live server. The F-5 work added S3 object totals to
the storage widget but that aggregation was never validated against real
S3 traffic, and none of the PUT/GET/DELETE paths have been touched this
cycle.

**Next session must do, in order:**

1. **Admin-side key provisioning.** Use `/api/v1/admin/s3/keys` (or the
   admin UI if wired) to mint an access-key / secret-key pair for a
   bucket scope. Record the creds — they are shown once.

2. **SigV4-authenticated client round-trip** using `aws-sdk-go-v2`
   pointed at `http://localhost:18080/s3/<bucket>` with
   `UsePathStyle=true` and the minted creds. Exercise at minimum:
     - `CreateBucket`
     - `PutObject` (small ≤8 KiB body + one multipart >5 MiB)
     - `ListObjectsV2`
     - `GetObject` (assert body round-trips byte-for-byte)
     - `DeleteObject`
     - `DeleteBucket`
   Capture request/response headers on one PUT to confirm SigV4
   verification is reached (look for `authorization: AWS4-HMAC-SHA256 …`
   accepted with 200/204, not 403 SignatureDoesNotMatch).

3. **Storage widget regression check.** After the PUTs, the dashboard's
   storage card must reflect the new object bytes under the bucket's
   project, and the bucket page itself should list the objects. If the
   numbers are off, F-5's aggregation needs revisiting.

4. **GC interaction smoke test.** Delete the objects, wait for the
   quiescence window (`gc.blob_quiescence_seconds`), trigger
   `POST /api/v1/admin/gc/run`, and confirm the trash + storage
   widget update correctly.

**Gotchas likely to bite:**
- `gofakes3` v1.0.0 does NOT verify SigV4 on its own — we layer that on
  top. If SigV4 rejects requests, check `internal/protocol/s3/sigv4/`
  skew + keyID lookup before blaming the SDK.
- Dev server HTTP uses port 18080 in the test harness I left at
  `/tmp/omni-p1p2.yaml`; if you recreate the data root, the admin pw is
  `admin-pw-12345`.
- `dataroot/s3/<bucket>/...` is the on-disk layout; easy to eyeball
  bucket contents without hitting the API.

**Rough expected scope:** 1 commit covering the S3 walkthrough +
whatever gets found along the way.

---

## Small leftovers, take-or-leave

- **Docker storage overestimate across shared blobs** (F-5 note from
  previous seed): storage sum still counts shared blobs fully in every
  repo that references them. Revisit only if billing / quota work
  starts using these numbers; for dashboarding, current behaviour
  matches operator mental model ("how much space does this repo take
  from my POV").
- **DEB pool-path reconstruction** (`resolveDebPoolPath`) assumes the
  standard `pool/<component>/<letter-or-lib-prefix>/<pkg>/<filename>`
  layout. Not tested against exotic repos. Low priority.
- **Codex rescue review** for this batch of fixes was explicitly skipped
  per user direction: the Codex runtime hung for ~1h on the prior
  invocation. See `~/.claude/projects/.../memory/feedback_codex_rescue_hangs.md`.
  If Codex behaves during the next session, a rescue pass across
  `materialize_pkg.go` (RPM + PyPI extraction) would be a good fit.
