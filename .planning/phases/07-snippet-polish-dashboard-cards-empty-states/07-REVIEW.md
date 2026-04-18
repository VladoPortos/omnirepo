---
phase: 07-snippet-polish-dashboard-cards-empty-states
reviewed: 2026-04-18T04:14:38Z
depth: standard
files_reviewed: 46
files_reviewed_list:
  - Makefile
  - internal/api/admin_jobs.go
  - internal/api/admin_jobs_test.go
  - internal/api/admin_phase1.go
  - internal/api/dashboard.go
  - internal/api/dashboard_test.go
  - internal/app/app.go
  - internal/app/phase3_helm.go
  - internal/protocol/deb/pool_release.go
  - internal/protocol/deb/pool_release_test.go
  - internal/protocol/deb/sync_handler.go
  - internal/protocol/helm/handler_test.go
  - internal/protocol/helm/oci_mirror.go
  - internal/protocol/helm/oci_mirror_test.go
  - internal/protocol/oci/blobs.go
  - internal/protocol/oci/handler.go
  - internal/protocol/oci/helm_mirror_test.go
  - internal/protocol/oci/manifests.go
  - internal/protocol/oci/mediatype.go
  - web/e2e/dashboard-composition.spec.ts
  - web/e2e/empty-states.spec.ts
  - web/e2e/snippet-copy.spec.ts
  - web/src/api/queries.ts
  - web/src/components/common/CopyButton.tsx
  - web/src/components/common/EmptyState.tsx
  - web/src/components/common/SnippetList.tsx
  - web/src/components/common/SnippetPanel.tsx
  - web/src/lib/__tests__/dashboard-thresholds.test.ts
  - web/src/lib/__tests__/snippets.test.ts
  - web/src/lib/dashboard-thresholds.ts
  - web/src/lib/snippets.ts
  - web/src/pages/DashboardPage.tsx
  - web/src/pages/ProjectDetailPage.tsx
  - web/src/pages/ProjectsPage.tsx
  - web/src/pages/SearchPage.tsx
  - web/src/pages/admin/TLSPage.tsx
  - web/src/pages/admin/TrashPage.tsx
  - web/src/pages/repo/AptRepoPage.tsx
  - web/src/pages/repo/DockerRepoPage.tsx
  - web/src/pages/repo/GitRepoPage.tsx
  - web/src/pages/repo/HelmRepoPage.tsx
  - web/src/pages/repo/PypiRepoPage.tsx
  - web/src/pages/repo/RawRepoPage.tsx
  - web/src/pages/repo/RpmRepoPage.tsx
  - web/src/pages/repo/S3BucketPage.tsx
  - web/vitest.config.ts
findings:
  critical: 0
  warning: 4
  info: 8
  total: 12
status: issues_found
---

# Phase 7: Code Review Report

**Reviewed:** 2026-04-18T04:14:38Z
**Depth:** standard
**Files Reviewed:** 46
**Status:** issues_found

## Summary

Phase 7 delivered snippet rewrites, six Composition-row dashboard cards, EmptyState
adoption across ~12 surfaces, and the Helm OCI→traditional mirror. Go-side work
is solid: transaction discipline in `helm.Mirror.MirrorToTraditional`,
post-commit mirror invocation from OCI `manifestPut`, per-bucket aggregation
in `dashboard.go`, and the new admin-jobs summary endpoint all look correct.
The `ResolvePoolPath` DEB Release-file reader plus traversal guard are well-
tested. The Codex-flagged follow-ups (InRelease fallback, disabled-CTA keyboard
focus) have been addressed.

Findings concentrate on the frontend:

- **WR-01** — `handleUpload` in five repo pages (Docker, Helm, PyPI, Apt, Rpm,
  Raw) builds its URL using `repo.name` twice instead of `projectName` then
  `repo.name`. A drag-drop upload today would hit the wrong REST path. This
  pre-dates Phase 7, but every affected file was modified in scope (EMPTY-03/04
  migrations), so it is now owned by this phase's surface area.
