# Batch 03 — Profile, API keys, S3 keys, self-service

**Status:** ✅ Passed clean (2026-04-22)
**Prereqs:** Batch 02 ✅ (superadmin + alice + bob exist)
**State produced for later batches:**
- `alice` has one user-owned API key (saved in session for protocol tests)
- `bob` has one user-owned API key
- Both have a recorded login, email (if editable), and avatar seed

## Pre-flight

- [ ] Logged in as `alice`
- [ ] `alice` has `user` role (not super-admin)
- [ ] Backend log tail open

## Test cases

### 3.1 Profile page renders
- [ ] Navigate to `/profile`
- [ ] **Expected:** Personal Information, Change Password, API Keys, S3 Access Keys, My Projects (placeholder until Batch 04), Delete Account
- [ ] Login field is disabled / read-only
- [ ] Avatar is rendered (dicebear SVG, deterministic from seed)
- [ ] Console + network clean

### 3.2 Display name / email edit (if supported)
- [ ] If email/display-name editable, change it and save
- [ ] **Expected:** success toast, `/api/v1/me` returns updated values, refresh preserves them
- [ ] If not editable, skip and document

### 3.3 Avatar regeneration
- [ ] Click "Regenerate avatar" (or equivalent)
- [ ] **Expected:** new SVG rendered client-side with new seed; seed persisted on server
- [ ] Refresh → avatar still matches the new seed
- [ ] No network request for an external avatar CDN (air-gap)

### 3.4 Create user API key — happy path
- [ ] In API Keys card, click "Create API key"
- [ ] Fill display name `alice-dev`, scope `user`, expiry (leave default)
- [ ] Submit
- [ ] **Expected:** one-time modal showing full key text `omni_...`; copy button works; warning "never shown again"
- [ ] Close modal → API key row appears in table with prefix + created_at; full key not shown
- [ ] Audit log: `api-key.create` with target=alice

**Save the full key text** into this file (redact last N chars if you prefer):
```
alice user key: omr_u_PP7UxqzLjWKAHt7jDePabloKWOZd   (id=6, name=alice-dev)
bob   user key: omr_u_k20hvNOJH2HnBmVWEHoB7qvZAIdK   (id=7, name=bob-dev — created before bob's self-delete retest; re-create after Batch 04 if Batch 03 bob key is needed upstream)
```

### 3.5 API key validation
- [ ] Try creating with empty name / overly long name (>256 chars) / duplicate name
- [ ] **Expected:** appropriate 400 envelope + field-level error in dialog

### 3.6 API key auth — positive
- [ ] `curl -k -u "alice:omni_..." https://localhost:18443/api/v1/me`
- [ ] **Expected:** 200 with alice's profile
- [ ] Audit log: `auth.api_key.used` (or equivalent event)

### 3.7 API key auth — wrong key
- [ ] `curl -k -u "alice:omni_wrong" https://localhost:18443/api/v1/me`
- [ ] **Expected:** 401 envelope, no user-enumeration leak

### 3.8 Revoke API key
- [ ] Row action "Revoke" on alice's key
- [ ] **Expected:** confirm dialog → DELETE endpoint → row disappears
- [ ] Re-curl with the key → 401
- [ ] Audit log: `api-key.revoke`

### 3.9 Create second key for bob (via bob's profile in another tab)
- [ ] Log in as bob in an incognito window, go to `/profile`, create `bob-dev` key
- [ ] Save the key text for later protocol tests

### 3.10 S3 access key — create (needs a project bucket from Batch 04)
- [ ] Skip for now if no project exists; return after Batch 04
- [ ] When revisiting: open "Create S3 key" dialog, pick project `acme`, role (read / read-write)
- [ ] **Expected:** one-time modal with Access Key ID + Secret; copy buttons
- [ ] Table row appears with the AKID, project name, role
- [ ] WALKTHROUGH-FINDINGS-2 F-12 regression: combobox shows project **name**, not numeric ID

### 3.11 S3 access key — revoke
- [ ] Row action → Revoke
- [ ] Subsequent SigV4 requests with that key return `AccessDenied`

### 3.12 Delete account (danger zone)
- [ ] As `bob`, scroll to Delete Account; attempt delete
- [ ] **Expected:** confirmation dialog (typed-login or equivalent); `DELETE /api/v1/me` returns 204; session invalidated; redirect to `/login`
- [ ] Try logging back in as `bob` → fails (user deleted)
- [ ] Audit log: `user.delete` with actor=bob, target=bob

**After this case**, re-create bob via admin UI for later batches to reuse.

### 3.13 Cannot delete self if side-effects would orphan project
- [ ] If bob is the only admin of a project, deletion should either reassign or block with a clear envelope
- [ ] Document observed behavior. If destructive (orphans project), file finding.

### 3.14 Profile is tolerant of session expiry
- [ ] Expire the session (clear cookie or wait for timeout)
- [ ] Click "Save" on a profile edit
- [ ] **Expected:** redirected to login OR a clear error envelope prompts re-auth; no silent data loss

### 3.15 Console + network sweep
- [ ] Revisit every tab/card in `/profile` with `browser_console_messages`
- [ ] Zero errors, zero warnings

## Findings

See [FINDINGS.md](FINDINGS.md) for full detail on each. Short-form here:

| ID | Sev | Area | One-line | Commit(s) |
|----|-----|------|----------|-----------|
| F-03.1 | R | handleMe | `GET /me` dropped `avatar_seed` → UI avatar reverted on reload | `4aff6df` |
| F-03.2 | R | apikeys handlers | User API key create/revoke emitted no audit events | `3d68953` |
| F-03.3 | R | api-key validation | Unlimited name length (300+ bytes accepted) | `31fb799` + `be474f8` |
| F-03.4 | R | api-key validation + schema | Duplicate live names accepted; race-safe partial unique index added | `31fb799` + `be474f8` |
| F-03.5 | **B** | OptionalSessionOrAPIKey | Invalid Basic/Bearer creds → 200 null instead of 401 (credential probes invisible) | `517c23f` + `be474f8` |
| F-03.6 | R | DeleteAccountSection + handleDeleteMe | Post-delete UI stuck on /profile; orphan sessions + api_keys not cleaned | `5f27d48` + `be474f8` |

**Codex verdict:** ✅ Clean after pass — all three real-issue recommendations (empty-Bearer tightening, DB-level partial unique index, server-side cleanup on DELETE /me) adopted in `be474f8`. One noise-class observation on audit naming asymmetry (`user.api-key.*` vs `project.api-key.*` with matching `target_kind` variants) — consistent by design, operator runbook note only.

## Sign-off

- [x] All cases passed (case 3.10/3.11 deferred until after Batch 04 — no project exists yet)
- [x] All F-03.* closed
- [x] API keys for alice + bob recorded in this file for later batches
- [x] README.md batch 03 status flipped to ✅
