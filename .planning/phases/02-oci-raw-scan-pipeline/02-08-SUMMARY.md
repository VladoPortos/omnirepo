---
phase: 02-oci-raw-scan-pipeline
plan: 08
subsystem: protocol + metadata + audit
tags: [raw, pathstore, fts5, audit, anonymous-read, mime-detection]
requires:
  - internal/storage/pathstore.go (Phase 1)
  - internal/storage/trash.go (Phase 1)
  - internal/auth/policy.go (Phase 1, ActorKindAnonymous from 02-05)
  - internal/auth/middleware/basic_or_apikey.go (Phase 1)
  - internal/httpx/anon_read.go (02-05 — AnonymousReadOK)
  - internal/metadata.ReposRepo / ProjectsRepo / ScansRepo / IndexArtifact
  - internal/audit.Logger
provides:
  - internal/metadata/migrations/006_raw_files.up.sql
  - internal/metadata/migrations/006_raw_files.down.sql
  - internal/metadata.RawFilesRepo (Insert / Get / Delete / ListDir)
  - internal/protocol/raw.Handler (New / Mount)
  - internal/protocol/raw.Deps + SeverityGateFn
  - internal/audit.EvtRawPut / EvtRawDelete / EvtRawGetBlocked
affects:
  - internal/app/app.go (mounts the RAW handler after the OCI mount)
  - internal/audit/events.go (adds three event kinds)
tech-stack:
  added: []
  patterns:
    - "Single writer tx wraps PathStore.Put + raw_files upsert + FTS5 + scans enqueue"
    - "Two-tier MIME detection (mime.TypeByExtension → http.DetectContentType)"
    - "skipIfActor wrapper lets AnonymousReadOK + BasicOrAPIKey co-exist on the same chain"
    - "Trash-then-DB-delete ordering keeps a crash mid-DELETE recoverable (file in trash, row still present → next GC reconciles)"
    - "Composite-PK ON CONFLICT DO UPDATE for atomic raw_files upsert"
key-files:
  created:
    - internal/metadata/migrations/006_raw_files.up.sql
    - internal/metadata/migrations/006_raw_files.down.sql
    - internal/metadata/raw_files.go
    - internal/metadata/raw_files_test.go
    - internal/protocol/raw/handler.go
    - internal/protocol/raw/handler_test.go
    - internal/protocol/raw/put.go
    - internal/protocol/raw/get.go
    - internal/protocol/raw/delete.go
    - internal/protocol/raw/listing.go
    - internal/protocol/raw/listing_test.go
  modified:
    - internal/audit/events.go (added EvtRawPut/Delete/GetBlocked)
    - internal/app/app.go (constructs + Mounts the RAW handler)
decisions:
  - "raw_files schema chosen with composite PK (repo_id, path) so Insert can use ON CONFLICT(repo_id, path) DO UPDATE for atomic upsert. size_bytes/mime/sha256/modified all refresh on overwrite; modified is set to CURRENT_TIMESTAMP via excluded clause to make ListDir's idx_raw_files_modified meaningful."
  - "ListDir uses LIKE '<dir>/%' AND NOT LIKE '<dir>/%/%' for direct-children-only filtering — no recursive CTEs needed; leverages SQLite's LIKE optimization."
  - "Path validation is strict: empty, '.', '..', NUL byte, non-canonical paths all rejected before reaching PathStore. Defense in depth on top of PathStore.cleanKey (Phase 1 D-29)."
  - "GET on a directory accepts trailing slash ('/dir/') by stripping it pre-validation. PUT/DELETE keep trailing-slash rejection (write to a trailing-slash key is meaningless)."
  - "FTS5 artifact entry uses the path as both 'name' and 'digest' — the search interface in Phase 5 will query LIKE on filename, and the path is the only stable identifier we have for raw files (no content digest in the URL)."
  - "skipIfActor wrapper added so the chained middleware order (AnonymousReadOK → BasicOrAPIKey) doesn't force-401 anonymous-read requests that legitimately have no Authorization header."
  - "Severity gate is a SeverityGateFn callback in Deps; nil = no-op. Plan 02-09 supplies the real implementation. EvtRawGetBlocked declared now so audit-emit code is in place — no schema churn when 02-09 lands."
  - "actorIsProjectMember does its own SELECT on project_members rather than depending on auth.WithProjectMembership (which the api package's middleware sets, but we don't run that middleware on the raw subrouter — and pulling it in would create a chi.Router naming conflict for {project}). Direct query keeps coupling minimal."
  - "Audit ActorAPIKeyID for API-key-authenticated callers uses Actor.APIKeyID; user-authenticated uses Actor.ID. Anonymous requests don't populate either pointer (audit_log columns are nullable per Phase 1 D-33)."
