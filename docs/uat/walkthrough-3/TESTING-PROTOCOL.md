# Testing protocol

Shared setup/teardown and conventions for every batch.

## 1. Start a clean server

```bash
# From repo root
make build-all

# Pick a data root you won't collide with
export OMNIREPO_DATA_ROOT=/tmp/omnirepo-wt3
rm -rf $OMNIREPO_DATA_ROOT && mkdir -p $OMNIREPO_DATA_ROOT

# Unique ports to avoid stepping on other omnirepo instances
export OMNIREPO_SERVER__HTTP_PORT=18080
export OMNIREPO_SERVER__HTTPS_PORT=18443

# Run with logs captured
./bin/omnirepo serve 2>&1 | tee $OMNIREPO_DATA_ROOT/server.log &
```

Health check:
```bash
curl -k https://localhost:18443/healthz
```

Base URL for all UI testing: `https://localhost:18443`
Base URL for protocol clients (docker/helm/git/curl): `http://localhost:18080`

**Why both:** UI defaults to HTTPS (self-signed). Protocol clients use HTTP
to keep the test environment simple (cert trust is exercised explicitly in
Batch 14 TLS cases, not implicitly everywhere).

## 2. Reset between batches (optional)

Most batches build on prior state. Only wipe if the batch explicitly says
"fresh data root" (typically Batch 01 and any TLS cert tests).

To wipe:
```bash
kill %1 2>/dev/null
rm -rf $OMNIREPO_DATA_ROOT && mkdir -p $OMNIREPO_DATA_ROOT
./bin/omnirepo serve 2>&1 | tee $OMNIREPO_DATA_ROOT/server.log &
```

## 3. Playwright MCP driving

Use the Playwright MCP browser tools — these are the canonical ones:

- `browser_navigate` — open URL
- `browser_snapshot` — accessibility tree snapshot (prefer over screenshots for assertions)
- `browser_click` / `browser_fill_form` / `browser_type` / `browser_select_option`
- `browser_console_messages` — **run after every flow**, paste warnings/errors into finding
- `browser_network_requests` — **run after every flow**, scan for unexpected 4xx/5xx
- `browser_take_screenshot` — attach to findings, save to `docs/uat/walkthrough-3/screenshots/`
- `browser_wait_for` — for async UI (sync job progress pills, etc.)

### Console cleanliness gate

After **every** test case in a batch, call `browser_console_messages` and
scan for:
- **ERROR** level → automatic finding, any severity
- **WARN** level → finding (at least minor), unless known/expected
- `Uncaught`, `Unhandled`, `TypeError`, `ReferenceError` anywhere in message body → finding
- React "act() warning", `key` warnings, hydration mismatches → finding (minor+)

### Network cleanliness gate

After every flow, call `browser_network_requests` and scan for:
- Any `5xx` → always a finding
- Any unexpected `4xx` (not explicitly exercised) → finding
- Any request to external origins the user didn't trigger (air-gap violation) → blocker

## 4. Backend log gate

In parallel, `grep -E '(ERROR|panic|FATAL|level=error)' $OMNIREPO_DATA_ROOT/server.log`
after each test case. Any hit is a finding (unless the case intentionally
induces an error envelope — e.g. bad-password login, in which case the
structured WARN is expected).

## 5. Bootstrap data

**Recommended shared seed** (create once in Batch 01, reuse in later batches):

Users (created via admin UI / API):
- `superadmin` / `Adm1n!Passw0rd` — created during setup (Batch 01)
- `alice` / `Alice!Passw0rd123` — project admin
- `bob` / `Bob!Passw0rd123` — project member
- `mallory` / `Mall0ry!Passw0rd` — **not** a member of `acme` project (for access-control tests)

Projects:
- `acme` — primary test project, `alice` is admin, `bob` is member
- `beta` — second project for cross-project tests, only `alice` is admin
- `closed` — only `superadmin` is member, used to prove 403 for `mallory`

