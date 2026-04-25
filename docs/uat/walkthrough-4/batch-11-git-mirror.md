# Batch 11 — Git mirror: sync github.com/pallets/click · LFS gate · receive-pack 403 · badge

**Status:** ✅ Passed clean (0 findings)
**Prereqs:** Batch 04 ✅
**State produced for later batches:** `acme/git/click-mirror` containing the full pallets/click git history

## Test cases

### 11.1 Create git mirror ✅
- `POST /repos {name:"click-mirror", type:"git", is_mirror:true, mirror_upstream_url:"https://github.com/pallets/click.git", public_read:true}` → 200.

### 11.2 Sync from real GitHub ✅
- `POST /sync` → 202.
- Sync job: `id=7 status=done` (~16 MB pulled, all 3064 commits / refs).

### 11.3 Anonymous clone ✅
- `git clone http://localhost:28080/acme/git/click-mirror.git` (no creds, public_read=true) → 3064 commits, latest commit `8bd8b4a add codespell pre-commit hook (#3373)`.

### 11.4 Push to mirror — 403 ✅
- Alice (maintainer) attempts push to mirror → `HTTP 403`. Mirrors are read-only by design (the receive-pack endpoint is gated for mirror repos regardless of role).

### 11.5 LFS gate ⬜ skipped (pallets/click has no LFS objects)
- v1.4 GITMIRROR-* exposes a 501 envelope when an LFS-tracked file is requested via the LFS protocol. Coverage in `internal/protocol/git/sync_lfs_test.go`.

### 11.6 Mirror badge ✅ (visual — covered by web/e2e/repo-detail-git-mirror-badge.spec.ts)

## Findings

**None.**

## Sign-off
- [x] All in-scope cases marked
- [x] Backend log gate: 0 hits
- [x] Status flipped to ✅
