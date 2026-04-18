# OmniRepo 1.0 E2E — Test Findings

**Status:** complete — 2026-04-18 session, all 8 phases + teardown run.
**Scope of this document:** self-sufficient work order. A fresh Claude
session, starting with nothing but this file + the codebase, should be
able to pick up any single finding and fix it without rediscovering
context.

Every finding below carries: **Evidence**, **Root cause** (usually a
`file:line`), **Reproduction**, **Fix sketch**. Severity legend:
`blocker` (1.0 ship-stopper), `real-issue` (pre-1.0 fix),
`minor`/`note` (polish / docs).

---

## 0 · Running-system context (as of this writing)

The test container and its populated volume are intentionally left up,
so the following is still live and reproducible. Teardown removed ONLY
the host apt sources rewrite — the container and data are intact.

### Running instance

- Container: `omnirepo-e2e` (image `omnirepo:e2e`, built from commit
  `cee63ee`).
- Ports: HTTP `:8080`, HTTPS `:8443` (self-signed cert, CN=omnirepo,
  fingerprint `sha256:8ECDBAA8…AD1F`).
- Data root (host side): `/tmp/omnirepo-e2e/data` — **do not rm** while
  debugging; `/tmp/omnirepo-e2e/data/db/omnirepo.sqlite` is the
  metadata DB.
- Trivy DB baked-in at `/opt/trivy-db/` (inside the container); copied
  to `/var/lib/omnirepo/trivy/db/` on first boot.
- Credentials:
  - admin login `admin` / password `omnirepo-e2e-pw-1!` (stored at
    `/tmp/omnirepo-e2e/secrets/admin-pw`).
  - API key `omr_u_KlS28YDFKsP7lPlaMY14T8Db06CH` (stored at
    `/tmp/omnirepo-e2e/secrets/api-key`).
  - `/api/v1/...` auth: `Authorization: Bearer <api-key>` (see F-T8).
  - Protocol auth (`/<project>/rpm/...`, `/<project>/deb/...`):
    `Authorization: Basic admin:<api-key>`.
- All four repos have `public_read=true` so anonymous GET on protocol
  endpoints works.

### Populated content

```
docker_manifests : 16 rows (one per image, all under e2e/docker/docker)
docker_blobs     : 115 rows
docker_tags      : 16 rows
rpm_packages     : 815 rows (Oracle Linux 9 BaseOS, newest-only)
deb_packages     : 4850 rows (Ubuntu jammy/jammy-updates/jammy-security main)
apt_suites       : 9 rows (3 suites × {amd64, all} + duplicates guarded)
scans            : 833 rows, all status=done
```

### Logs worth keeping

```
/tmp/omnirepo-e2e/logs/
  01-docker-build.log        image build stdout
  01-boot.log                container first-boot log
  02-push-summary.log        docker push OK/FAIL tally
  02-push-<image>.log        one per image, push transcript
  03-upload.log              rpm bulk PUT transcript
  03-upload-errors.log       empty (good)
  04-apt-mirror.log          apt-mirror stdout (truncated, run hit disk cap)
  04-upload.log              deb bulk PUT transcript
  04-upload-errors.log       empty
  06-tcpdump.pcap            outbound traffic during apt install
  06-tcpdump-docker.pcap     outbound traffic during docker pull
```

### Quick teardown (when you're done)

```bash
docker stop omnirepo-e2e && docker rm omnirepo-e2e
docker rmi omnirepo:e2e
sudo rm -rf /tmp/omnirepo-e2e   # or keep for forensics
```

---

## 1 · Environment findings (non-OmniRepo)

### F-T1 · Stale `omnirepo-dev` process shadowed the container's ports
**Phase:** 1 · **Severity:** note (environment, not OmniRepo)

A leftover `/tmp/omnirepo-dev` from a previous `make dev`/Codex
session was listening on `:8080`/`:8443`; Docker silently failed to
publish the container's ports so every request hit the stale dev
server instead of the fresh container. The UI even acted "correct"
because the dev instance had its own data volume.

**Mitigation (product, not test):** the container entrypoint should
refuse to start if it can't exclusively bind its ports, or the
`make dev` target should hand over to the container cleanly.

---

## 2 · Test-plan / docs findings

### F-T2 · `test.md` upload URLs don't match current routes
**Phase:** 2, 3, 4 · **Severity:** note (docs)

