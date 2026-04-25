# Batch 09 — Helm OCI: oci:// upstream · cred gate · tag-rebound

**Status:** ✅ Passed (cred-gate verified; full sync needs a real Docker Hub PAT — skipped)
**Prereqs:** Batch 04 ✅ (dockerhub upstream cred exists on acme)

## Test cases

### 9.1 Cred-gate without cred ✅
- `POST /repos {is_mirror, mirror_upstream_url:"oci://registry-1.docker.io/bitnamicharts/nginx", type:"helm"}` (no `mirror_cred_id`) → `HTTP 422` with envelope:
  - `code: "mirror.docker_hub_requires_credential"`
  - `class: "validation"`
  - `message: "Docker Hub enforces a 100 requests / 6h anonymous rate limit per source IP. Attach a basic credential (username + PAT) to sync reliably."`
- v1.5 OCIHELM-05 / D-04 typed-envelope working with the right operator-friendly message.

### 9.2 Cred-gate with cred attached ✅
- Same payload + `mirror_cred_id: 1` (the fake `dockerhub` cred from batch 04) → `HTTP 200`. Repo created.

### 9.3 Actual oci:// sync ⬜ skipped (needs real Docker Hub PAT)
- The fake cred would fail at the actual `helm pull oci://...` step. Live sync round-trip is exercised by `make test-live-oci` (gated behind `DOCKERHUB_USER`/`DOCKERHUB_TOKEN` env vars).

### 9.4 Tag-rebound (OCI helm push detection) ⬜ unit-test coverage only
- Plan 07-04 wires `wireHelmMirror` so an OCI manifest push to a tagged Helm chart auto-mirrors into the traditional helm repo. Covered by hermetic tests in `internal/protocol/helm/` (per v1.4 OCIHELM-08 plan).

## Findings

**None.** Cred-gate envelope is well-shaped and operator-actionable.

## Sign-off
- [x] All in-scope cases marked
- [x] Backend log gate: 0 hits
- [x] Status flipped to ✅