Upstream credentials stored on `acme`:
- `dockerhub` — Docker Hub username + password (or PAT) for OCI pulls
- `gh` — GitHub PAT for private Git mirror test (optional)

## 6. Finding lifecycle

1. **Discover:** while running a test case, console/network/UI/backend reveals a problem.
2. **Record:** add an entry to the batch file's **Findings** section **and** `FINDINGS.md`. Use ID `F-<batch#>.<n>`.
3. **Classify:**
   - **B / blocker** — prevents normal use (crash, data loss, 5xx on happy path, auth bypass)
   - **R / real-bug** — wrong behavior visible to user, not just cosmetic
   - **m / minor** — polish, labels, layout
   - **n / noise** — stray console message, inert spec violation
4. **Investigate:** read the code paths, reproduce reliably, identify root cause. No "mitigations" — find the source.
5. **Fix:** change the code at the root. Commit with a message that references the finding ID: `fix(wt3): <desc> (F-05.3)`.
6. **Codex verify:** after a batch's fixes are applied, invoke the Codex rescue agent:

   ```
   Agent(subagent_type="codex:codex-rescue", prompt="<context + files touched + ask for file:line + severity + one-line fix, <1200 words, 15 min max>")
   ```

   Apply valid findings; discard noise. Record verdict in finding.
7. **Retest:** re-run the original repro via Playwright MCP. Confirm:
   - Original symptom gone
   - No new console errors/warnings
   - No new network failures
   - No new backend errors
8. **Close:** mark `✅ Passed` in the finding. When **all** findings in a batch are closed and retested, mark the batch ✅ in README.md.

## 7. Codex invocation template

```
Context:
  Walkthrough #3 Batch XX — <area>.
  Just shipped: <list of commits or files touched>.

Please review for correctness and leakage (not style):
  1. <specific question 1>
  2. <specific question 2>
  3. <specific question 3>

Open code-review findings to re-classify (from WALKTHROUGH-FINDINGS-2.md and prior):
  <F-X.Y: one-line desc>

Report format:
  file:line + severity (blocker/real-issue/minor/noise) + one-line fix.

Caps:
  Under 1200 words.
  15 minutes max, do not hang.
```

## 8. Screenshots and artifacts

- **Screenshots folder:** `docs/uat/walkthrough-3/screenshots/`
- **Naming:** `batch-XX-<case-id>-<short>.png` (e.g. `batch-05-5.3-tag-delete.png`)
- **When to attach:** every real-bug+ finding. Nice-to-have for minor/noise.
- **Do not commit:** failed-run traces, Playwright test-results. Those live in
  `web/playwright-report/` and `web/test-results/` and are gitignored.

## 9. Finding entry template

Copy into the batch file's Findings section:

```markdown
### F-<batch>.<n> <one-line title>
- **Severity:** B / R / m / n
- **Area:** <component / endpoint / page>
- **Symptom:** <what the user sees or what the console says>
- **Repro:**
  1. <step>
  2. <step>
  3. <observed>
- **Console/network:** <paste relevant line or "clean">
- **Root cause:** <after investigation, link file:line>
- **Fix:** commit `<sha>` — <one-line>
- **Codex verify:** ⬜ Pending | ✅ Clean | 🟥 Rejected
- **Retest:** ⬜ Pending | ✅ Passed
- **Status:** 🟨 Open / ✅ Closed
```

## 10. When to ask the user for help

Per the CLAUDE.md global rule: drive everything via Playwright first. Only
ask the user when:

- A test genuinely needs human input (e.g. a real GitHub PAT, a private
  Docker Hub account, a real TLS cert)
- An external service (Docker Hub, pypi.org, charts.bitnami.com,
  github.com) is unreachable from this host and no local stub exists
- You've exhausted Playwright-based verification and the UI still won't
  cooperate

State explicitly what you tried and why it failed before asking.
