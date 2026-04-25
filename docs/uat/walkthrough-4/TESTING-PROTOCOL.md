# Testing protocol — Walkthrough #4

> Copied from walkthrough-3 protocol. Only the data-root path and ports change
> (`/tmp/omnirepo-wt4`, 28080/28443) — everything else is unchanged from the
> protocol that passed v1.4.

## 1. Server (already started by orchestrator)

```bash
export OMNIREPO_DATA_ROOT=/tmp/omnirepo-wt4
export OMNIREPO_SERVER__HTTP_PORT=28080
export OMNIREPO_SERVER__HTTPS_PORT=28443
./bin/omnirepo serve 2>&1 | tee $OMNIREPO_DATA_ROOT/server.log &
```

Health check: `curl -k https://localhost:28443/healthz`

Base URLs:
- UI: `https://localhost:28443` (HTTPS, self-signed) or `http://localhost:28080`
- Protocol clients (docker / helm / git / pip / aws / curl): `http://localhost:28080`

## 2. Playwright MCP driving

Use the Playwright MCP browser tools — these are canonical:

- `browser_navigate` — open URL
- `browser_snapshot` — accessibility tree (prefer for assertions)
- `browser_click` / `browser_fill_form` / `browser_type` / `browser_select_option`
- `browser_console_messages` — **after every flow**, paste warnings/errors into finding
- `browser_network_requests` — **after every flow**, scan for unexpected 4xx/5xx
- `browser_take_screenshot` — attach to findings, save into `screenshots/`
- `browser_wait_for` — async UI

### Console gate

ERROR / WARN / Uncaught / Unhandled / TypeError / ReferenceError / React act() warnings / key warnings / hydration mismatches → **finding** (severity scaled to symptom).

### Network gate

Any 5xx → always finding. Any unexpected 4xx (not part of the test) → finding. Any external-origin call the user didn't trigger (air-gap violation) → blocker.

### Backend log gate

`grep -E '(ERROR|panic|FATAL|level=error)' /tmp/omnirepo-wt4/server.log` after each case. Hits = finding (unless intentional, e.g. bad-password 401).

## 3. Bootstrap data (created once in Batch 01–04, reused later)

Users:
- `superadmin` / `Adm1n!Passw0rd` — created in Batch 01 setup
- `alice` / `Alice!Passw0rd123` — project admin on `acme`
- `bob` / `Bob!Passw0rd123` — viewer on `acme`, maintainer on `beta`
- `mallory` / `Mall0ry!Passw0rd` — **not** a member of any project

Projects:
- `acme` — alice admin, bob viewer (RBAC test surface)
- `beta` — alice admin, bob maintainer
- `closed` — only superadmin

Upstream credentials on `acme`:
- `dockerhub` — Docker Hub username + PAT (for OCI pulls)
- `gh` — GitHub PAT for Git mirror tests (optional; public-repo tests skip if absent)

## 4. Finding lifecycle

1. Discover during a test case (UI / console / network / backend reveals problem).
2. Record in batch file's Findings section + `FINDINGS.md`. ID `F-<batch>.<n>`.
3. Classify: B/blocker | R/real-bug | m/minor | n/noise.
4. Investigate root cause — no mitigations.
5. Fix at the source. Commit `fix(wt4): <desc> (F-XX.YY)`.
6. Codex verify per-batch.
7. Retest the original repro.
8. Mark ✅ Closed.

## 5. Codex invocation template

```
Context:
  Walkthrough #4 Batch XX — <area>.
  Just shipped: <list of commits/files>.

Please review for correctness and leakage (not style):
  1. <specific question 1>
  2. <specific question 2>

Report format:
  file:line + severity (blocker/real-issue/minor/noise) + one-line fix.

Caps:
  Under 1200 words. 15 minutes max, do not hang.
```

Invoke via `Agent(subagent_type="codex:codex-rescue", prompt=...)` per CLAUDE.md.

## 6. Screenshots

- Folder: `docs/uat/walkthrough-4/screenshots/`
- Naming: `batch-XX-<case-id>-<short>.png`
- Attach to every real-bug+ finding.

## 7. Finding entry template

```markdown
### F-<batch>.<n> <one-line title>
- **Severity:** B / R / m / n
- **Area:** <component / endpoint / page>
- **Symptom:** <what user sees / console says>
- **Repro:**
  1. ...
  2. ...
- **Console/network:** <relevant line or "clean">
- **Root cause:** file:line — explanation
- **Fix:** commit `<sha>` — one-line
- **Codex verify:** ⬜ Pending | ✅ Clean | 🟥 Rejected
- **Retest:** ⬜ Pending | ✅ Passed
- **Status:** 🟨 Open / ✅ Closed
```