- Phase 2 `${REG}/e2e/docker/${IMG}` produces `/v2/e2e/docker/<image>`
  — the OCI router treats segment-3 as the repoName, so unless a
  dedicated repo exists per image the push 404s. Real shape is
  4-segment: `${REG}/e2e/docker/docker/${IMG}` (see F-T11 for the
  UI-side copy of the same bug).
- Phase 2 API key creation uses `POST /api/v1/profile/api-keys`.
  Real route is `POST /api/v1/me/api-keys`.
- Phase 3 upload uses
  `PUT /api/v1/projects/e2e/repos/rpm/oracle/artifacts/<file>`. Real:
  `PUT /<project>/rpm/<repo>/packages/<filename>` (no `/api/v1/`
  prefix, mounted at root). See
  `internal/protocol/rpm/handler.go:133`.
- Phase 4 upload similarly — real route is
  `PUT /<project>/deb/<repo>/pool/*?suite=X&component=main` (see
  `internal/protocol/deb/handler.go:137`). Suites must be declared
  first via `PATCH /<project>/deb/<repo>/suites` (see
  `internal/protocol/deb/delete.go:136`).

**Fix:** update `test.md` §3 with the real routes. Cross-reference
F-T11 and F-T8 once those are fixed so the plan stays aligned.

### F-T4 · `test.md` Phase 2 image list has two bad tags
**Phase:** 2 · **Severity:** note (docs)

- `haproxy:3-alpine` does not exist on Docker Hub; use
  `haproxy:3.1-alpine`.
- `prometheus:v2.54.1` is not in `library/`; use
  `prom/prometheus:v2.54.1`.

### F-T5 · Phase 3 forecast "~3,000 RPMs" is stale
**Phase:** 3 · **Severity:** note (docs)

`dnf reposync --repoid=ol9_baseos_latest --newest-only` actually yields
~815 RPMs / ~774 MB today. Enough to stress the ingest path — just
update the forecast in `test.md`.

---

## 3 · Server / protocol findings (the real bugs)

### F-T3 · OCI push rejects blobs > 64 MiB
**Phase:** 2 · **Severity:** real-issue

**Evidence.** Four images failed Phase 2 with:
```
error from registry: provided length did not match content length -
final chunk exceeds 67108864 bytes
```
Failures: `postgres:16-alpine`, `mariadb:11`, `mysql:8.4`,
`golang:1.23-alpine`. All have a single layer > 64 MiB.

**Root cause.** `internal/protocol/oci/handler.go:44` declares
`ChunkMaxBytes int64` with default 64 MiB at `handler.go:150`
(`chunk = 64 << 20`). The limit is enforced on the final PUT at
`internal/protocol/oci/blobs.go:364` (and again at `blobs.go:300` for
intermediate PATCH chunks). The value is never sourced from
`config.yaml` — `internal/config/config.go` has no `oci.chunk_max_bytes`
key; only `UploadSessionTTLSeconds` exists for OCI. Docker clients
that fall back to single-PUT (no resumable chunking) therefore hard-
cap at 64 MiB layer size.

**Reproduction.**
```bash
docker pull mariadb:11
docker tag  mariadb:11 localhost:8080/e2e/docker/docker/mariadb:11
docker push localhost:8080/e2e/docker/docker/mariadb:11   # fails
```

**Fix sketch.** Smallest useful fix (preserves current safety):

1. Add a config entry:
   - `internal/config/config.go`: add `OCI.ChunkMaxBytes int64` under
     the existing `OCI` struct; default 512 MiB (8×) or make it
     unbounded when 0.
   - `internal/config/defaults.go`: set the default; document unit.
   - `internal/app/app.go` (around the OCI handler construction —
     search for `oci.Deps{`): wire `cfg.OCI.ChunkMaxBytes` into
     `oci.Deps.ChunkMaxBytes`.
2. Add regen tests for the new config path (see
   `internal/protocol/oci/blobs_test.go:157` for the current in-test
   override pattern).
3. Follow-on (separate PR): implement spec-compliant PATCH-then-PUT
   chunked upload so the server cap only bounds per-chunk memory, not
   total blob size.

**Verification after fix.**
```bash
# Push mariadb:11 — should succeed.
docker push localhost:8080/e2e/docker/docker/mariadb:11
```

