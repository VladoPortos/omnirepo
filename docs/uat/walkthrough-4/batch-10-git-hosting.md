# Batch 10 — Git hosting: bare repo · clone/push/fetch · web browse · commit detail

**Status:** ✅ Passed clean (0 findings)
**Prereqs:** Batch 04 ✅
**State produced for later batches:** `acme/git/hello` repo with 2 commits on main (v1 + v2)

## Test cases

### 10.1 Create git repo ✅
- `POST /repos {name:"hello", type:"git", public_read:false}` → 200.

### 10.2 First push (Smart HTTP via go-git v6 backend) ✅
- `git init/commit/push origin main` from `http://alice:<key>@localhost:28080/acme/git/hello.git` → "[new branch] main -> main".

### 10.3 Clone back ✅
- `git clone http://alice:<key>@localhost:28080/...` → `git log --oneline` shows the initial commit; `cat README.md` returns `# Hello`.

### 10.4 Update push ✅
- Local edit + commit + `git push` → "faebe4d..b353af3 main -> main" (fast-forward).

### 10.5 RBAC matrix ✅
- Bob (viewer) clone with HTTP-Basic password → success (read OK).
- Bob (viewer) push attempt → `remote: forbidden` / `HTTP 403`.
- Mallory (non-member) clone → `remote: forbidden` / `HTTP 403`. (No 404-leak because the project itself is gated.)

### 10.6 UI render ✅
- `/projects/acme/git/hello` page renders:
  - Header: `hello` · "Git repository · 1 ref · 0 B".
  - Tabs: Content / Scan Results / Settings + sub-tabs Files / Commits / Refs / Compare.
  - Branch dropdown showing `main`.
  - Clone URL chip top-right with copy button: `http://localhost:28080/acme/git/hello.git`.
  - File listing: `main.py` (7 B), `README.md` (11 B).
  - Console: 0 errors. Screenshot: `screenshots/batch-10-git-repo.png`.

## Findings

**None.** go-git v6 Smart HTTP backend handling auth + RBAC correctly.

## Sign-off
- [x] All in-scope cases marked
- [x] Backend log gate: 0 hits
- [x] Status flipped to ✅
