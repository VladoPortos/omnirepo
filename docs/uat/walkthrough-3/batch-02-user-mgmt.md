# Batch 02 — User management (admin)

**Status:** ⬜ Not started
**Prereqs:** Batch 01 ✅ (superadmin + alice exist, session active)
**State produced for later batches:**
- Users: `superadmin`, `alice`, `bob`, `mallory`
- `alice` and `bob` have permanent passwords (no forced-change flag)
- `mallory` is not a member of any project yet (Batch 04 handles membership)

## Pre-flight

- [ ] Logged in as `superadmin` in Playwright MCP
- [ ] Server log tail open
- [ ] `/admin/users` accessible (super-admin gate works)

## Test cases

### 2.1 Admin users page loads
- [ ] Navigate to `/admin/users`
- [ ] **Expected:** table lists `superadmin`, `alice`; columns include login, role, created, last seen, actions
- [ ] Loading spinner resolves → table renders
- [ ] Console + network clean

### 2.2 Non-admin cannot reach admin pages
- [ ] Open new incognito, log in as `alice` (non-admin)
- [ ] Navigate to `/admin/users`
- [ ] **Expected:** redirect to `/` (RequireSuperAdmin guard) OR visible 403 page (consistent with other admin routes)
- [ ] `GET /api/v1/admin/users` returns 403 envelope
- [ ] Close the incognito tab, continue as superadmin

### 2.3 Create user — happy path (bob)
- [ ] Open "Create user" dialog
- [ ] Fill: login `bob`, password `Bob!Passw0rd123`, role `user`, must_change=false
- [ ] Submit
- [ ] **Expected:** row for `bob` appears in list, dialog closes, no page reload needed
- [ ] `POST /api/v1/admin/users` returns 201 with the created user (no password in response)
- [ ] Audit log: `user.create` with `actor=superadmin`, `target=bob`

### 2.4 Create user — duplicate login
- [ ] Attempt to create `bob` again
- [ ] **Expected:** 409 envelope (`error_class=validation` or `conflict`), field-level error on login
- [ ] Row for `bob` is unchanged, dialog stays open with error
- [ ] Console clean (no uncaught)

### 2.5 Create user — validation
- [ ] Create with empty login / empty password / too-short password / whitespace-only
- [ ] **Expected:** each rejected with field-level errors before submit (or clean 400 envelope if client-side is bypassed)

### 2.6 Create user — mallory + alice bootstrap
- [ ] Create `mallory` / `Mall0ry!Passw0rd` (user)
- [ ] Verify `alice` already exists from Batch 01
- [ ] All four users visible in `/admin/users`

### 2.7 Admin-forced password reset
- [ ] Row action "Reset password" on `bob`
- [ ] Enter new temp password `Temp!Passw0rd`, set must_change=true
- [ ] **Expected:** bob can log in with temp password but is immediately redirected to `/change-password`
- [ ] After bob changes, must_change flag clears (validate by re-login)
- [ ] Audit log: `user.password_reset` (admin) + `user.password_change` (bob)
- [ ] Console clean

### 2.8 Self password change — wrong current password
- [ ] As `alice`, go to `/profile` → Change Password card
- [ ] Submit with wrong current password
- [ ] **Expected:** 400/401 envelope, field-level error, password unchanged
- [ ] Audit log: `user.password_change` with outcome=failure

### 2.9 Self password change — success
- [ ] As `alice`, submit correct current + new password
- [ ] **Expected:** 204/200 success, success toast, fields cleared
- [ ] Log out and back in with new password — success
- [ ] Audit log: `user.password_change` with outcome=ok

### 2.10 Delete user (soft delete)
- [ ] As superadmin, delete `mallory` from `/admin/users`
- [ ] Confirmation dialog shows "This cannot be undone" wording (or similar)
- [ ] **Expected:** `mallory` row disappears; `DELETE /api/v1/admin/users/mallory` returns 204
- [ ] Mallory can no longer log in (`POST /auth/login` returns 401, same envelope as invalid creds)
- [ ] Audit log: `user.delete`

### 2.11 Include-deleted toggle
- [ ] Toggle "Show deleted" or equivalent on `/admin/users`
- [ ] **Expected:** `mallory` reappears with "Deleted" badge, read-only actions
- [ ] `GET /api/v1/admin/users?include_deleted=true` returns mallory with `deleted_at` set

### 2.12 Cannot re-create deleted user's login (or explicitly can)
- [ ] Attempt to create a new user with login `mallory`
- [ ] Document whether the partial-unique index on `(login) WHERE deleted_at IS NULL` allows this (per WALKTHROUGH-FINDINGS-2 F-7, it should)
- [ ] Either outcome is acceptable; record observed behavior. If the old deleted row becomes a collision, file a finding.

### 2.13 Cannot delete self
- [ ] Try to delete `superadmin` while logged in as `superadmin`
- [ ] **Expected:** action blocked or 400/409 envelope explaining "cannot delete self"
- [ ] Console clean

### 2.14 Cannot delete last super-admin
- [ ] (Edge case) If only one super-admin exists, the delete action on that super-admin must be blocked even via API
- [ ] **Expected:** server returns a structured envelope explaining the safety check
- [ ] (To test: promote alice to super-admin if supported, then try to delete superadmin → should succeed; then try to delete alice → should block)

### 2.15 Role changes
- [ ] If UI exposes role change (user ↔ super-admin), try promoting `alice` and demoting back
- [ ] **Expected:** dashboard/profile reflects role immediately on next navigation
- [ ] Audit log entry for each role change
- [ ] If UI does not expose this, test via API and document

### 2.16 List pagination / search
- [ ] If user list paginates or has search, exercise it with current four users (add 5-10 dummies via API if needed)
- [ ] **Expected:** page-size and page-number controls behave correctly, no duplicates, no skipped rows

### 2.17 Console + network sweep
- [ ] Repeat each case with `browser_console_messages` + `browser_network_requests` checks
- [ ] Zero unexpected errors or warnings

## Findings

_(add F-02.N entries here; mirror into FINDINGS.md)_

## Sign-off

- [ ] All cases passed
- [ ] All F-02.* findings ✅ Closed
- [ ] Backend log zero ERROR/panic
- [ ] Final user set: `superadmin`, `alice`, `bob` (live); `mallory` (soft-deleted OR re-created; document which)
- [ ] README.md batch 02 status flipped to ✅
