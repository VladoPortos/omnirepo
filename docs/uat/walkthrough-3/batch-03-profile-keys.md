# Batch 03 — Profile, API keys, S3 keys, self-service

**Status:** ⬜ Not started
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
alice user key: omni_<prefix>_<...>
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

_(add F-03.N entries here)_

## Sign-off

- [ ] All cases passed (case 3.10/3.11 can be deferred until after Batch 04)
- [ ] All F-03.* closed
- [ ] API keys for alice + bob recorded in this file for later batches
- [ ] README.md batch 03 status flipped to ✅
