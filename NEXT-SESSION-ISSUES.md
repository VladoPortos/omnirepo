# Carry-forward — post-v1.0

v1.0 milestone is complete. All Phase 5 walkthrough findings, the 26-item
pre-release audit, the 15 UI-test findings, and every S3 / admin follow-up
raised during the 2026-04-17 walkthrough session are shipped. Historical
detail lives in git history (`git log --oneline` from `0481bb2` back).

---

## Open items — take-or-leave, no blockers

### Docker storage overestimate across shared blobs

`repoSizeExpr` in `internal/api/dashboard.go` walks every manifest's
`layers[]` + `config.digest`, joins to `docker_blobs.size_bytes`, and sums.
A blob referenced by N repos is fully charged to every repo (N× overcount).
This matches the operator mental model for "how much space does this repo
take from my POV," but breaks if we ever want to drive billing or per-repo
quotas from these numbers.

**Fix shape when it becomes real:** ref-count blobs at dashboard time
(divide each blob's bytes by the distinct-repo count of its manifests) or
maintain a materialised `docker_blob_refs` table kept in sync by the OCI
upload / GC paths. ~80 LOC either way.

### DEB pool-path reconstruction — exotic layouts

`resolveDebPoolPath` assumes the standard Debian pool layout
`pool/<component>/<lp>/<pkg>/<filename>` where `<lp>` is the first letter
of `<pkg>` (or `lib<x>` for the `libX` subtree). Repos that diverge (custom
components, non-letter sharding) will confuse the resolver. The handler is
already defensive — mis-resolved paths produce 404, not corruption — but
nice packages under an unusual pool layout just won't be found.

**Fix shape:** read the pool layout from the repo's `dists/<suite>/Release`
instead of inferring from filename. Defer until a real repo trips it.

### Codex rescue pass across the S3 + admin batch

The 2026-04-17 session shipped `s3_buckets.go`, the live walkthrough
harness, the dashboard/project-detail extensions, the S3 tab +
`S3BucketPage` rewrite, and `admin_gc.go` status handler — all without a
Codex rescue review (the prior Codex run hung ~1 h; feedback logged in
`~/.claude/projects/.../memory/feedback_codex_rescue_hangs.md`). Worth a
single pass if Codex behaves during a future session, time-boxed hard.

---

## Starting v1.1

Current `.planning/STATE.md` reads `milestone: v1.0, status: Milestone
complete`. To archive this milestone and open v1.1, run:

```
/gsd-complete-milestone        # archives v1.0 phase artifacts
/gsd-new-milestone             # opens v1.1 with a fresh roadmap
```

Dev harness (still useful for manual verification):

- Live server: `bin/omnirepo serve --config /tmp/omni-p1p2.yaml` on port 18080.
- Admin login: `admin` / `admin-pw-12345`.
- On-disk S3 layout: `<data_root>/s3/<bucket>/...`.
- Re-run the SigV4 walkthrough:
  ```
  OMNI_S3_ENDPOINT=http://localhost:18080 \
  OMNI_S3_BUCKET=<bucket> \
  OMNI_S3_AKID=AKIA... OMNI_S3_SECRET=... \
  go test -tags=walkthrough -count=1 -v ./test/walkthrough/...
  ```