metrics:
  duration: ~25m
  tasks: 2
  files: 11 created, 2 modified
  completed: 2026-04-15
requirements_complete:
  - RAW-01
  - RAW-02
  - RAW-03
  - RAW-04
  - RAW-05
  - SCAN-03
  - SRCH-01
---

# Phase 2 Plan 08: RAW Pass-Through Handler Summary

A complete RAW protocol surface at `/<project>/raw/<repo>/<path...>` with
PUT/GET/HEAD/DELETE, atomic writes via PathStore, two-tier MIME detection,
directory listings (JSON or HTML via `Accept`), anonymous-read support for
`public_read=true` repos, and an auto-scan enqueue path that fires through
the same writer transaction as the artifact metadata insert.

## Final raw_files Schema

```sql
CREATE TABLE raw_files (
    repo_id     INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
    path        TEXT    NOT NULL,
    size_bytes  INTEGER NOT NULL DEFAULT 0,
    mime        TEXT    NOT NULL DEFAULT '',
    sha256      TEXT    NOT NULL DEFAULT '',
    modified    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (repo_id, path)
);
CREATE INDEX idx_raw_files_modified ON raw_files(modified);
```

Notable choices:

- **Composite PK on (repo_id, path)** — exact conflict target needed for the
  `ON CONFLICT(repo_id, path) DO UPDATE` upsert. Refreshing
  size_bytes/mime/sha256/modified covers every overwriteable column.
- **`mime` and `sha256` default to empty string, not NULL** — keeps Go scan
  trivial (no `sql.NullString` plumbing), and downstream code can branch on
  `== ""` instead of `Valid` checks.
- **`modified` defaults to `CURRENT_TIMESTAMP` and is bumped on every
  upsert** via `excluded` semantics, making `idx_raw_files_modified` the
  basis for any "recently changed files" feature in Phase 5.

## Listing HTML Markup

Minimal — Phase 5 owns presentation. Output:

```html
<!doctype html><html><body><ul>
  <li><a href="a.txt">a.txt</a> 5</li>
  <li><a href="b.txt">b.txt</a> 4</li>
  <li><a href="sub/">sub/</a> 0</li>
</ul></body></html>
```

Names are HTML-escaped; directories get a trailing slash on both display
text and href so client-side navigation works without JavaScript.

JSON listing per D-30:

```json
[
  {"name":"a.txt","size":5,"is_dir":false,"modified":"2026-04-15T11:54:33Z"}
]
```

Selected by `Accept: application/json`; default falls back to HTML.

## Path Validation Function

`validateRawPath` in `internal/protocol/raw/handler.go`:

1. Reject empty input or input containing a NUL byte outright.
2. Strip a single leading `/`.
3. Split on `/` and reject any segment that is `""`, `"."`, or `".."`.
4. Run `path.Clean("/" + p)` and require the cleaned form (with leading
   slash trimmed) to equal the input — catches non-canonical slashes that
   the segment scan might miss.
5. Reject any `path.Clean` output starting with `/..`.

Outcome: matches the `validateRawPath` table in `listing_test.go` —
trailing slash, `..`, `./`, `//`, NUL, multi-level traversal all rejected;
plain `foo.txt`, `a/b/c.txt`, and `/leading/slash` accepted.

For GET/HEAD requests the resolver strips a single trailing slash before
calling `validateRawPath` so `/dir/` is handled as a directory listing
without a 400.

## Test Evidence

