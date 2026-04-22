# Batch 04 — Projects, members, access control, upstream credentials

**Status:** ⬜ Not started
**Prereqs:** Batch 03 ✅ (users + API keys exist)
**State produced for later batches:**
- Projects: `acme` (alice=admin, bob=member), `beta` (alice=admin), `closed` (superadmin only)
- Upstream credentials on `acme`: `dockerhub`, `gh` (optional)
- Project API key on `acme` (used in later protocol tests)

## Pre-flight

- [ ] Logged in as `superadmin` (project creation may require super-admin or a dedicated permission)
- [ ] Backend log tail open

## Test cases

### 4.1 Projects page renders
- [ ] Navigate to `/projects`
- [ ] **Expected:** empty-state card with "Create your first project" CTA
- [ ] Console + network clean

### 4.2 Create project — validation
- [ ] Open create dialog, submit empty / invalid name (spaces, uppercase, symbols, path-traversal `../`, reserved word `admin`)
- [ ] **Expected:** each rejected with field-level error or clean 400 envelope with `field=name`
- [ ] No project created

### 4.3 Create project `acme` — happy path
- [ ] Create project named `acme`, description "primary test project"
- [ ] **Expected:** redirect (or in-place update) to `/projects/acme` with empty Repos/Members/Activity/Settings tabs
- [ ] `GET /api/v1/projects` returns `acme` with `created_by=superadmin`
- [ ] Audit log: `project.create`

### 4.4 Create project `beta` and `closed`
- [ ] Same as 4.3 for each

### 4.5 Projects list shows all three
- [ ] `/projects` shows cards for `acme`, `beta`, `closed`
- [ ] Each card shows project name, description, and basic counts (0 repos, 1 member)
- [ ] Sort / filter controls (if any) work correctly

### 4.6 Project detail — tabs
- [ ] `/projects/acme` → tabs: Repos, Members, S3 Buckets (if exposed), Activity, Settings
- [ ] Each tab loads without error when clicked
- [ ] Breadcrumb: `Projects > acme`

### 4.7 Add member — alice as admin on acme
- [ ] Members tab → Add member → login `alice`, role `admin`
- [ ] **Expected:** alice appears in members list; `POST /api/v1/projects/acme/members/alice` returns 201/204
- [ ] WALKTHROUGH-FINDINGS-2 F-8 regression: opening the Add Member dialog invalidates user list cache (picker shows alice/bob/mallory)
- [ ] Audit log: `project.member.add`

### 4.8 Add member — bob as member on acme
- [ ] Add `bob` with role `member`

### 4.9 Member visibility (alice)
- [ ] Log in as alice, go to `/projects`
- [ ] **Expected:** `acme` and `beta` visible (she was added to `acme`, and is the creator?) — adjust based on actual visibility rules
- [ ] `closed` not visible to alice
- [ ] Click into `/projects/acme` → renders with Repos, Members (alice can edit?), Activity, Settings

### 4.10 Non-member access attempt
- [ ] As `mallory`, navigate to `/projects/acme`
- [ ] **Expected:** 403 envelope / "you don't have access" page (not a 404 leak)
- [ ] Direct API call `GET /api/v1/projects/acme` → 403
- [ ] Audit log: `project.access.denied` (if that event exists) or just a structured WARN

### 4.11 Remove member
- [ ] As superadmin, remove `bob` from `acme`
- [ ] Log in as bob → `/projects/acme` now 403
- [ ] Re-add bob for subsequent batches

### 4.12 Project-scoped API key
- [ ] `/projects/acme/settings` → API Keys tab (or similar)
- [ ] Create a key (scope: project)
- [ ] **Expected:** one-time modal; key carries project scope (prefix encodes project name or similar)
- [ ] Store the key for protocol tests. Label it `acme-ci`.

### 4.13 Project API key auth
- [ ] `curl -k -u "project:acme:omni_..." https://localhost:18443/api/v1/projects/acme` — 200
- [ ] Same curl against `https://localhost:18443/api/v1/projects/beta` — 403 (key is scoped to acme only)

### 4.14 Upstream credentials — add dockerhub
- [ ] `/projects/acme/settings` → Upstream Credentials → Add
- [ ] Name `dockerhub`, type `BasicAuth`, username + password (use test creds from user; if absent, use placeholder and note that live OCI tests in Batch 09 will need real creds)
- [ ] **Expected:** 201; row appears with name + type; password is not displayed
- [ ] `GET /api/v1/projects/acme/upstream-creds/dockerhub` returns 200 with redacted password
- [ ] Audit log: `upstream-cred.create`

### 4.15 Upstream credentials — edit
- [ ] Edit dockerhub — change username
- [ ] **Expected:** success; old password still stored (not cleared by an empty password field); 200
- [ ] Verify by mirror pull in Batch 09 (the creds actually work)

### 4.16 Upstream credentials — delete
- [ ] Create a throwaway credential, delete it
- [ ] **Expected:** row removed; can re-create with same name; audit log entry

### 4.17 Activity tab shows events
- [ ] `/projects/acme/activity` → recent events: project.create, member.add × 2, upstream-cred.create
- [ ] WALKTHROUGH-FINDINGS-2 F-5 regression: project activity doesn't 500 on malformed json (json_valid guard)

### 4.18 Delete project — soft delete
- [ ] `/projects/closed` → Settings → Delete project
- [ ] Confirmation dialog
- [ ] **Expected:** project disappears from `/projects`; `DELETE /api/v1/projects/closed` returns 204; entry appears in `/admin/trash` (tested fully in Batch 14)
- [ ] Dashboard project count drops by 1 (WALKTHROUGH-FINDINGS-2 F-4)
- [ ] Audit log: `project.delete`

### 4.19 Re-create deleted project — unique index on live rows
- [ ] Create a new `closed` project → **Expected:** success (partial-unique index from WALKTHROUGH-2 F-7 allows this)
- [ ] Both the trashed and live `closed` projects exist; UI distinguishes them

### 4.20 Project settings — visibility, quotas (if exposed)
- [ ] Any project-level toggles (public_read default, quotas, etc.) — exercise each
- [ ] Changes persist after reload

### 4.21 Console + network sweep
- [ ] Visit every tab, every dialog, every settings card
- [ ] Zero console errors/warnings
- [ ] Zero unexpected network failures

## Findings

_(F-04.N)_

## Sign-off

- [ ] All cases passed
- [ ] Final state:
  - [ ] `acme` project exists, alice=admin, bob=member
  - [ ] `beta` project exists, alice=admin
  - [ ] `closed` project exists (live copy), another `closed` in trash
  - [ ] `dockerhub` upstream credential configured on `acme`
  - [ ] Project API key `acme-ci` recorded in this file
- [ ] All F-04.* closed
- [ ] README.md batch 04 status flipped to ✅