### F-T6 · Packages.gz `Filename:` doesn't match stored pool path — **BLOCKER**
**Phase:** 4, 6 · **Severity:** blocker

**Evidence.** `nano` is stored on disk at
`/var/lib/omnirepo/repos/e2e/deb/ubuntu/pool/main/n/nano/nano_6.2-1ubuntu0.1_amd64.deb`
(the canonical Debian source-package layout). But Packages.gz serves:
```
Filename: pool/n/nano/nano_6.2-1ubuntu0.1_amd64.deb
```
Note the missing `main/` component prefix. apt fetches that path and
gets a 404 because the file isn't there.

**Root cause.**
1. `internal/protocol/deb/put.go:163` stores only the basename
   (`Filename: filename`) in the `deb_packages.filename` column. The
   actual pool path the client PUT to is **discarded** after the file
   hits disk.
2. `internal/protocol/deb/regen.go:219` synthesises the pool path at
   regen time:
   ```go
   poolPath := fmt.Sprintf("pool/%s/%s/%s",
       componentPrefix(p.Package),   // "n"  (or "lib?"  for lib* pkgs)
       p.Package,                     // "nano"
       p.Filename)                    // basename
   ```
   — ignoring the component and source-package parts. `componentPrefix`
   is defined at `regen.go:352`.

Schema confirmation (`.schema deb_packages`):
```
filename TEXT NOT NULL    -- basename only, no pool path
(no storage_pool_path or any column that captures where it actually lives)
```

**Reproduction.**
```bash
# On the populated instance:
curl -sk https://localhost:8443/e2e/deb/ubuntu/dists/jammy/main/binary-amd64/Packages \
  | awk '/^Package: nano$/,/^$/{if (/^Filename:/) print}'
# Filename: pool/n/nano/nano_6.2-1ubuntu0.1_amd64.deb

ls /tmp/omnirepo-e2e/data/repos/e2e/deb/ubuntu/pool/n/nano/
# ls: no such dir — file is at pool/main/n/nano/ instead.

curl -sI http://localhost:8080/e2e/deb/ubuntu/pool/n/nano/nano_6.2-1ubuntu0.1_amd64.deb
# 404 Not Found
```

**Fix sketch (option 1, recommended).**
1. **Schema migration** — add `storage_pool_path TEXT NOT NULL
   DEFAULT ''` to `deb_packages`:
   - New migration file under `internal/metadata/migrations/`, numbered
     after the current last one (see existing files in that dir).
   - Backfill: for each row, compute the legacy
     `pool/<prefix>/<pkg>/<filename>` as the best-effort initial value
     (what current regen produces); new uploads overwrite it.
2. **put.go:138** — after `storageKeyForPool` returns the storage key,
   strip the project/repo prefix to get the pool-relative path:
   `poolPath := strings.TrimPrefix(storageKey, project+"/deb/"+repo+"/")`
   and store it:
   ```go
   StoragePoolPath: poolPath,   // e.g. "pool/main/n/nano/nano_…deb"
   ```
3. **regen.go:219** — stop synthesising `poolPath`. Use the stored
   column:
   ```go
   out = append(out, PackagesEntry{
       Control:  ctrl,
       Filename: p.StoragePoolPath,   // <-- real path
       Size:     p.SizeBytes,
       SHA256:   strings.TrimPrefix(p.Digest, "sha256:"),
   })
   ```
4. Add a unit test covering: `PUT .../pool/main/libz/libzstd/zstd_…deb`
   → `Packages.gz` contains `Filename: pool/main/libz/libzstd/…`.

**Fix sketch (option 2, workaround-only — do NOT ship).** Leave the
generator alone, fix the GET handler (`internal/protocol/deb/get.go:92`)
to look up the real storage path from `deb_packages` when the literal
path on disk misses. Downside: breaks if the pool layout differs from
what the handler can reconstruct.

**Workaround in this test run.** Hardlinked every
`pool/main/<prefix>/<src>/<file>.deb` → `pool/<prefix>/<binary>/<file>.deb`
so the generator's fiction matched reality. Then `apt install nano`
worked. Do NOT ship the hardlink trick.

**Verification after fix.**
```bash
# After re-ingesting the jammy mirror:
sudo apt-get update
sudo apt-get install -y nano zstd
# Both succeed, pulled from localhost:8080, tcpdump sees zero outbound.
```