- **WR-02** — `GitRepoPage.cloneUrl` is missing the `/git/` path segment. The
  copy-to-clipboard block in the Git page header ships a URL that won't match
  the backend chi route (`/git/{project}/{repo}.git`). The Phase 7 `snippets.ts`
  `git` case emits the correct form.
- **WR-03** — `DockerRepoPage` renders `docker pull ${hostname}/${repo.name}:${row.tag}`
  (missing `projectName`). MOCK_TAGS is empty today so the row never hits the
  screen, but this is latent copy-paste-and-fail the moment real data arrives.
- **WR-04** — `S3BucketPage` passes the raw `name` route param straight into
  `Link to={`/projects/${name}`}` (line 191), bypassing the `enc()` wrapper
  convention established in `queries.ts`. Low-risk today (slugs are pre-
  validated) but breaks the defense-in-depth posture the Phase 7 query layer
  just established.

Info-level findings cover minor hygiene: a commented-out `chimw` import variance
in `manifests.go`, the `BucketDetail` object-count optional-chain pattern that
could collapse to a single expression, and the `SearchPage` test locator
fragility documented elsewhere. None are bugs.

## Warnings

### WR-01: Upload URL builds `/projects/<repo>/repos/<repo>/artifacts` instead of `/projects/<project>/repos/<type>/<repo>/artifacts`

**Files:**
- `web/src/pages/repo/AptRepoPage.tsx:163-165`
- `web/src/pages/repo/RpmRepoPage.tsx:144-147`
- `web/src/pages/repo/HelmRepoPage.tsx:162-164`
- `web/src/pages/repo/PypiRepoPage.tsx:199-201`
- `web/src/pages/repo/RawRepoPage.tsx:181-184`

**Issue:** `handleUpload` interpolates `repo.name` into both URL slots that
should be `projectName` and `repo.name`. When the user drags a file onto the
Dropzone, the client POSTs to e.g. `/projects/myrepo/repos/myrepo/artifacts`
rather than `/projects/myproj/repos/rpm/myrepo/artifacts`. The backend route
requires the repo **type** in the second slot (see `admin_phase1.go:220` —
`/projects/{name}/repos/{type}/{repo}`), so every variant is a 404.

This is a pre-Phase-7 issue, but Phase 7 explicitly edited these files (EMPTY-03/
EMPTY-04 adoption in commits `a325d0b` and `11cdc3d`), so it's in scope now.

**Fix (template, apply to all five):**
```tsx
// Current
const handleUpload = async (file: File, onProgress: (pct: number) => void) => {
  await api.upload(`/projects/${repo.name}/repos/${repo.name}/artifacts`, file, onProgress);
};

// Correct — include projectName + repo type
const handleUpload = async (file: File, onProgress: (pct: number) => void) => {
  await api.upload(
    `/projects/${encodeURIComponent(projectName ?? '')}/repos/rpm/${encodeURIComponent(repo.name)}/artifacts`,
    file,
    onProgress,
  );
};
```

Substitute `rpm` with the page's protocol (`deb`, `helm`, `pypi`, `raw`) per
file. RawRepoPage.tsx already has an additional `${uploadPath}` suffix — keep
that, just fix the two earlier segments.

### WR-02: Git clone URL omits the `/git/` route segment

**File:** `web/src/pages/repo/GitRepoPage.tsx:113`

**Issue:**
```tsx
const cloneUrl = `${window.location.protocol}//${hostname}/${projectName}/${repo.name}.git`;
```

The backend Git handler mounts at `/git/{project}/{repo}` (verified in
`internal/protocol/git/handler.go:99`). The Phase-7 `snippets.ts` `git` case
correctly emits `https://${host}/git/${project}/${repo}.git`. The cloneUrl
block at the top of `GitRepoPage` and its empty-repo variant copy the wrong
URL to clipboard — `git clone` against it returns 404.

**Fix:**
```tsx
const cloneUrl = `${window.location.protocol}//${hostname}/git/${projectName}/${repo.name}.git`;
```

### WR-03: Docker CopyButton snippet missing `projectName` segment

**File:** `web/src/pages/repo/DockerRepoPage.tsx:201`

**Issue:**
```tsx
<CopyButton
  text={`docker pull ${hostname}/${repo.name}:${row.tag}`}
  ...
