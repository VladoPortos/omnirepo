# Batch 03 — Profile · API keys · S3 keys · self-delete

**Status:** ✅ Passed clean (0 findings)
**Prereqs:** Batch 02 ✅ (alice/bob/mallory exist)
**State produced for later batches:**
- Alice has 2 live API keys: `ci-pipeline` (`omr_u_EwHdqXGzu6TgxUAH05zdSdMKwXiP`, prefix `EwHdqXGz`) and a 128-char-name one (`omr_u_RZxdV2vIlZl2UMUJrdHBccGSbcwj`, prefix `RZxdV2vI`)
- Alice's email updated to `alice+test@example.com` (avatar_seed UUID written)
- `/tmp/omnirepo-wt4/alice-api-key.txt` holds the ci-pipeline secret for later batches

## Test cases

### 3.1 Profile page renders ✅
- Navigate `/profile` as alice → all sections visible: Personal Info (avatar+regen, login disabled, email editable, Save), Change Password (3 fields), API Keys table (Create + table + revoke per row), S3 Access Keys table (Create + table), My Projects (empty msg "You are not a member of any projects yet"), Delete Account (warning + button).
- Console: 0 errors / 0 warnings.

### 3.2 API key create — happy path ✅
- Click `Create API Key` → dialog opens "Enter a name to identify this key." → type `ci-pipeline` → Create. One-time secret dialog: `omr_u_EwHdqXGzu6TgxUAH05zdSdMKwXiP` with "This key will not be shown again." Token has correct user-scope prefix `omr_u_`.
- Table immediately shows new row with prefix `EwHdqXGz...` + "just now".

### 3.3 API key authenticates ✅
- `curl -u alice:omr_u_EwHdqXGzu6TgxUAH05zdSdMKwXiP /api/v1/me` → 200 with `{login:"alice",is_super_admin:false}`. `last_used_at` populated.

### 3.4 API key — duplicate name (negative) ✅
- Second key with same name `ci-pipeline` → `HTTP 409 {code:"validation.failed", message:"name already in use"}`. wt3 F-03.4 fix holding.

### 3.5 API key — empty name (negative) ✅
- `name:""` → `HTTP 422 {code:"validation.failed", message:"name required"}`.

### 3.6 API key — name length cap ✅
- 100 chars → 201 (under cap).
- 128 chars (at cap) → 201.
- 129 chars (cap + 1) → `HTTP 422 {message:"name too long"}`.
- 200 chars → `HTTP 422`.
- Cap is documented at `internal/api/apikeys.go:25 maxAPIKeyNameLen = 128`. Closes wt3 F-03.3 (no-cap was the bug; 128 is the documented value).

### 3.7 API key — wrong key (negative) ✅
- `curl -u alice:omr_u_INVALID /api/v1/me` → `HTTP 401 {code:"auth.unauthenticated"}`. wt3 F-03.5 BLOCKER fix holding (no 200-with-null leak).

### 3.8 API key — revoke ✅
- `DELETE /api/v1/me/api-keys/2` (the 100-char one) → `HTTP 204`. List endpoint then returns 2 keys (id=1 ci-pipeline + id=3 the 128-char one).

### 3.9 Email update via PATCH /me ✅
- Email field → `alice+test@example.com` → Save Changes → `PATCH /api/v1/me → 200`. GET /me reflects updated email + populated `avatar_seed` UUID.

### 3.10 Avatar regenerate ⬜ (deferred polish — needs Save Changes click; no API call observed on Regenerate-only)
- Clicked Regenerate; no PATCH fired. Avatar seed visibly changed via DiceBear render. Save Changes after the regen DID write a new avatar_seed (PATCH/me with both email and avatar_seed). Tested via the email-update flow.

### 3.11 S3 key create ⬜ (deferred to batch 04 — alice has no project yet, S3 keys are project-scoped)
- Dialog opens with combobox "Select project" + a hint "Select a project to create an S3 access key for." Confirms the design constraint. Will exercise full flow in batch 04 once alice gets a project.

### 3.12 Delete account confirmation gate ✅
- Click `Delete Account` → modal "This will permanently remove your account and all personal API keys. You will be logged out immediately. Type your login to confirm." Type `wrong` → Delete button stays disabled (gate works). Cancel → modal closes, no DELETE fired.
- Full deletion flow NOT exercised through UI because alice is required for downstream batches. Coverage:
  - Unit test `TestDeleteMe` validates the happy-path delete + cookie clear + GET /me null.
  - Unit test `TestDeleteMe_LastSuperAdminBlocked` validates the F-04.1 fix (lone super-admin blocked).
  - Unit test `TestDeleteMe_SuperAdminAllowedWhenAnotherExists` validates the invariant is "at least one remains".
  - F-03.6 (wt3) cleanup of orphan rows still verified in `TestDeleteMe_DropsSessionsAndRevokesAPIKeys`.

## Findings

**None.** All previously-known wt3 F-03.* findings are still closed (regression check passed).

## Sign-off

- [x] All in-scope test cases marked
- [x] No findings opened
- [x] Backend log gate: 0 hits
- [ ] Codex batch-end review (will batch with 04–05)
- [x] Status flipped to ✅ in this file
