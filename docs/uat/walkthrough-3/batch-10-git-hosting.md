# Batch 10 — Git hosting (non-mirror)

**Status:** ⬜ Not started
**Prereqs:** Batch 04 ✅
**State produced for later batches:**
- `acme/git/tools` with at least one branch and a few commits
- Confirmed dual-mount paths (canonical + legacy per WALKTHROUGH-2 D-4)

## Pre-flight

- [ ] `git` CLI available
- [ ] Logged in as alice
- [ ] Server log tail open
- [ ] HTTP endpoint in use for git clients (avoid TLS cert trust friction)

## Test cases

### 10.1 Create git repo (non-mirror)
- [ ] CreateRepoDialog: type `Git`, name `tools`, mirror=false
- [ ] **Expected:** D-2 regression — CreateRepoDialog for Git in non-mirror mode shows NO mirror checkbox, NO mirror config (per 4170f65 widened form)
- [ ] Repo created; `/projects/acme/git/tools` renders empty-state with clone/push snippets

### 10.2 Empty repo behavior
- [ ] UI shows "No commits yet" or equivalent empty state
- [ ] `git ls-remote http://alice:<api-key>@localhost:18080/acme/git/tools.git` returns an empty/initial ref list (no crash)

### 10.3 Clone empty repo
- [ ] `git clone http://alice:<api-key>@localhost:18080/acme/git/tools.git /tmp/tools`
- [ ] **Expected:** clone succeeds, produces an empty working tree
- [ ] Warning "You appear to have cloned an empty repository" is acceptable (git client)

### 10.4 First push
- [ ] `cd /tmp/tools && echo "hello" > README && git add . && git commit -m "init" && git push origin main`
- [ ] **Expected:** push succeeds; UI eventually shows `main` branch, 1 commit, tree browser populated
- [ ] Audit log: `git.receive_pack.success` / `git.push` (whichever event is emitted)

### 10.5 Refs endpoint contract (WALKTHROUGH-2 F-9 + F-10 regression)
- [ ] `GET /api/v1/projects/acme/repos/git/tools/refs` → JSON with `[{ "name": "main", "sha": "...", "type": "branch" }]`
- [ ] `name` is short (`main`, not `refs/heads/main`)
- [ ] `sha` present (not `target`)
- [ ] Symbolic HEAD is filtered out (no entry with `type: "symbolic"`)
- [ ] Schema matches OpenAPI GitRef

### 10.6 Tree view
- [ ] UI shows file tree with README at root
- [ ] Click README → blob view renders content, syntax-highlighted where applicable
- [ ] Console clean

### 10.7 More commits + branches
- [ ] Add a second commit on `main`
- [ ] Create and push a branch `feature-x` with its own commit
- [ ] **Expected:** UI lists both branches; switching refs updates tree/commits
- [ ] Commit history shows correct author, message, date, sha

### 10.8 Tag
- [ ] `git tag v1.0 && git push origin v1.0`
- [ ] **Expected:** UI lists `v1.0` under Tags; tree view for the tag matches that commit

### 10.9 Blame
- [ ] `/projects/acme/git/tools/blame/main/README` (or wherever blame is routed)
- [ ] **Expected:** each line shows commit sha, author, date
- [ ] Multi-commit file: blame attributes lines to their authoring commits

### 10.10 Compare refs
- [ ] `/projects/acme/git/tools/compare/main..feature-x`
- [ ] **Expected:** diff stats and per-file patches render
- [ ] Console clean

### 10.11 Commit detail
- [ ] Click a commit row → commit detail page
- [ ] **Expected:** patch, author, date, parent commit links
- [ ] Follow parent link back up the tree — no 404s

### 10.12 Dual-mount paths (D-4 regression)
- [ ] Canonical: `git clone http://alice:<api-key>@localhost:18080/acme/git/tools.git`
- [ ] Legacy: `git clone http://alice:<api-key>@localhost:18080/git/acme/tools.git`
- [ ] **Expected:** both succeed; pushes to either path land on the same repo
- [ ] UI clone snippet shows the canonical form

### 10.13 LFS gate (new in GITMIRROR-04, applies to all git repos)
- [ ] `curl -X POST http://alice:<key>@localhost:18080/acme/git/tools.git/info/lfs/objects/batch -H 'Content-Type: application/vnd.git-lfs+json' -d '{"operation":"upload","transfers":["basic"],"objects":[{"oid":"...","size":1}]}'`
- [ ] **Expected:** 501 with structured envelope `lfs.not_supported` (per db2bbbb)
- [ ] `git lfs` commands fail politely; normal git operations still succeed

### 10.14 Permission — member vs non-member
- [ ] As `mallory` (non-member), `git clone http://mallory:<key>@localhost:18080/acme/git/tools.git`
- [ ] **Expected:** 403 from Smart HTTP; structured
- [ ] As `bob` (member), clone succeeds

### 10.15 Anonymous access with public_read
- [ ] Enable `public_read` on the repo
- [ ] `git clone http://localhost:18080/acme/git/tools.git` without creds
- [ ] **Expected:** clone succeeds
- [ ] Turn public_read off → clone fails with 401

### 10.16 Concurrent pushes
- [ ] Two shells push to different branches simultaneously
- [ ] **Expected:** both succeed; no corruption; refs endpoint reflects both after

### 10.17 Delete repo
- [ ] Delete `acme/git/tools` via settings → soft-delete; entry in Trash (Batch 14)
- [ ] `git clone` after delete → 404
- [ ] Restore from Trash (pre-test for Batch 14) and clone again — works

### 10.18 Console + network sweep
- [ ] Every git page (tree, blob, commits, blame, compare, settings, commit detail)
- [ ] Zero errors/warnings

## Findings

_(F-10.N)_

## Sign-off

- [ ] All cases passed
- [ ] Final state:
  - [ ] `acme/git/tools` exists with main + feature-x + v1.0
  - [ ] Dual-mount verified
  - [ ] LFS gate verified
- [ ] All F-10.* closed
- [ ] README.md batch 10 status flipped to ✅
