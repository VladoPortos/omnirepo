# OmniRepo fresh-install walkthrough — findings

Run date: 2026-04-16. Fresh DB at `/tmp/omnirepo-data-fresh`. No `bootstrap.json` —
first user created entirely through the new UI setup flow.

## What was tested

Single operator, single project `platform`, one repo per supported protocol
(`rpm`, `deb`, `pypi`, `docker`, `helm`, `git`, `raw`). Real artifacts pulled
from public mirrors, uploaded with the new user's API key:

| protocol | artifact                                      | path                                                           | upload result |
|----------|-----------------------------------------------|----------------------------------------------------------------|---------------|
| RPM      | `zlib-1.2.11-17.el8.x86_64.rpm` (vault.centos) | `PUT /platform/rpm/centos-packages/packages/…rpm`              | **201** |
| DEB      | `hello_2.10-3_amd64.deb` (Debian pool)        | `PUT /platform/deb/debian-main/pool/main/h/hello/…deb`         | **201** |
| PyPI     | `six-1.16.0-py2.py3-none-any.whl`             | `POST /platform/pypi/wheels/legacy/` (twine style)             | **200** |
| Helm     | `nginx-15.14.0.tgz` (Bitnami)                 | `PUT /platform/helm/charts/charts/nginx-15.14.0.tgz`           | **201** |
| RAW      | Linux kernel `README`                         | `PUT /platform/raw/files/kernel-readme.txt`                    | **201** |
| Git      | `git-scm.com` clone → push main               | `git push http://…/git/platform/code.git`                      | **ok** |
| Docker   | `alpine:3.19` via `crane copy`                | `/v2/platform/docker/images/manifests/3.19`                    | **blocked** — see F-1 |
| S3       | _not exercised — needs SigV4_                 | —                                                              | skipped |

All uploads auto-enqueued a scan row; all failed (Trivy DB not loaded — see
observations).

---

## Setup flow itself (the new feature)

Worked end-to-end on a completely empty DB:

1. First visit to `/` → `SetupGuard` redirects to `/setup`.
2. Form accepts login / email / password + confirm. Client-side match + ≥ 8 char.
3. `POST /api/v1/setup/superadmin` → 200, `user.created` audit event with outcome
   `first_run_superadmin`.
4. Redirect to `/login`. Sign-in works, dashboard loads with the one seeded user.
5. Re-hitting `/setup` after setup-done correctly bounces to `/login` (since
   `needs_setup=false`).
6. Second `POST /setup/superadmin` returns 409 — endpoint is naturally one-shot.

All 7 unit tests for the setup endpoints pass (`go test -run TestSetup ./internal/api/`).
Full `./internal/api/`, `./internal/audit/`, `./internal/app/` suites stay green.

---

## Findings (severity ordered)

### F-1 — Docker registry WWW-Authenticate challenge missing `scope=` (FUNCTIONAL — blocks docker push)

`crane copy alpine:3.19 localhost:8080/platform/docker/images:3.19` fails with
`unexpected status code 401`. Server emits

```
Www-Authenticate: Bearer realm="http://localhost:8080/v2/token",service="omnirepo"
```

per `internal/protocol/oci/token_verify.go:40`, but the Docker Registry v2 auth
spec requires the challenge to include a `scope="repository:<name>:<actions>"`
parameter so clients know what scope to request from the realm. Without it,
crane and the Docker daemon do not follow the Bearer flow — they give up on
the 401. The handler's test (`oci/handler_test.go:218`) actively locks the
incorrect format in.

Evidence: `/v2/token?service=omnirepo&scope=repository:platform/docker/images:push,pull`
with Basic auth issues a valid JWT and GETting the manifest with that bearer
token succeeds. The server-side logic works — only the challenge advertisement
is incomplete.

Fix (future): append `,scope="repository:<project>/<type>/<repo>:<actions>"` to
the challenge header; update the test regex to match. Compute actions from the
request method (GET/HEAD → `pull`, PUT/POST/PATCH/DELETE → `push,pull`).

### F-2 — Per-repo scans endpoint declared in OpenAPI is not mounted (FUNCTIONAL)

`openapi.yaml` lists `/projects/{name}/repos/{type}/{repo}/scans` but only
`/projects/{name}/repos/{type}/{repo}/artifacts/{id}/scans` is mounted
(`internal/api/scans.go:86`). Requests to the former fall through to the SPA
handler and return HTTP 200 + `index.html`. That is deeply confusing — a
404 JSON body would at least signal the real problem.

Two defects here:
1. Either mount the repo-level scans list, or drop it from the OpenAPI.
2. Unknown `/api/v1/*` paths should 404 JSON, never return the SPA. The SPA
   fallback needs to be scoped to non-`/api/` paths only.

### F-3 — Uploaded RPM package not visible in repo UI (FUNCTIONAL)

Uploaded `zlib-1.2.11-17.el8.x86_64.rpm` returned 201 and is persisted (row 1
in `rpm_packages`). The repo detail page (`/projects/platform/rpm/centos-packages`
→ Content tab) shows "No RPM packages found. Upload an .rpm file to get started."