### F-T7 · GET `/pool/<…>.deb` doesn't URL-decode `%2B`
**Phase:** 4, 6 · **Severity:** real-issue

**Evidence.**
```
GET /e2e/deb/ubuntu/pool/z/zstd/zstd_1.4.8+dfsg-3build1_amd64.deb    → 200
GET /e2e/deb/ubuntu/pool/z/zstd/zstd_1.4.8%2Bdfsg-3build1_amd64.deb  → 404
```
`apt` always percent-encodes `+` when deriving the download URL from
the Filename field. Common package-version suffixes (`+dfsg`,
`+deb12u1`, `+really1.2.3`, `~rc1`) therefore can't be fetched through
OmniRepo even if F-T6 is fixed.

**Root cause.** `internal/protocol/deb/handler.go:217` reads the chi
wildcard raw:
```go
rest := chi.URLParam(r, "*")
return resolved{project: proj, repo: rr, rest: rest}, true
```
chi preserves percent-encoding in `{*}`. Then
`internal/protocol/deb/get.go:73` does
`validatePoolSubpath("pool/" + res.rest)` — `%2B` stays literal, the
stat misses, handler returns 404.

**Fix.** URL-decode once, in `deb/handler.go`'s `resolveRepo` right
after the `chi.URLParam` line:
```go
rest := chi.URLParam(r, "*")
if dec, err := url.PathUnescape(rest); err == nil {
    rest = dec
}
```
Add the same decode in `internal/protocol/rpm/handler.go` wherever the
filename is parsed (RPM versions can contain `+` and `~` too — check
`rpm/get.go` and `rpm/put.go`). Regression test: `%2B`, `%7E`, plain
`+`, plain `~`.

### F-T8 · `/api/v1/...` does not accept Basic auth with an API key
**Phase:** 2, 3, 4 · **Severity:** real-issue

**Evidence.** Protocol endpoints accept `curl -u admin:${KEY}`
(BasicOrAPIKey middleware). Management API does not — returns
`auth.unauthenticated` unless you switch to
`-H "Authorization: Bearer ${KEY}"`.

**Root cause.** `internal/auth/middleware/session_or_apikey.go:31`
only checks `Authorization: Bearer`. Meanwhile
`internal/auth/middleware/basic_or_apikey.go` already contains the
exact code that lifts an API key out of Basic's password field
(regex check at the start of the handler, around the lines that
reference `auth.APIKeyRegex.MatchString(pw)`).

**Fix.** Before the Bearer check in
`session_or_apikey.go:SessionOrAPIKey`, try Basic:
```go
if login, pw, ok := r.BasicAuth(); ok && auth.APIKeyRegex.MatchString(pw) {
    if actor, authed := authenticateAPIKey(r.Context(), d, pw); authed {
        next.ServeHTTP(w, r.WithContext(auth.WithActor(r.Context(), actor)))
        return
    }
    writeJSON401(w, r); return
}
// existing bearer/cookie paths below…
```
Test `/api/v1/me` with both Basic and Bearer; they should yield the
same actor.

---

## 4 · UI findings

### F-T9 · `/setup` still renders after the DB has a super-admin
**Phase:** 1 · **Severity:** minor

On subsequent boots `/setup` loads the form and the POST returns 409.
Harmless; just polish. Swap the form for a "Setup complete — sign in"
screen when `/api/v1/setup/status` returns `{needs_setup: false}`.
Code: `web/src/pages/SetupPage.tsx:69` already guards on
`!status.needs_setup`; replace the short-circuit with the
"already done" screen.

### F-T10 · Docker repo Content tab shows "No artifacts yet" despite 16 manifests — **BLOCKER**
**Phase:** post-run UI walkthrough · **Severity:** blocker

**Evidence.** `/projects/e2e/docker/docker` header says `Docker
repository · 477.2 MB`. The Content tab renders the empty-state
onboarding card. `docker_manifests`/`docker_blobs`/`docker_tags`
clearly have data.

