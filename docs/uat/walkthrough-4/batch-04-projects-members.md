# Batch 04 — Projects · members · RBAC · upstream creds

**Status:** ✅ Passed clean (0 findings)
**Prereqs:** Batch 03 ✅
**State produced for later batches:**
- Projects: `acme` (id=1), `beta` (id=2), `closed` (id=3)
- Memberships: alice maintainer on acme + beta; bob viewer on acme, maintainer on beta; mallory has no project membership
- Repos: `acme/raw/demo` (id=2, alice-created), `beta/raw/vroom` (id=1, bob-as-maintainer-created)
- Upstream creds on acme: `dockerhub` (id=1, kind=docker, host=registry-1.docker.io), `pypi.org` (id=2, kind=pypi, host=pypi.org)

## Test cases

### 4.1 Create projects via API ✅
- `POST /api/v1/projects {name,description_md}` for acme/beta/closed → all 200. (NB: not `/admin/projects` — the route is `/api/v1/projects`.)

### 4.2 Add members via API ✅
- `POST /api/v1/projects/{name}/members/{login}` with `{role:"maintainer"}` for alice + bob → 200.
- Initial attempt with `{role:"admin"}` → `HTTP 422 {message:"must be 'maintainer' or 'viewer'"}`. Confirms there is NO project-level admin role; `is_super_admin` (global) handles that scope.

### 4.3 RBAC matrix ✅
| Actor | Project | Action | Result |
|---|---|---|---|
| Bob (viewer) | acme | POST /repos | **403** `auth.forbidden` "You do not have permission to do that." |
| Bob (maintainer) | beta | POST /repos | **200** (created `vroom`) |
| Bob (viewer) | acme | DELETE /repos/raw/demo | **403** |
| Bob (viewer) | acme | GET /repos/raw/demo | **200** (read OK) |
| Mallory (non-member) | acme | POST /repos | **403** `auth.not_a_project_member` "You are not a member of this project." |
| Mallory | closed | GET /projects/closed | **403** "not a project member" |
| Alice (maintainer) | acme | POST /repos | **200** (created `demo`) |

All envelopes correctly classified (`auth.forbidden` vs `auth.not_a_project_member`). v1.5 RBAC working — viewer can read, cannot mutate; non-member sees the same 403 envelope shape.

### 4.4 Upstream creds — schema validation ✅
- `host` required → 422 if missing.
- `kind` must be one of `docker`/`rpm`/`apt`/`pypi`/`helm` → 422 on `docker_basic`, `http_token`. Discovered the OpenAPI/spec `kind` values are protocol names, not auth-scheme names.

### 4.5 Upstream creds — happy path ✅
- `POST /projects/acme/upstream-creds {name:"dockerhub", kind:"docker", host:"registry-1.docker.io", username, password}` → `HTTP 201` with response body `{id,host,kind,username,created_at,updated_at}` — **no `password` field in response** (correctly redacted).

### 4.6 Upstream creds — RBAC ✅
- Bob (viewer) `POST /projects/acme/upstream-creds` → `HTTP 403 {code:"auth.forbidden", message:"not_a_maintainer"}`.
- Bob (viewer) `GET /projects/acme/upstream-creds` → `HTTP 403` (viewer can't even list creds — design choice, creds are sensitive metadata).

### 4.7 Upstream creds — duplicate ✅
- Re-creating `dockerhub` cred while it still existed → `HTTP 409`.

### 4.8 Project detail UI renders for member ✅
- Alice navigates `/projects/acme` → page renders with:
  - Tabs: Overview / Docker / RPM / APT / PyPI / Helm / Git / **RAW (1)** / S3 — note the `(1)` badge from the demo repo.
  - Members card: alice (Maintainer badge + role combobox), bob (Viewer badge + role combobox), per-row delete buttons, top-right Add Member button.
  - Project API Keys section: empty + Mint Token button + helper text "tokens start with `omr_p_`".
  - Project Activity log: `repo.created repo/acme/raw/demo`, `member.added project/acme` (×2), `project.created project/acme`.
  - Storage card: `0 B`, 1 repository.
  - Top-right Delete Project button (red).
- Console: 0 errors / 0 warnings. Screenshot: `screenshots/batch-04-acme-detail.png`.

## Findings

**None.** All RBAC paths return correct envelopes. v1.5 RBAC system is shipping clean.

## Sign-off

- [x] All in-scope test cases marked
- [x] No findings opened
- [x] Backend log gate: 0 hits
- [ ] Codex batch-end review (will batch with 05)
- [x] Status flipped to ✅ in this file