- `go build -mod=vendor ./...` — exit 0.
- `go test -mod=vendor -race -count=1 ./internal/protocol/raw/... ./internal/audit/... ./internal/metadata/...` — all green.
- Full-repo `go test -mod=vendor -race -count=1 ./...` — every package green
  except the pre-existing flake `internal/jobs/TestPool_NoHandlerMarksFailed`
  (also flagged in `02-05-SUMMARY.md`; not caused by changes here, see
  Deferred Issues).

Targeted coverage (`internal/protocol/raw`):

- `TestRawPutGet_HappyPath` — PUT 201 + Location, file on disk byte-equal,
  raw_files row populated with size + sha256 + mime, GET returns body with
  correct Content-Type and Content-Length.
- `TestRawGet_MagicNumberFallback` — `.foo` extension PNG-magic-bytes
  payload returns `image/png` via `http.DetectContentType` fallback.
- `TestRawPut_PathTraversalRejected` — `../..` paths never escape repo
  root (chi normalizes most before dispatch; PathStore + validateRawPath
  defend against the rest).
- `TestRawAnonymousGet_PublicRead` — anonymous GET on `public_read=true`
  repo succeeds; anonymous PUT on the same repo is denied.
- `TestRawAnonymousGet_PrivateRepoBlocked` — anonymous GET on a private
  repo returns 401.
- `TestRawDelete_MovesToTrash` — DELETE returns 204, file vanishes from
  CAS, raw_files row removed, subsequent GET returns 404.
- `TestRawPut_OversizedRejected` — body exceeding `MaxPutBytes` returns
  413.
- `TestRawGet_DirectoryListing_JSONAndHTML` — `Accept: application/json`
  returns JSON array including expected entries; default returns HTML
  containing escaped entry names.
- `TestRawGet_NotFound`, `TestRawPut_RepoMustExist` — 404 surfaces.
- `TestRawAuditEvents_PutAndDeleteRecorded` — `audit_log` rows created
  for both kinds.
- `TestAuditEventConstants` — string values match spec.

Targeted coverage (`internal/metadata`):

- `TestPhase2RawFilesMigration` — table + index exist after migration;
  schema_migrations recorded.
- `TestRawFilesRepo_InsertGetDelete` — round-trip of all columns.
- `TestRawFilesRepo_UpsertUpdatesSizeAndModified` — overwrite refreshes
  size/mime/sha256 AND advances `modified`.
- `TestRawFilesRepo_ListDirDirectChildrenOnly` — nested files NOT in
  direct-children listings.

## Threat-model compliance

| Threat   | Status     | Evidence |
|----------|------------|----------|
| T-02-08-01 path traversal | mitigated | `validateRawPath` rejects `..`/`.`/empty segments + non-canonical paths; PathStore re-validates via `cleanKey` (Phase 1 D-29). `TestRawPut_PathTraversalRejected` confirms no escape under repo root. |
| T-02-08-02 disk exhaustion via oversized PUT | mitigated | `http.MaxBytesReader` cap (configurable, default 5 GiB); `TestRawPut_OversizedRejected` confirms 413 + body-not-stored. |
| T-02-08-03 anonymous PUT/DELETE | mitigated | `AnonymousReadOK` only attaches anonymous Actor on GET/HEAD; `actorIsProjectMember` denies anonymous Kind on writes. `TestRawAnonymousGet_PublicRead` covers the `anonymous PUT → 401/403` branch. |
| T-02-08-04 anonymous GET on private repo | mitigated | `AnonymousReadOK` falls through when `public_read=false`; `BasicOrAPIKey` then 401s. `TestRawAnonymousGet_PrivateRepoBlocked` confirms. |
| T-02-08-05 FTS5 SQLi via filename | mitigated | All `IndexArtifact` calls parameterized; modernc/sqlite never interpolates. Same proof as 02-07 (FTS5 helpers in `internal/metadata/fts.go`). |
| T-02-08-06 listing reveals files an actor can't read | accept | Listing inherits repo-level read; per-file ACLs out of scope for v1. |
| T-02-08-07 TOCTOU between Stat and ServeContent | accept | Filesystem race acknowledged; `os.Open` returns whatever bytes are in the file at open time. |
| T-02-08-08 `http.DetectContentType` exposes magic | accept | Standard library behavior; mime is informational only. |

## Deviations from plan

