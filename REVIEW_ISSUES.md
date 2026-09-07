# Full Build Review — Issues and Solution Status

This is the single retained record from the review session.

## Important status note

- No product-code fixes from this review were merged into `main`.
- `main` contains this report only; implementation work remains isolated on branch `codex/fix-full-review` in `.worktrees/fix-full-review`.
- **Implemented in worktree** means a solution exists and was tested on the isolated branch, but it is not part of `main` and should not be treated as released.
- **Not implemented** means the issue was reviewed and a solution was planned, but no implementation was completed.

## Data lifecycle and access

### D1. A deleted project name could reattach its old storage tree

**Status:** Implemented in worktree; not merged.

**Issue:** A soft-deleted project did not reserve its name. Creating a new project with the same name could reuse the old name-based storage directory and attach deleted repository bytes to the new project. Project-level purge was also incomplete and not safely retryable.

**Solution prepared:** Reserve names across live and deleted project rows; allow reuse only after explicit project purge. Purge uses durable intent/quarantine states, serializes the project namespace, removes metadata transactionally, compensates on failure, and keeps namespace-generation markers so stale writers/restores cannot populate a reused name. Relevant worktree commits include `f41161bc`, `c7da5ebc`, `77ee3e03`, `d1ffad7f`, and `aa9fa518`.

### D2. Repository and drift restores could lose data or report false success

**Status:** Implemented and heavily hardened in worktree; not merged. Final review cleanup was interrupted before merge.

**Issue:** Restore moved or deleted filesystem data independently from database changes. A database failure, concurrent restore/purge, cancellation, or process crash could remove the trash record, move live bytes back to trash, leave metadata without payloads, or make the operation non-retryable.

**Solution prepared:** Replace one-step restore with a durable state machine (`restore_intent`, `payload_moved`, `db_restored`) and atomic sidecars. Add canonical holder ownership, holder-to-project lock ordering, immutable project ID/generation binding, authoritative database reconciliation after restart, finalize-only handling after a committed database restore, generation-owned compensation, and cleanup guards so admin purge/retention cannot delete pending recovery. Generic, repository, drift, and Helm rollback paths share the lifecycle owner. Relevant worktree commits include `12d81a8d`, `103a878c`, `77ee3e03`, `d1ffad7f`, `aa9fa518`, and `9901a9b1`.

### D3. The final live super-admin could be demoted

**Status:** Implemented in worktree; not merged.

**Issue:** Role changes were checked and written separately, so concurrent demotions could remove the last live super-admin. Multi-field admin-user PATCH requests could also partially commit, and session-revocation errors were ignored.

**Solution prepared:** Count live super-admins and apply demotion inside one serialized writer transaction. Apply the full admin patch—role, email, password flags/hash, and session invalidation—in one transaction and emit the success audit only after commit. Relevant worktree commits include `a3eb281d`, `ca9ef56d`, `322743e1`, and `77ee3e03`.

### D4. Restart recovery left abandoned running jobs leased

**Status:** Implemented in worktree; not merged.

**Issue:** Boot recovery used a stale-time threshold, so recent or future-skewed `running` rows owned by the previous process could remain stuck indefinitely.

**Solution prepared:** Recover every `running` sync/scan row before workers start. Abandoned Helm syncs become terminal failures; other syncs and scans return to pending, with lease and in-flight state cleared consistently. Relevant worktree commits: `58842521` and `a1dc4cb9`.

### D5. Viewers could invoke scan mutations

**Status:** Implemented in worktree; not merged.

**Issue:** Artifact rescan, repository rescan, and scan-prune POST routes reused read-capable resolution without an explicit maintainer mutation check.

**Solution prepared:** Keep read resolution unchanged, then require the existing repository-update capability for all three mutation routes. Viewer sessions and viewer keys receive `403`; maintainers, valid project keys, and super-admins keep access. Relevant worktree commit: `c230a680`.

### D6. Restoring a repository did not rebuild all search projections

**Status:** Implemented in worktree; not merged.

**Issue:** Restore reindexing omitted Go modules, npm packages, Maven artifacts, and vulnerability/CVE search rows. Repeated or project-wide restores could also duplicate rows or choose shared CVE data nondeterministically.

**Solution prepared:** Rebuild Go, npm, Maven, artifact, and CVE projections from canonical tables in the restore transaction. Gate reindexing on an actual restore transition and select shared CVE data deterministically across the complete restored set. Relevant worktree commits: `42a1f57e` and `4b74a9cb`.

## Protocol correctness

### P1. Per-request HTTP timeout was used as a whole mirror-job deadline

**Status:** Not implemented.

**Issue:** Healthy DEB, RPM, PyPI, Helm, and Git mirrors could fail when aggregate job time exceeded `UpstreamHTTPTimeout`, even though every individual HTTP request completed within the configured limit.

**Planned solution:** Let the worker/job context control the complete job; keep `UpstreamHTTPTimeout` only on the HTTP client and preserve caller cancellation.

### P2. PEP 694 multi-file commits could partially publish

**Status:** Not implemented.

**Issue:** A failure during a PyPI upload-session commit could publish only some files or rows, race with legacy uploads/concurrent commits, and leave the session in an untruthful state.

**Planned solution:** Add an atomic `ReplaceBatch`, sorted shared upload locks, one SQLite transaction for all rows, and an `open → committing → committed` session state with full rollback and retry.

### P3. S3 backend decoded opaque listing markers

**Status:** Not implemented.

**Issue:** The backend opportunistically base64-decoded a raw marker, so a legitimate key resembling base64 could be skipped. Protocol token encoding already belongs to gofakes3.

**Planned solution:** Keep backend markers raw and let gofakes3 perform the single V2 continuation-token encoding/decoding layer.