Same pattern likely for DEB (verified DB contents on `hello` — but also no
deb_packages rows … wait, deb_packages shows the schema has different columns;
DEB `hello` upload returned 201 and signing keys regenerated, so the DEB path
worked at the protocol layer. Need to verify why the front-end list is empty.

Likely cause: the query hook for repo content is calling an endpoint that
returns an empty list (paginated 0 items), or calling the wrong endpoint and
silently absorbing a 401/404. Worth checking the Network tab on that page.

### F-4 — Scan dispatcher has no handler for rpm/deb/pypi/helm kinds (FUNCTIONAL)

Scans table after uploads:

```
repo_id=1 (rpm)   status=pending attempts=3 last_error=no handler for kind "rpm"
repo_id=2 (deb)   status=pending attempts=3 last_error=no handler for kind "deb"
repo_id=3 (pypi)  status=pending attempts=3 last_error=no handler for kind "pypi"
repo_id=5 (helm)  status=pending attempts=3 last_error=no handler for kind "helm"
repo_id=7 (raw)   status=pending attempts=3 last_error=trivy --skip-db-update cannot be specified on the first run
```

Only the RAW path even reached Trivy. The dispatcher has no handler registered
for rpm/deb/pypi/helm artifact kinds — every scan for those retries to the 3-
attempt ceiling and stays stuck at `pending` (should probably terminate in
`failed` after exhausting retries, too — another minor bug).

### F-5 — Storage widget reports 0 B despite real uploads (FUNCTIONAL / UX)

Dashboard storage widget: "0 B / 95.9 GB, 0% used". Per-repo bars all show 0 B.
Project page: "7 repositories, 0 B total". Actual on-disk uploads ~213 KB
across 5 repos.

Either the per-repo size counter is not updated on successful PUT/POST, or the
dashboard aggregator reads from a stale source. Makes it impossible for an
operator to see what an instance is actually storing.

### F-6 — Post-setup success banner doesn't render on `/login` (UX, minor)

The `FirstRunSetupPage` navigates with `{ state: { setupDone: true } }` and
`LoginPage` reads `location.state.setupDone` to render a green "Super-admin
account created. Sign in to continue." banner. The banner does not appear.

Likely cause: the `SetupGuard` wrapping `/login` returns children synchronously
on the first render (needs_setup=false thanks to the mutation's optimistic
`setQueryData`), but something in React Router's state-preservation between
`useNavigate('/login', {state})` and `useLocation()` drops the state. Worth
adding a regression test with the e2e suite.

### F-7 — Duplicate breadcrumb on repo detail page (UX, known)

`/projects/platform/rpm/centos-packages` shows two breadcrumb rows:

```
Dashboard > Projects > platform > rpm > centos-packages
Projects  > platform > RPM > centos-packages
```

Memory note `feedback_phase5_ui_fixes_round2` already flagged this; still not
fixed on this build.

### F-8 — Arbitrary 413 / content-type inconsistencies (INFORMATIONAL)

The PyPI legacy upload accepted `filetype=bdist_wheel` with a `content=@…`
multipart part using `filename=six-1.16.0-py2.py3-none-any.whl` and returned
`{"status":"ok","filename":"six-1.16.0-py2.py3-none-any.whl"}`. That works
but the naming — `content` for the file field — differs from the
twine / PEP 694 field name `content`. Worth checking against a stock
`twine upload` run at some point.

---

## What worked cleanly (worth calling out)

- New `/setup` endpoints + guard are tight. Clean round-trip from "empty DB"
  to "logged in super-admin" in three clicks. Audit event captured.
- API-key creation via `/api/v1/me/api-keys` is frictionless.
- RPM, DEB, Helm, RAW protocol PUTs all returned 201 immediately. Helm/RPM
  metadata regen events fired (visible in activity feed).
- PyPI legacy upload works — `twine`-style clients should be compatible.
- Git push/clone via go-git v6 backend works transparently with Basic auth +
  api-key token. Refs synced, `git.refs.synced` audit event.
- DEB & RPM auto-generated signing keys on repo creation
  (`signing_key.created` audit events).
- Dashboard, Projects page, Project detail, Project tabs all render on real
  data without console errors.
- Setup status endpoint is a clean, cheap probe — `{"needs_setup":true/false}`.

---

## Non-bugs (expected behavior, just for the record)

- **Trivy DB not loaded**: scans fail with `--skip-db-update cannot be specified
  on the first run`. Per spec §10 + §19, Trivy DB must be uploaded by an admin
  via `/admin/trivy/db` before scans can run. Air-gap design choice.
- **S3 excluded from repo-create dropdown**: `ProjectDetailPage.tsx:58` filters
  it out by design — buckets auto-create via the S3 protocol's `PUT /s3/<bucket>`.
- **Scan findings = 0**: no scans have completed, so the dashboard card is
  accurate.

---

## Suggested next iteration (ranked)

1. **F-2 + F-3** are the most user-facing: `/api/*` falling to SPA on missing
   routes, and uploaded content not visible in UI. Both break the "does it
   work?" user test on the happy path.
2. **F-1** is the biggest functional gap — docker push is a headline protocol.
3. **F-4** needs the scan dispatcher wired up for rpm/deb/pypi/helm; until
   then the whole scan feature only works for RAW, which is a corner case.
4. **F-5** storage aggregation — probably a counter-update hook missing from
   the protocol PUT paths.
5. **F-6 / F-7** are cosmetic; roll into the next UI polish pass.

No blocker found for the fresh-install + setup-wizard flow itself. The
superuser bootstrap feature behaves exactly as designed.