### Auto-fixed / shape refinements

**1. [Rule 3 — Behavior] GET on `/dir/` with trailing slash returned 400**

- Found during: `TestRawGet_DirectoryListing_JSONAndHTML` first run.
- Issue: `validateRawPath` rejects empty trailing segments (correct for
  PUT/DELETE), so `/p/raw/r/dir/` failed validation before the directory
  branch could fire.
- Fix: In `resolveRepoAndPath`, when `requirePath=false` (GET/HEAD), strip a
  single trailing slash before calling `validateRawPath`. Trailing-slash
  rejection stays strict for writes.
- Files: `internal/protocol/raw/handler.go`.

**2. [Rule 2 — Correctness] Membership lookup not pre-resolved by api middleware**

- Found during: Task 2 design.
- Issue: The plan's action block referenced `auth.Can(actor, action, target)`
  but the RAW subrouter doesn't sit behind `api.Mount`'s
  `membershipResolver` middleware — and adding it would create a
  `{name}` vs `{project}` chi URL-param naming conflict on the same
  router. Without membership context, `auth.Can` for project-scoped
  actions returns `not_a_project_member` for legitimate writers.
- Fix: `actorIsProjectMember` in `put.go` does its own
  `SELECT COUNT(*) FROM project_members` lookup. Super-admin bypasses;
  project-owned API keys check `actor.ProjectScope`. Keeps RAW handler
  decoupled from api package's middleware stack.
- Files: `internal/protocol/raw/put.go`.

**3. [Rule 3 — API shape] `skipIfActor` wrapper for auth chain co-existence**

- Found during: Task 2 first end-to-end run (anonymous GET test).
- Issue: chaining `AnonymousReadOK → BasicOrAPIKey` directly: when
  `AnonymousReadOK` attaches an anonymous Actor and falls through, the
  next-stage `BasicOrAPIKey` looks at the (absent) Authorization header
  and 401s — defeating the anonymous fast path.
- Fix: Wrap `BasicOrAPIKey` in `skipIfActor`, which short-circuits when an
  Actor is already in ctx. Mirrors the same pattern as the OCI handler's
  `VerifyBearer.passes-through-when-Actor-set` logic, just in middleware
  form instead of inline.
- Files: `internal/protocol/raw/handler.go`.

## Deferred Issues

- **`internal/jobs/TestPool_NoHandlerMarksFailed`** flake: pre-existing,
  documented in `02-05-SUMMARY.md` Deferred Issues. Not introduced by this
  plan (verified by `git stash` + isolated re-run on the prior commit).
  Out of scope.

## Commits

| Hash    | Subject |
|---------|---------|
| 2d68fc6 | test(02-08): add failing tests for raw_files schema + repo (RED) |
| aa4e904 | feat(02-08): add raw_files table + typed repo (RAW-01..05) (GREEN) |
| a962c70 | test(02-08): add failing tests for RAW handler and listing (RED) |
| 2bc0ad0 | feat(02-08): RAW pass-through handler with PUT/GET/HEAD/DELETE + listings (D-27..D-31, RAW-01..05) (GREEN) |

## Self-Check: PASSED

- internal/metadata/migrations/006_raw_files.up.sql — FOUND
- internal/metadata/migrations/006_raw_files.down.sql — FOUND
- internal/metadata/raw_files.go — FOUND
- internal/metadata/raw_files_test.go — FOUND
- internal/protocol/raw/handler.go — FOUND
- internal/protocol/raw/handler_test.go — FOUND
- internal/protocol/raw/put.go — FOUND
- internal/protocol/raw/get.go — FOUND
- internal/protocol/raw/delete.go — FOUND
- internal/protocol/raw/listing.go — FOUND
- internal/protocol/raw/listing_test.go — FOUND
- internal/audit/events.go — FOUND (modified)
- internal/app/app.go — FOUND (modified, raw.Mount call present)
- Commits 2d68fc6, aa4e904, a962c70, 2bc0ad0 — FOUND in `git log --oneline`
- `go build -mod=vendor ./...` — exit 0
- `go test -mod=vendor -race -count=1 ./internal/protocol/raw/... ./internal/metadata/... ./internal/audit/...` — exit 0