### P4. OCI final PUT could exceed the upload-session byte cap

**Status:** Not implemented.

**Issue:** The byte limit was enforced for PATCH chunks but not for bytes included in the final PUT, allowing a session to exceed `SessionMaxBytes`.

**Planned solution:** Apply the aggregate limit before accepting the final body, preserve the previous session on `413`, and cover exact-boundary, empty-final-body, and chunked-body rollback cases.

## Web/API contracts

### W1. Browser upload controls called nonexistent or incompatible routes

**Status:** Not implemented.

**Issue:** RAW, APT/DEB, RPM, PyPI, and Helm dropzones could not complete their visible upload workflows against real authenticated API contracts.

**Planned solution:** Add first-class authenticated browser-upload APIs backed by shared protocol services; use the three-step PyPI session flow; return a common typed result; update all dropzones and verify real server state.

### W2. Search navigation inferred repository identity from display text

**Status:** Not implemented.

**Issue:** The UI split `location` and could interpret a version such as `3.0.1` as a project/repository route.

**Planned solution:** Return explicit `project_name`, `repo_name`, and `repo_type` fields and build routes exclusively from canonical identity.

### W3. Git browse/download URLs were inconsistent and incorrectly encoded

**Status:** Not implemented.

**Issue:** The download URL used a reversed route and ad-hoc interpolation that broke refs or paths containing spaces, `%`, `#`, `?`, or slashes.

**Planned solution:** Use one tested URL builder for browse and raw download, encoding components while preserving intended separators.

### W4. A protected-request 401 outside `/me` did not invalidate the session

**Status:** Not implemented.

**Issue:** Expired/revoked sessions could leave private cached data and the UI in an authenticated-looking state until `/me` happened to fail.

**Planned solution:** Use one idempotent unauthorized callback for fetch and XHR, clear protected queries/cache, set `['me']` to null, and let `AuthGuard` preserve the return path.

### W5. Production maintenance actions were placeholders or dead ends

**Status:** Not implemented.

**Issue:** Wipe/sync controls either showed “API not yet connected,” failed to invalidate content, or appeared for repository types without a backend action.

**Planned solution:** Wire supported wipe and ad-hoc sync mutations to real endpoints, show actual job/error results, invalidate affected queries, and hide unsupported controls.

### W6. Role capabilities were applied inconsistently in the UI

**Status:** Not implemented.

**Issue:** Viewer, maintainer, and super-admin controls were shown/disabled inconsistently across projects, repositories, S3 buckets, and access keys.

**Planned solution:** Define one role/action capability matrix and use it consistently for visibility and disabled-state guidance while keeping backend authorization authoritative.

### W7. Git mirror settings were unsupported in the settings UI

**Status:** Not implemented.

**Issue:** Git mirrors could not update their supported credential setting, while unrelated package-mirror fields could be exposed.

**Planned solution:** Add Git to the mirror-settings discriminator, keep upstream URL read-only, expose only credentials, and hide unsupported filters/drift/scan controls.

### W8. The S3 browser silently stopped at 1,000 objects

**Status:** Not implemented.

**Issue:** The browser used one `limit=1000` request and did not follow `next_marker`, so large buckets appeared incomplete.

**Planned solution:** Use marker-driven infinite pagination, aggregate/deduplicate objects and prefixes, display partial totals until fully loaded, and invalidate all pages after mutations.

## Bootstrap, CI, and build

### B1. Supported repository types and severity normalization were inconsistent

**Status:** Not implemented.

**Issue:** Bootstrap/API/database validation duplicated repository-type lists and could reject documented Go/npm/Maven types. Documented uppercase severity values were not normalized consistently.

**Planned solution:** Define repository types once in a dependency-free package, compare them bidirectionally with the migrated SQLite CHECK constraint, and normalize severity before validation/storage.

### B2. `govulncheck` could not run on a clean checkout

**Status:** Not implemented.

**Issue:** Go package loading imports embedded `web/dist`, but the workflow did not build the frontend first; clean checkout analysis failed before vulnerability scanning.

**Planned solution:** Install Node in the workflow, run `npm ci` and the production web build first, then run govulncheck on a filtered Go package list and retain SARIF validation.

### B3. Web build and Playwright launch scripts depended on POSIX shell syntax

**Status:** Not implemented.

**Issue:** Inline `mkdir -p`, `cp`, `cd && export`, and `mktemp` commands made build/E2E helpers shell-dependent and brittle.

**Planned solution:** Replace them with tested Node scripts for Swagger copying and E2E server startup, using explicit subprocesses, temporary-directory cleanup, signal forwarding, and Linux-container execution.

### B4. The Docker release builder did not match the pinned Go toolchain

**Status:** Not implemented.

**Issue:** The release image used `golang:1.26-alpine` while `go.mod` and CI pinned Go 1.25.12.

**Planned solution:** Pin the builder image to Go 1.25.12, assert the compiler version during the image build, then build and health-check the release container.

### B5. The OCI-to-Helm mirror test fixture had a data race

**Status:** Not implemented.

**Issue:** `TestOCIManifestPut_MirrorsHelmToTraditional` shared a plain `int64` counter between goroutines, making race verification red and potentially masking real races.

**Planned solution:** Replace the fixture counter with `atomic.Int64`, repeatedly run the focused race test, then run the complete OCI race suite.

## Worktree reference

The unmerged implementation history is preserved on branch `codex/fix-full-review` in `.worktrees/fix-full-review`. The main implemented-data sequence ends at `9901a9b1`; a final narrow Go proxy/Git status adjustment was left uncommitted when work was stopped. Nothing from that branch is included in `main` by this report.