**Root cause.**
`internal/api/repo_content.go:141-147` explicitly returns empty for
`docker` (and `git`):
```go
case "git", "docker":
    // Git and Docker have their own dedicated listing surfaces (git
    // tree/refs under /projects/.../git/..., docker tags under /v2).
    // Return an empty list so the UI doesn't error, but the tabs there
    // should link to the protocol-specific views instead.
    entries = []RepoContentEntry{}
```
So `GET /api/v1/projects/e2e/repos/docker/docker/content` returns
`[]` by design, and the Docker repo page currently uses this endpoint
anyway (or at least, there's no alternative wired in).

**Fix sketch.**
1. Add a dedicated `listDockerContent(repoID, limit, offset)` that
   joins `docker_tags`, `docker_manifests`, and `docker_blobs` to
   produce rows of the form:
   ```
   { id, name: "<image>:<tag>", size_bytes: <layers sum>,
     uploaded_at, scan_severity, extra: { digest, media_type, image, tag } }
   ```
   group rows by (image, tag) so each tag is one row.
2. Drop the `case "git", "docker":` short-circuit at
   `repo_content.go:141` — `docker` gets a real query; `git` can keep
   returning `[]` for now (git repos enumerate refs via their own
   surface).
3. Frontend: `web/src/pages/repo/DockerRepoPage.tsx` currently has its
   own tag-list logic (see `DockerRepoPage.tsx:201` where it uses
   `hostname` + repo + tag). Decide: either serve the unified
   `/content` shape and remove the bespoke logic, or keep
   `DockerRepoPage` calling its own endpoint AND fix that endpoint to
   not render the empty-state when rows exist.

**Verification after fix.**
```bash
curl -sk -H "Authorization: Bearer $KEY" \
  https://localhost:8443/api/v1/projects/e2e/repos/docker/docker/content | jq 'length'
# should be 16 (one per manifest/tag)
```

### F-T11 · Docker CLI snippets use the 3-segment URL (which 404s)
**Phase:** post-run UI walkthrough · **Severity:** real-issue

**Evidence.** `web/src/lib/snippets.ts:66-70`:
```ts
cmd: `docker pull ${host}/${project}/${repo}/<image>:<tag>`,
cmd: `docker push ${host}/${project}/${repo}/<image>:<tag>`,
```
The SPA passes `repo` = the docker repo name (e.g. `docker`); so the
rendered snippet is `localhost:8080/e2e/docker/<image>:<tag>`. Pushing
to that URL returns `NAME_UNKNOWN` (see F-T2 investigation): the OCI
router at `internal/protocol/oci/blobs.go:123` only accepts
4-segment paths `/v2/{project}/{type}/{repo}/{image}` or the
3-segment `/v2/{project}/{type}/{repo}` where the repo name IS the
image name.

Test confirms: `snippets.test.ts:29`/`:30` asserts the *current* wrong
shape, so the test needs updating too.

**Fix.**
1. `web/src/lib/snippets.ts:66` / `:70` — emit the 4-segment URL:
   ```ts
   cmd: `docker pull ${host}/${project}/${repo.type}/${repo.name}/<image>:<tag>`,
   cmd: `docker push ${host}/${project}/${repo.type}/${repo.name}/<image>:<tag>`,
   ```
   (the current `repo` variable is just the name; wire in the type too
   — see how `DockerRepoPage.tsx:201` already builds
   `${hostname}/${projectName}/${repo.name}:${row.tag}`, missing the
   `repo.type` + `<image>` structure).
2. Update `web/src/lib/__tests__/snippets.test.ts` accordingly.
3. (Optional, bigger) consider teaching the OCI router to also accept
   the 3-segment shape by auto-creating the `<image>` namespace inside
   the repo. Not required, but matches "registry-1.docker.io"
   ergonomics most users expect.

### F-T12 · Breadcrumb `Dashboard ▸ Projects ▸ e2e ▸ Docker ▸ docker` — the "Docker" link 404s
**Phase:** post-run UI walkthrough · **Severity:** real-issue

**Evidence.** On a repo page, the second-to-last crumb links to
`/projects/<name>/<type>` (e.g. `/projects/e2e/docker`). No such
route exists in `web/src/App.tsx:282-288`:
```ts
{ path: 'projects', element: <ProjectsPage /> },
{ path: 'projects/:name', element: <ProjectDetailPage /> },
{ path: 'projects/:name/s3/:bucket', element: <S3BucketPage /> },
{ path: 'projects/:name/:type/:repo', element: <RepoDetailRouter /> },
// no 'projects/:name/:type' route
```
The request falls through to `{ path: '*', element: <NotFoundPage /> }`
at `App.tsx:354`, which renders **outside** the app shell (bare
`<NotFoundPage />`, no sidebar/breadcrumb). So the user sees a
layoutless 404 from within the app — very jarring.

**Root cause.** `web/src/components/layout/Breadcrumbs.tsx:82-88`
renders every intermediate segment as a `<Link to={path} />` without
checking whether that path has a route.

**Fix (pick one).**
1. **Skip the type segment in breadcrumbs** (lowest effort). In
   `Breadcrumbs.tsx`, detect the repo URL pattern
   (`projects/:name/:type/:repo`) and render the type crumb as
   `<BreadcrumbPage>` (non-link text), not `<BreadcrumbLink>`. Keeps
   the URL visible, drops the broken navigation.
2. **Ship a `projects/:name/:type` page** that lists all repos of that
   type in the project. Natural page. Requires a new route + a small
   component that filters `useProject(name).repos` by type.
3. **Both**: ship the page AND make the crumb link valid. Best UX.

Also fix the chrome-less 404: make `NotFoundPage` render inside the
same `AppShell` wrapper used for authenticated routes when the URL is
"auth-ish" (anything under `/projects/...`, `/admin/...`, `/profile`).
Right now `App.tsx:354` places the catch-all *outside* the
authenticated-layout children array so it bypasses `AppShell`.

### F-T13 · Trivy DB widget says "Age unknown (baked-in)" right after the Docker image was built
**Phase:** 1 · **Severity:** real-issue (UX)

**Evidence.** The widget at `web/src/pages/DashboardPage.tsx:981`
renders `Age unknown (baked-in)` when the backend returns
`age_hours: -1`. The backend returns `-1` for baked-in DBs at
`internal/api/admin_trivy.go:124`.

But Trivy itself writes a `metadata.json` next to the DB file. In our
container it reads (right now):
```json
{"Version":2,
 "NextUpdate":"2026-04-17T18:52:29.285569899Z",
 "UpdatedAt":"2026-04-16T18:52:29.28557025Z",
 "DownloadedAt":"2026-04-18T19:01:08.782790623Z"}
```
So the upstream DB age AND the image-build time are both available on
disk. We just don't read them.

**Root cause.** `internal/api/admin_trivy.go:115` reads only the
`trivy_db_meta` row (our own metadata), which is empty for baked-in
installs (we never INSERT on the baked-in seed path —
`internal/app/app.go:SeedTrivyDB` at `app.go:814` copies files and
logs but doesn't touch the `trivy_db_meta` table).

**Fix sketch.**
1. In `SeedTrivyDB` (`app.go:814`), after the copy, INSERT a row into
   `trivy_db_meta` with:
   - `source = "baked-in"`
   - `version = "baked-<YYYYMMDD>"` (read from `metadata.json`
     `UpdatedAt`)
   - `applied_at = metadata.json.DownloadedAt` (when the DB was baked
     into the image) — OR the current time for the FIRST-BOOT copy.
   - `size_bytes = sum of file sizes`
2. Then remove the `-1` special case at `admin_trivy.go:124` — baked
   DBs flow through the normal `ageHours` computation.
3. UI at `DashboardPage.tsx:981` still needs to surface "baked-in" as
   a source label, but with a real age the text reads e.g.
   `Updated 3 days ago · baked-in` instead of "unknown".

**Bonus.** `TrivyPage.tsx:285` already has branching on
`status.source === 'baked-in'` — extend it to show the
`applied_at` timestamp from the seed.

### F-T14 · Dashboard "View findings →" link goes nowhere
**Phase:** 1 · **Severity:** real-issue (UX)

**Evidence.** `web/src/pages/DashboardPage.tsx:790`:
```tsx
render={<a href="#high-severity">View findings →</a>}
```
There is no element with `id="high-severity"` anywhere in the tree,
and there is no `/findings` or `/admin/findings` route in
`web/src/App.tsx`. The link is a dead anchor.

**Fix.**
1. Decide the destination. Natural options:
   - A new page `/admin/findings` showing a paginated table of all
     scan findings (grouped by severity, linking back to the
     artifact+CVE). Mirrors the existing `/admin/*` admin routes in
     `App.tsx:292-352`.
   - Or send the user to the Search page pre-filtered to severities
     `critical|high`.
2. If neither ships now, delete the CTA. Current state (dead link) is
   worst.

### F-T15 · Repo header shows size but no item count
**Phase:** UI walkthrough · **Severity:** minor (UX)

**Evidence.** `web/src/pages/repo/RepoPageLayout.tsx:93`:
```tsx
{typeLabel} repository &middot; {formatBytes(repo.size_bytes)}
```
No `item_count` field is passed or displayed.

**Fix.**
1. Add `item_count` (or per-type `tag_count`, `package_count`) to
   `Repo` in `internal/api/types.go` and populate it in whatever
   query feeds `GET /api/v1/projects/{name}/repos`. The counts are
   cheap SELECTs:
   - docker: `COUNT(*) FROM docker_tags WHERE repo_id=?`
   - rpm: `COUNT(*) FROM rpm_packages WHERE repo_id=?`
   - deb: `COUNT(*) FROM deb_packages WHERE repo_id=?` (careful: a
     deb may exist in multiple suites — choose distinct package+arch
     count).
   - pypi/helm/raw/s3: analogous.
2. Update `RepoPageLayout.tsx:93` to render:
   `{typeLabel} repository · {count} {unit(count, typeLabel)} · {formatBytes(...)}`.
   Unit helper: "packages" / "images" / "charts" / "files" / "objects".

### F-T16 · Search / filter inputs have no "×" clear affordance
**Phase:** UI walkthrough · **Severity:** minor (UX)

**Evidence.** `web/src/components/common/InlineSearch.tsx` (whole
file — no clear button). Used across every repo content tab, search
page, admin tables.

**Fix.** Add a clear button inside `InlineSearch.tsx`:
```tsx
{value && (
  <button
    type="button"
    aria-label="Clear"
    className="absolute right-2 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground"
    onClick={() => onChange('')}
  >
    <X className="size-4" />
  </button>
)}
```
+ `import { X } from 'lucide-react'`. Apply `pr-8` to the `<Input>`
to make room. One place, every consumer benefits.

### F-T17 · Clicking a row in the RPM list shows detail at the BOTTOM of the list, not inline
**Phase:** UI walkthrough · **Severity:** real-issue (UX)

**Evidence.** `web/src/pages/repo/RpmRepoPage.tsx:271`:
```tsx
{/* Selected package detail */}
{selectedPkg && (
  <div className="rounded-md border bg-muted/30 p-4 space-y-2">
    …
```
The detail panel is rendered AFTER `<DataTable>` and outside it. For a
repo with 815 packages, clicking a row paints the panel three
viewports below the clicked cell. Users can't see that their click
did anything.

**Root cause.** State `selectedPkg` lives on the parent page;
`DataTable` has no concept of "expanded row". The detail is rendered
as a sibling of the table.

**Fix.**
1. Promote the detail into the table as an accordion row. Two options:
   - **Easiest** — change `DataTable` to accept a
     `renderExpanded?: (row) => ReactNode` prop, and the table inserts
     a full-width `<tr>` right after the expanded row. `RpmRepoPage`
     passes an `isRowExpanded` function + the existing detail JSX.
   - **More general** — a `<Disclosure>` wrapper component that any
     repo content table can use. Apply to every repo type.
2. While fixing, make sure scroll position doesn't jump when the panel
   opens (use `scroll-margin-top` on the expanded row).

Applies to `DebRepoPage`, `PypiRepoPage`, `HelmRepoPage`, `RawRepoPage`
once it's refactored.

### F-T18 · Repo Content tabs have no pagination
**Phase:** UI walkthrough · **Severity:** real-issue (UX + perf)

**Evidence.** 815 RPMs render in a single table today; 4,850 debs in
another. Real installs will see 10k–100k. The backend already
supports pagination:
- `internal/api/repo_content.go:160` parses `limit` (default 100, cap
  500) and `offset` (at `:171`, same pattern).
- But the SPA sends no `limit`/`offset` and renders whatever arrives.

**Fix sketch.**
1. Frontend — pick one of:
   - Classic page numbers. State: `page` + `pageSize` (default 100).
     Render page N-buttons below the table.
   - Cursor-based "Load more" (already the style used by the
     per-row-scan feature — see the recent "load-more pagination"
     commit `8ffe66c`).
   - Virtual scroll (react-virtual/tanstack) — best for 10k+ rows.
2. Backend already capped at `limit=500`. For the frontend to
   page smoothly above 500 rows we need either:
   - The backend to respond with `{items, next_offset|next_cursor}`
     instead of a bare `[]`. The current return type at
     `repo_content.go:156` is `[]RepoContentEntry`. Change to a
     wrapper `{ items: [], total: int, next_offset: int }`. Frontend
     type `RepoContentEntry` is declared in `web/src/api/types.ts` —
     add the wrapper.
3. Apply the same to every repo type's content tab.

Skip this and the UI falls over above ~1k rows — we already saw the
"click row, detail at bottom" UX problem (F-T17) be even more painful
because the bottom is several screens away.

---

## 5 · Phase results table

| Phase | Exit criterion | Result |
|-------|----------------|--------|
| 1 | Container up, 4 repos, Trivy baked-in | **PASS** (container ready <1 s after `docker run`, idle RSS 86 MiB) |
| 2 | 20 docker images, push + pull digests match | **PARTIAL** (16/20; 4 blocked by F-T3 chunk cap; F-T4 fixed the 2 bad tags) |
| 3 | OL9 `dnf makecache` from OmniRepo | **PASS** |
| 4 | ~10k debs ingested, InRelease verifies | **PASS** (4,850 debs in 74 s @ P=6; zero "database is locked"; InRelease GPG-verified with key `075194C6…6551F492`) |
| 5 | Observability green at scale | **PASS** (dashboard 56 ms, search 23 ms, repo content 20 ms; SQLite 13 MB; scans=833, all done) |
| 6 | Host `apt install` + `docker pull` from OmniRepo only, zero outbound | **PARTIAL** (apt install **requires F-T6 workaround**; after workaround, tcpdump verified zero non-loopback traffic. Docker pull ✅ with 4-segment URL) |
| 7 | OL9 container `dnf install nano` | **PASS** |
| 8 | Reupload idempotent | **PASS** (5 RPMs re-PUT returned 201, no NEVRA dupes) |
| Teardown | apt sources restored | **PASS** (apt-get update against upstream archives confirms) |

---

## 6 · Ship / no-ship read

**Blockers for 1.0:**
- **F-T6** — Packages.gz path mismatch defeats apt mirroring.
- **F-T10** — Docker repo content tab shows empty for a populated repo.

**Real issues (pre-1.0 fix):**
- Protocol: F-T3 (OCI 64 MiB cap), F-T7 (URL-decode `%2B`),
  F-T8 (Basic auth on /api/v1).
- UI: F-T11 (wrong docker CLI snippets), F-T12 (breadcrumb 404 +
  chrome-less 404), F-T13 (Trivy age "unknown" even for fresh
  builds), F-T14 (dead `View findings →` link), F-T17 (detail panel
  renders at bottom of list), F-T18 (no pagination on repo content).

**Polish for post-1.0:** F-T4, F-T5, F-T9, F-T15 (item count in repo
header), F-T16 (× clear on search inputs).

**Minimum for "homelab-ready":** F-T3, F-T6, F-T7, F-T10, F-T11,
F-T12, F-T13, F-T14, F-T17, F-T18.

---

## 7 · Recommended fix order

Fastest-to-cheap-win first, blockers early:

1. **F-T7** (URL decode) — ~10 LoC, unblocks F-T6 workaround too.
2. **F-T8** (Basic auth on /api/v1) — ~15 LoC, copy the block from
   `basic_or_apikey.go`.
3. **F-T3** (OCI chunk cap) — ~30 LoC, new config key + wiring.
4. **F-T13** (Trivy baked age) — one INSERT in `SeedTrivyDB`.
5. **F-T11** (docker CLI snippets) — 2-line fix + test update.
6. **F-T14** (dead findings link) — either ship the page or delete
   the CTA.
7. **F-T12** (breadcrumb 404) — 2-part: make the crumb non-link, AND
   wrap `NotFoundPage` in `AppShell` when authenticated.
8. **F-T6** (deb pool path) — schema migration + generator change.
   Biggest change, but on the path to unblocking.
9. **F-T10** (docker content endpoint) — new listDockerContent query.
10. **F-T18** (pagination) — largest frontend change; tackle after
    F-T10 to exercise both at once.
11. **F-T17** (accordion detail) — depends on DataTable refactor.
12. **F-T15** (item counts) — backend query addition + header render.
13. **F-T16** (× clear) — 1 component edit.
14. **F-T9** (setup page when done) — 1 component edit.
15. **F-T4/F-T5/F-T2** (test.md hygiene) — doc touch-ups.
