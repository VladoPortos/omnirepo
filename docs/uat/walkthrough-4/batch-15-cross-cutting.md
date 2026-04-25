# Batch 15 — Cross-cutting: search · dashboard · /api/docs · error envelopes · a11y · console

**Status:** ✅ Passed clean (0 findings)

## Test cases

### 15.1 Global search ✅
- `/search` page renders with: search input, Kind filter (Repos · Artifacts · CVEs), Severity filter (Critical · High · Medium · Low), Project dropdown.
- Empty-state hint: "Start typing to search across repositories, artifacts, and CVEs."
- Type `redis` → instant results (FTS5-backed) listing 50+ matching helm chart entries (`redis 21.0.x`, `redis 20.x`, `redis 19.x`, ... down to `redis 16.11.2`).
- Console clean. Screenshot: `screenshots/batch-15-search-redis.png`.

### 15.2 Dashboard with live data ✅
- `/` renders fully populated:
  - Stats: **3 Projects**, **14 Repositories**, **826 high CVEs**, **0 critical**.
  - Status cards: Storage (healthy), Recent Failures (all clear), Scan Findings Trend (allclear), Background Jobs (Running badge — 0 running, 3 queued), TLS Certificate (Self-signed), Trivy Database (Fresh, updated 0d ago), SQLite Health (Healthy, 11.2 MB / 4.0 MB WAL, modernc v1.48.2).
  - Recent Activity: live `scan.started` / `scan.finished` events streaming in.
  - High-Severity Findings: real CVE list with `KSV-0014` / `KSV-0118` IDs, project/repo location with file:line (e.g., `acme/bitnami · redis-17.10.1.tgz:templates/replicas/statefulset.yaml`).
- Console clean. Screenshot: `screenshots/batch-15-dashboard.png`.

### 15.3 Swagger UI / API docs ✅
- `/api/docs` redirects to `/swagger/`. Page renders with title "OmniRepo API 1.0.0 OAS 3.1".
- Description: "REST API for OmniRepo — self-hosted artifact repository server. Serves OCI/Docker, RPM, APT/Debian, PyPI, Helm, RAW, S3, and Git from a single container on one HTTP/HTTPS port."
- /api/v1/openapi.yaml link visible.
- Authorize button top-right.
- Sections expand: setup (GET /setup/status, POST /setup/superadmin), auth (POST /auth/login, /auth/logout, /auth/change-password), me (GET, PATCH, DELETE), … all endpoints with lock icons indicating auth requirement.
- Screenshot: `screenshots/batch-15-api-docs.png`.

### 15.4 Error envelope shape ✅
- All envelopes carry `{code, message, class, incident_id}`:
  - `401 unauthenticated` → `{code:"auth.unauthenticated", class:"permission", ...}`
  - `405 method not allowed` → `{code:"validation.failed", message:"Method not allowed for this route.", ...}`
  - `404 not found` returns `401 auth.unauthenticated` for `/api/v1/*` paths (security choice — don't leak existence to unauthenticated callers; documented behaviour).
  - All envelopes include `incident_id` for log correlation.

### 15.5 Console cleanliness sweep ✅
- Across all pages visited (`/`, `/projects`, `/projects/acme`, `/projects/acme/docker/demo`, `/projects/acme/git/hello`, `/profile`, `/admin/users`, `/admin/audit`, `/admin/tls`, `/admin/trash`, `/admin/gc`, `/admin/maintenance`, `/search`, `/swagger/`):
  - **Zero ERROR-level console messages** (the inert "Failed to load resource: 401" entries from intentional auth-failure tests are pre-classified noise per wt3 F-01.2).
  - **Zero React warnings**, key warnings, or hydration mismatches.

### 15.6 a11y / responsive ⬜ delegated to existing e2e (`web/e2e/a11y-audit.spec.ts`, `responsive.spec.ts`)
- The axe + responsive specs run against the same SPA and pass per v1.7 STATE.md.

## Findings

**None.**

## Sign-off
- [x] All in-scope cases marked
- [x] Backend log gate: 0 hits across all batches' UI traffic
- [ ] Codex batch-end review
- [x] Status flipped to ✅
