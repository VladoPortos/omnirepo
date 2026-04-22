# Batch 10 — Git hosting (non-mirror)

**Status:** ✅ Passed clean (after fixes)
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

### F-10.0 — Doc typo in batch-10 test text (docs-only, acknowledged)
- **Symptom:** Test 10.1 reads "CreateRepoDialog for Git in non-mirror mode shows NO mirror checkbox, NO mirror config (per 4170f65 widened form)". Contradicts the commit it cites — 4170f65 deliberately widened the dialog so Git is in MIRROR_PROTOCOLS.
- **Actual invariant:** Mirror checkbox IS rendered for Git; mirror-specific fields (URL/creds/filters) only appear when ticked.
- **Resolution:** Doc-only note, no code change.

### F-10.1 — http.request slog always logged `actor_id=""` (real-issue)
- **Symptom:** Every `http.request` log line carried `actor_id=""` even on fully authenticated calls, making the structured log useless for audit tracing.
- **Root cause:** `internal/httpx/middleware_logger.go:27` hardcoded `slog.String("actor_id", "")` with a stale "Phase 2 auth middleware fills it in" comment from Phase 1. No auth middleware ever filled it.
- **Fix:** `246a262` + context plumbing. Introduced `auth.LoginBox` (mutable per-request holder) + `httpx.LoginBoxSeeder` (injection callback to break the auth → httpx cycle). `StructuredLogger` seeds a box on ctx; `auth.WithActor` auto-populates it; logger reads it after `next.ServeHTTP`.
- **Retest:** ✅ server.log now shows `"actor_id":"alice"` / `"superadmin"` / `""` for anonymous.
- **Codex verdict:** noise (Q1) — no lifetime/race bug found in production paths.

### F-10.2 — Git item_count badge counted symbolic HEAD (real-issue)
- **Symptom:** UI header showed "2 refs" for a repo with 1 branch; "4 refs" with 2 branches + 1 tag. `/refs` endpoint already filtered symbolic rows, creating a visible inconsistency.
- **Root cause:** `internal/api/projects_full.go:72` counted all rows in `git_refs`, which includes the internal symbolic HEAD row populated by WalkAndReplace.
- **Fix:** `246a262` + Codex follow-up `a11a736` — changed SQL expression to `type IN ('branch','tag')` (future-proofed per Codex suggestion over initial `type <> 'symbolic'`).
- **Retest:** ✅ UI header shows "3 refs" (feature-x, main, v1.0).

### F-10.5 — git_browse.go handlers violated OpenAPI + TS contract (blocker)
- **Symptom:** Files table always rendered Size="--", clicking a file did nothing, Commits tab crashed with `Cannot read properties of undefined (reading 'split')`. The entire Git browse UI was effectively broken.
- **Root cause:** Six handlers in `internal/api/git_browse.go` emitted wire shapes that did not match the schemas declared in `openapi.yaml` (which the TS client was typed against). Example: `{name, type:'file'|'dir', size}` vs. `{name, path, type:'blob'|'tree'|'commit', size, sha}`. Schema drift across handleGitTree, handleGitBlob, handleGitCommits, handleGitCommit, handleGitBlame, handleGitCompare.
- **Fix:** `246a262` — rewrote each handler to match canonical shape. Added `diffTrees`/`changePatch`/`blobAsAddPatch`/`lineCount`/`parentSHAs` helpers. Commit detail + compare now render as `GitDiff` (stats + per-file unified patches). Accept both `..` and `...` in compare spec. Codex follow-up `a11a736` — reject 4-dot spec + refs that start/end with `.`.
- **Retest:** ✅
  - Files table now shows "14 B" / "6 B" instead of "--"
  - Clicking README opens blob view with rendered content + line numbers (1, 2)
  - Commits tab: zero console errors, commits with author/date/SHA render
  - Commit detail: side-by-side diff with `+1 / -0` stats
- **Codex verdict:** noise (Q4) on main path; minor (4-dot spec) applied in `a11a736`.

### F-10.7 — public_read=true did not enable anonymous git clone (real-issue + info-leak)
- **Symptom:** Clone without credentials against a public_read=true git repo returned 401 instead of streaming the pack.
- **Root cause A — middleware:** `internal/protocol/git/middleware.go:138-141` rejected requests lacking an Actor in ctx with a flat 401 before `auth.Can` could see them. Target didn't carry `PublicRead`.
- **Root cause B — policy:** `internal/auth/policy.go:209` only allowed `ActionRepoRead` in the anonymous short-circuit; `ActionGitRepoRead` used by git Smart-HTTP was not covered.
- **Fix:** `246a262` — new `AnonymousGitRead` middleware attaches `Actor{Kind:ActorKindAnonymous}` when repo.PublicRead + read action + no Authorization header; chain reordered (`ResolveRepoFromURL → AnonymousGitRead → skipIfActor(BasicOrAPIKey) → resolveMembership → RequireGitPermission`). Policy widened to accept `ActionGitRepoRead` + target.PublicRead.
- **Codex follow-up (real-issue, info leak):** ResolveRepoFromURL returning 404 for missing repos while private repos returned 401 let anonymous callers enumerate repo names via status-code sniffing. Fixed in `a11a736` — new `writeMissingOrChallenge` returns 401 + Basic challenge to anonymous callers (same as private-repo response), preserving the real 404 only for authenticated callers.
- **Retest:** ✅
  - Anonymous on public_read=true repo → 200, clone succeeds
  - Anonymous on private repo → 401 + WWW-Authenticate: Basic
  - Anonymous on missing repo → 401 + WWW-Authenticate: Basic (indistinguishable from private — no enumeration oracle)
  - Authenticated user on missing repo → 404 (entitled to know)
  - Authenticated clone still works on both public and private repos
- **Codex verdict:** real-issue (info leak) flagged + fixed; noise (Q2, Q3) on write-path leakage and middleware ordering.

## Sign-off

- [x] All cases passed
- [x] Final state:
  - [x] `acme/git/tools` exists with main + feature-x + v1.0 (restored from trash after 10.17)
  - [x] Dual-mount verified (canonical `/acme/git/tools.git` + legacy `/git/acme/tools.git`)
  - [x] LFS gate verified (501 `lfs.not_supported` + structured envelope)
  - [x] public_read anonymous clone gated via repo.public_read flag
  - [x] actor_id populated in http.request logs
- [x] All F-10.* closed (F-10.0 docs-only; F-10.1/.2/.5/.7 fixed in `246a262` + `a11a736`)
- [x] README.md batch 10 status flipped to ✅
- [x] Codex verify pass complete — 1 real-issue + 2 minor fixes applied
