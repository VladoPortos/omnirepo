# Batch 02 — User CRUD · password change · admin force-reset · last-admin guard

**Status:** ✅ Passed (2 fixes shipped, 0 open)
**Prereqs:** Batch 01 ✅ (`superadmin / Adm1n!Passw0rd` exists)
**State produced for later batches:**
- `alice / Alice!Passw0rd123` (id=2, regular user)
- `bob / Bob!Passw0rd123` (id=3, regular user)
- `mallory / Mall0ry!Passw0rd` (re-created mid-batch as id=5; original mallory id=4 deleted to exercise admin DELETE)
- All 3 created with `must_change_password=true` then promoted via admin PATCH (or, in alice's case, via the UI must-change-password flow)
- `/tmp/omnirepo-wt4/creds.json` contains the cleartext passwords (test data only)

## Test cases

### 2.1 Create user via admin UI ✅
- Navigated `/admin/users` as superadmin → clicked Create User → filled `alice / alice@example.com` → Create User submit
- One-time password dialog shown (`oQRkD2SLyq4sQGFg` for the first run, `XPilWu7k3uO6MElP` after the F-04.1-fix data-root reset)
- Console clean. New row visible in users table. `must_change_password=true` confirmed via API.

### 2.2 Create user via API (mass spawn for state) ✅
- Bob and mallory created via `POST /api/v1/admin/users` (same handler — UI flow is just a wrapper). All three users returned `{login, one_time_password}`.

### 2.3 First-login OTP redirect to /change-password ✅
- Alice logged in via Playwright with OTP → URL became `/change-password`. Form fields: Current Password / New Password / Confirm New Password. Banner: "Your password must be changed before you can continue." Console clean.

### 2.4 Self-service change-password — happy path ✅
- Alice filled current=OTP / new=`Alice!Passw0rd123` / confirm=match → `POST /auth/change-password → 200`. Redirected to dashboard (`/`). Console clean. Subsequent login with new password succeeds.

### 2.5 Self-service change-password — wrong current ✅
- API call with `current="WrongCurrent" / new="Whatever123!"` → `HTTP 401` envelope `{code:"auth.unauthenticated", message:"wrong current password"}`. Audit row recorded with `outcome=wrong_password` (per existing F-02.2 fix). Original password still valid.

### 2.6 Self-service change-password — weak new password 🟥 → ✅ (F-04.2 fixed)
- Pre-fix: `current=Alice!Passw0rd123 / new="abc"` → **HTTP 200**, password changed to "abc". Setup rejects this same string with 422 — inconsistent.
- Fix shipped (commit `6bd799c`): factored `auth.PasswordValid()` + `PasswordMinLen=8` constant; called from setup, change-password, and admin PATCH.
- Post-fix re-test: same payload → `HTTP 422 {code:"validation.failed", message:"password must be at least 8 characters"}`. Alice's prior password still valid.

### 2.7 Admin force-reset ✅
- `PATCH /api/v1/admin/users/bob` with `{new_password:"Bob!Passw0rd123", must_change_password:false}` → `HTTP 200`, response `{"changes":{"must_change_password":false,"password":"reset"}}`.
- Bob can immediately log in with the new password. All prior bob sessions are invalidated (HI-01 — handler line `admin_users_full.go:220`).

### 2.8 Admin force-reset — weak password 🟥 → ✅ (F-04.2 same fix)
- Pre-fix: `PATCH /admin/users/bob` with `{new_password:"abc"}` → 200 OK. Same root cause as 2.6.
- Post-fix: same payload → `HTTP 422` envelope `password must be at least 8 characters`.

### 2.9 Admin DELETE — non-self user ✅
- `DELETE /api/v1/admin/users/mallory` (as superadmin) → `HTTP 200 {"status":"ok"}`. mallory soft-deleted; subsequent login attempt fails. (Re-created mid-batch for downstream batches.)

### 2.10 Admin DELETE — self ✅
- `DELETE /api/v1/admin/users/superadmin` (as superadmin, target=self) → `HTTP 409` envelope `cannot delete yourself — use the self-service delete in your profile`. Existing safety check at `admin_phase1.go:677-679` working.

### 2.11 Self-delete — last super-admin guard 🟥 → ✅ (F-04.1 BLOCKER fixed)
- Pre-fix: `DELETE /api/v1/me` as the only super-admin → **HTTP 200**, super-admin soft-deleted, instance bricked (no operator can administer; can't even re-login). This was a regression of wt3 F-02.3 which only added the guard to the admin-delete path; the self-delete path called `Users.Delete` directly without the last-super-admin check.
- Fix shipped (commit `a19a512`): `handleDeleteMe` now calls `Users.DeleteEnforceLastSuperAdmin`; on `ErrLastSuperAdmin` returns `HTTP 409` + envelope `cannot delete your account — promote another user to super-admin first`.
- Regression tests added: `TestDeleteMe_LastSuperAdminBlocked` and `TestDeleteMe_SuperAdminAllowedWhenAnotherExists`.
- Post-fix re-test on a fresh data root: lone super-admin self-delete → `HTTP 409`; session still alive; `GET /me` still returns the live super-admin row.

### 2.12 Audit visibility ✅
- Recent Activity card on dashboard correctly shows: `auth.login.success superadmin`, `auth.login.failure nonexistent_user_zzz`, `auth.login.failure superadmin`, `user.created superadmin`, `admin.integrity_check.completed` — all in correct chronological order (per wt3 F-04.2 RFC3339 nano fix still working).

## Findings

### F-04.1 Self-delete of last super-admin succeeds → instance bricked
- **Severity:** **B / blocker** (regression of wt3 F-02.3)
- **Area:** `internal/api/admin_phase1.go:593-619 handleDeleteMe`
- **Symptom:** `DELETE /api/v1/me` as the only super-admin returns 200 and soft-deletes the row. After the call, no operator can log in or reach `/admin/*` — the air-gapped instance is unrecoverable without wiping the data root.
- **Repro:** Fresh server, single super-admin. `curl -X DELETE -b cookie.txt http://localhost:28080/api/v1/me` → 200; subsequent `curl -X POST .../auth/login` with same creds → 401 "Invalid login or password."
- **Root cause:** `handleDeleteMe` called `d.Users.Delete(ctx, a.ID)` (raw soft-delete). The sibling `handleDeleteUser` calls `Users.DeleteEnforceLastSuperAdmin` which has the invariant baked in. The two paths drifted.
- **Fix:** commit `a19a512` — `handleDeleteMe` now uses `DeleteEnforceLastSuperAdmin`; returns 409 + ErrConflict on `ErrLastSuperAdmin`.
- **Codex verify:** ⬜ Pending (will batch with 03–05 review)
- **Retest:** ✅ Passed (lone super-admin → 409 + envelope; session live; user intact)
- **Status:** ✅ Closed

### F-04.2 Change-password + admin PATCH accept weak passwords (setup rejects)
- **Severity:** R / real-bug (inconsistent password policy across entry points)
- **Area:** `internal/api/admin_phase1.go:474 handleChangePassword`, `internal/api/admin_users_full.go:203 patch.NewPassword`, `internal/api/setup.go:85` (the only path with the check)
- **Symptom:** `POST /auth/change-password` with `new:"abc"` → 200 OK. `PATCH /admin/users/{login}` with `new_password:"abc"` → 200 OK. Setup rejects the same string with 422 "password must be at least 8 characters".
- **Root cause:** Inline `len < 8` check in setup only; siblings either had no check or only `req.New == ""`.
- **Fix:** commit `6bd799c` — factored `auth.PasswordValid()` + `auth.PasswordMinLen` constant; wired into all 3 sites. Validation runs BEFORE wrong-current verification.
- **Tests:** `auth.TestPasswordValid` (table covering 0/1/3/7/8/realistic/64-char), `api.TestChangePassword_WeakNew`. Updated `api.TestChangePassword_WrongCurrent` to use a long-enough new password (it was previously passing 1-char "x" which now short-circuits).
- **Codex verify:** ⬜ Pending
- **Retest:** ✅ All 3 sites return 422 + envelope; alice's password unchanged after rejected weak update.
- **Status:** ✅ Closed

## Sign-off

- [x] All in-scope test cases marked
- [x] All findings closed (2 fixes shipped, 0 open)
- [x] `go test ./...` green (32 packages)
- [x] Backend log gate: 0 hits across batch
- [ ] Codex batch-end review (will batch with 03–05)
- [x] Status flipped to ✅ in this file
