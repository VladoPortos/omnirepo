# Batch 01 — Install · setup · auth · sessions

**Status:** ✅ Passed clean (closed 2026-04-25)
**Prereqs:** Fresh data root at `/tmp/omnirepo-wt4`. Server on `28080/28443`. No super-admin yet.
**State produced for later batches:**
- `superadmin` / `Adm1n!Passw0rd` exists
- Active session cookie in Playwright MCP browser

## Pre-flight

- [x] Repo built fresh: `make build-all` (3.07s, manualChunks chunks visible)
- [x] Data root wiped + recreated
- [x] Server started on `28080/28443`, log at `/tmp/omnirepo-wt4/server.log`
- [x] `curl -k https://localhost:28443/healthz` → `{"status":"ok"}`
- [x] `curl http://localhost:28080/healthz` → `{"status":"ok"}`

## Test cases

### 1.1 Cold start — redirect to /setup ✅
- Navigate `http://localhost:28080/` → redirected to `/setup`. Form has Login/Email/Password/Confirm fields + Create button. Single `GET /api/v1/setup/status → 200`. Console clean.

### 1.2 Setup validation — weak password ✅
- Browser-side: HTML5 `minlength` blocks submit (no POST). Server-side: `POST /api/v1/setup/superadmin` with `password=123` → **HTTP 422** with envelope `{code:"validation.failed", message:"password must be at least 8 characters", class:"validation", incident_id}`. Console clean.

### 1.3 Setup validation — mismatched confirm ✅
- Submit with `Adm1n!Passw0rd` vs `Different!Pass` → client-side guard fires alert "Passwords do not match. Check the highlighted field." No POST. Console clean.

### 1.4 Setup success — create super-admin ✅
- Submit `superadmin / admin@example.com / Adm1n!Passw0rd` → `POST /setup/superadmin → 200`, redirect to `/login` with banner "Super-admin account created. Sign in to continue." Console clean.

### 1.5 /setup re-entry after super-admin exists ✅
- Navigate `/setup` after super-admin existed → graceful page "Setup complete · A super-admin account already exists. Sign in to continue." with `Go to sign in` button. Idempotent / safe.

### 1.6 Login — bad password ✅
- `superadmin / WrongPassword!` → `POST /auth/login → 401`, alert "Invalid login or password. Please try again. Check your Caps Lock or contact an administrator to reset your password." (Console "Failed to load resource: 401" is the inert browser-native message — accepted noise per wt3 F-01.2.)

### 1.7 Login — bad username ✅
- `nonexistent_user_zzz / AnyPassword123` → 401 with **identical** generic envelope. No user-enumeration leak.

### 1.8 + 1.9 Login happy path + dashboard ✅
- `superadmin / Adm1n!Passw0rd` → `POST /auth/login → 200`, redirect to `/`. Dashboard fully rendered: 4 stat cards (Projects: 0, Repos: 0, Users: 1, Scan Findings: 0), Status Summary (Storage Healthy, Recent Failures All clear, Scan Trend All clear, Background Jobs No jobs yet, TLS Self-signed, Trivy DB Not initialised, **SQLite Health Healthy modernc v1.48.2 (FTS5, JSON1)**, Storage 0 B / 95.9 GB), Recent Activity correctly listing all 4 prior audit events (`auth.login.success`, `auth.login.failure x2`, `user.created`).

### 1.10 Logout ✅
- User menu → Sign Out → `POST /auth/logout → 200`, redirect to `/login`. Session invalidated.

### 1.11 Session expiry — manual cookie clear ⬜ (skipped — covered by existing e2e `login.spec.ts`)

### 1.12 Concurrent session ⬜ (skipped — out of scope; v1 has no single-session enforcement)

### 1.13 Deep-link preserved across login ✅
- Logged-out, navigate `/admin/users` → redirect to `/login` (the `next` parameter is preserved in component state, not URL). After login → land directly on `/admin/users` (page renders, breadcrumb visible, console clean). The F-01.3 fix from wt3 is working.

## Findings

**None.** Zero blockers, zero real-bugs, zero minor, zero noise findings beyond the
pre-classified browser-native "Failed to load resource" entries (wt3 F-01.2).

## Sign-off

- [x] All in-scope test cases marked (1.11/1.12 skipped — see notes above)
- [x] All findings closed (none opened)
- [x] Backend log gate: `grep -E '(ERROR|panic|FATAL|level=error)' /tmp/omnirepo-wt4/server.log` → 0 hits across 253 lines
- [ ] Codex batch-end review (will batch with 02–05 to conserve quota; see plan)
- [x] Status flipped to ✅ Closed in this file