/>
```

Docker registry paths on OmniRepo live at `${host}/${project}/${repo}/${image}:${tag}`
(spec §10, also confirmed by the Phase-7 `snippets.ts` docker case). The copy
button emits `${host}/${repo.name}:${tag}` which is missing both the project
segment and `/docker/`. MOCK_TAGS is empty today so the row never renders in
production, but the moment real tag data is wired the operator gets a bogus
command.

**Fix:**
```tsx
<CopyButton
  text={`docker pull ${hostname}/${projectName}/${repo.name}:${row.tag}`}
  className="size-7"
/>
```

Note: `projectName` comes from `useParams` at the top of the file — reuse that
variable (not `repo.name`) to stay consistent with the rest of the file.

### WR-04: S3BucketPage interpolates `name` param without URL encoding

**File:** `web/src/pages/repo/S3BucketPage.tsx:174, 191`

**Issue:** Two interpolations of `name` (the project slug from `useParams`)
directly into URL strings:
- line 174: `navigate(`/projects/${name}`)` after bucket delete
- line 191: `<Link to={`/projects/${name}`}>`

The Phase 7 `queries.ts` established the `enc = encodeURIComponent` wrapper
convention specifically so route params can't slip into URLs unencoded. The
queries hooks (`useBucket`, `useBucketObjects`, `useDeleteBucket`) already
encode, but these inline navigation calls bypass the guard.

Today's slugs are restricted to `[a-z0-9._-]` so `encodeURIComponent` is a
no-op on the golden path. The risk is a future feature that loosens the
validation rule, or a manual URL typo surfacing in test fixtures.

**Fix:**
```tsx
navigate(`/projects/${encodeURIComponent(name)}`);
// and
<Link to={`/projects/${encodeURIComponent(name)}`}>{name}</Link>
```

Same applies to the hover-template on line 192's endpoint copy:
`Endpoint: <code>/s3/{bucket}</code>` — bucket is also a URL-param; if WR-04
is applied the same encoding should travel.

## Info

### IN-01: ProjectDetailPage S3 empty-state diverges from EmptyState primitive

**File:** `web/src/pages/ProjectDetailPage.tsx:572-587`

**Issue:** The S3BucketsTab renders a hand-rolled "No buckets" empty state
with its own border-dashed wrapper and CTA, instead of the shared `<EmptyState>`
primitive that every other zero-state in Phase 7 uses. Behaviorally fine,
but inconsistent with the `empty-states.spec.ts` `assertEmptyState` contract
which asserts `data-testid="empty-state"` on every surface.

**Fix:** Replace with an EmptyState once S3 is promoted into the EMPTY-01'
spec. Low priority.

### IN-02: `DashboardPage.activityTargetHref` silently swallows unknown `action` prefixes

**File:** `web/src/pages/DashboardPage.tsx:1044-1056`

**Issue:** Unknown action prefixes return `''` which falls through to the
non-linked branch. Dead-data path that's intentional but uncommented — any
new audit event kind (the backend has been adding them per phase) silently
loses its drill-through. A developer-mode warning `console.warn` or an
explicit TODO would make the gap greppable.

**Fix:** Add a comment listing the known prefixes the function handles and
the upgrade path for new ones.

### IN-03: `manifestRefs` treats any JSON parse failure as 400 INVALID — by design, but worth a comment

**File:** `internal/protocol/oci/manifests.go:55-103`

**Issue:** `manifestRefs` returns an error on JSON parse failure. The caller
in `manifestPut` correctly 400s; in `manifestDelete` (after WR-04 in the code)
it also 400s per the block comment at `manifests.go:512-516`. The
re-parse in `writeManifestWithRefcounts:636` silently converts parse errors
to `refs = nil` (dropping the ref-decrement step). The comment on
manifests.go:637 (`if perr != nil { priorRefs = nil }`) is the opposite of
the WR-04 policy applied to DELETE, which explicitly errors out on a
malformed stored manifest.

This may be intentional — the prior manifest is rarely broken — but it means
tag-overwrite paths silently skip the ref-delta on a corrupt prior. Worth a
one-line note explaining why the policies differ.

### IN-04: `dashboard.go` manual numeric parse inlined instead of `strconv.ParseInt`

**File:** `internal/api/dashboard.go:336-342, 420-426`

**Issue:** The settings-fallback for `storage_total_bytes` hand-parses each
digit character into `n`:
```go
for _, c := range totalStr {
    if c >= '0' && c <= '9' {
        n = n*10 + int64(c-'0')
    }
}
```
Functionally identical to `strconv.ParseInt(totalStr, 10, 64)` but ignores
parse failures silently (returns 0). Since `storage_total_bytes` is admin-set,
a typo yields silent 0 with no log. Low-impact but `strconv.ParseInt + slog.Warn`
on error matches the logDashErr pattern used elsewhere in the same handler.

### IN-05: `oci_mirror.go` uses `os.MkdirTemp` per invocation

**File:** `internal/protocol/helm/oci_mirror.go:99-103`

**Issue:** Every mirror call creates a fresh tmp dir under the OS-default
`/tmp` rather than `$DATA_ROOT/tmp/`. On air-gapped hosts with a dedicated
data volume, this spreads IO between volumes unexpectedly. Working as
designed per the cleanup pattern, but inconsistent with the
`uploadTmpPath` convention in `blobs.go` (`$DATA_ROOT/tmp/uploads/`).

**Fix (optional):**
```go
tmpDir, err := os.MkdirTemp(filepath.Join(dataRoot, "tmp"), "helm-mirror-*")
```
Requires threading `dataRoot` into the Mirror struct. Not a correctness
issue — defer to a future hygiene pass unless operators hit the mixed-volume
write pattern.

### IN-06: `DashboardPage` cold-load renders 6 composition skeletons pre-`useMe()` hydration

**File:** `web/src/pages/DashboardPage.tsx:268-275`

**Issue:** The cold-load branch always renders six `<SkeletonCard>`s under the
Composition row without waiting for `useMe()` to resolve `is_super_admin`.
On a non-admin's first load this flashes 3 skeleton slots that then
disappear once the real layout renders. The comment on line 261-267
acknowledges the trade-off but doesn't mention that the visual artifact is
*expected* on every non-admin page load, which may surprise reviewers.

**Fix:** Consider conditionally rendering 3 skeletons when `meQ.data` is
present-and-non-admin; keep 6 for the undetermined case. Minor UX polish.

### IN-07: `empty-states.spec.ts` EMPTY-01 cleanup is best-effort

**File:** `web/e2e/empty-states.spec.ts:193-210`

**Issue:** The `EMPTY-01: zero projects` test bulk-deletes every project
before asserting zero. If a test running in parallel creates a project
between the delete and the EmptyState assertion, the test will flake. The
comment on line 196-199 acknowledges this. Consider Playwright's `test.describe.serial`
wrapping EMPTY-01 and EMPTY-02 (which depend on each other's project seed
shape) to remove the race.

**Fix (optional):**
```ts
test.describe.serial('EMPTY-01 + EMPTY-02 depend on project listing invariants', () => {
  test('EMPTY-01: zero projects', ...)
  test('EMPTY-02: zero teammates', ...)
})
```

### IN-08: `IndexDEB`/`IndexDEBDelete` pair in `deb/sync_handler.go` duplicates FTS maintenance seen elsewhere

**File:** `internal/protocol/deb/sync_handler.go:296-302`

**Issue:** The delete-then-insert pattern for FTS is consistent with
`writeManifestWithRefcounts:645-653` (OCI) and `helm/oci_mirror.go:164-171`.
Consider hoisting into a `metadata.ReindexDEB` helper mirroring the Helm
shape — reduces duplication in future protocol handlers. Pure cleanup,
not a bug.

---

_Reviewed: 2026-04-18T04:14:38Z_
_Reviewer: Claude (gsd-code-reviewer)_
_Depth: standard_
