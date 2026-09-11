# Security Hardening Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close five confirmed security findings and make plaintext HTTP optional without breaking established artifact workflows.

**Architecture:** Add focused boundary controls at artifact response, authentication, outbound network, authorization, and metadata transaction layers. Construct shared controls in `app.Run` and inject them through existing dependency bundles.

**Tech Stack:** Go 1.26, chi, SQLite, go-containerregistry, React/Vite, Docker.

**Spec:** `docs/superpowers/specs/2026-09-11-security-hardening-design.md`

## Global Constraints

- Work only on `codex/security-hardening` in the isolated worktree.
- Preserve backward compatibility except for unsafe behavior.
- Write and observe each regression test failing before implementation.
- Do not push or create a pull request without user direction.

---

### Task 1: Neutralize active RAW content

**Files:** Modify `internal/protocol/raw/get.go`; test `internal/protocol/raw/handler_test.go`.

**Interfaces:** `serveFile` continues serving the same bytes; active media becomes an attachment.

- [x] Add a public-RAW integration test that uploads HTML and asserts octet-stream, attachment, nosniff, sandbox CSP, and unchanged bytes.
- [x] Run the test and confirm it fails on the missing headers/type.
- [x] Add an active-content classifier and safe response headers.
- [x] Run all RAW tests.

### Task 2: Bound password-authentication work

**Files:** Create `internal/auth/attempt_limiter.go` and test; modify API/middleware dependency wiring and login/Basic handlers; modify `internal/app/app.go`.

**Interfaces:** `AttemptLimiter.Acquire(peer string) (release func(), retryAfter time.Duration, ok bool)` grants a rate/global-cap permit.

- [x] Test token exhaustion, refill, entry bounds, and concurrent permit exhaustion using a fake clock.
- [x] Run tests and confirm the missing type failure.
- [x] Implement the limiter and inject one shared instance.
- [x] Add handler tests proving throttled REST and Basic attempts return 429 without reaching password verification.
- [x] Run auth, middleware, API, and app tests.

### Task 3: Make HTTP optional

**Files:** Modify `internal/config/config.go`, config tests/examples, `internal/app/app.go`, and app tests.

**Interfaces:** `server.http_enabled` / `OMNIREPO_SERVER__HTTP_ENABLED`, default true.

- [x] Add config and app tests proving false skips HTTP binding while HTTPS remains available.
- [x] Run tests and confirm failure.
- [x] Add the field, defaults, documentation, and conditional listener lifecycle.
- [x] Run config and app tests.

### Task 4: Enforce outbound destination policy

**Files:** Create `internal/httpx/ssrf_transport.go` and tests; modify sync and OCI pull wiring.

**Interfaces:** `httpx.NewSafeTransport(base *http.Transport) *http.Transport` returns a cloned transport with guarded dialing; `httpx.ValidateOutboundHost` rejects prohibited literals.

- [x] Add deterministic tests for prohibited IPv4/IPv6 literals and a dial-time resolver test.
- [x] Run tests and confirm missing API failure.
- [x] Implement address classification and guarded dialing while preserving proxy/TLS settings.
- [x] Inject the transport into mirror clients and external OCI remote options.
- [x] Add integration tests proving loopback pull/mirror attempts are rejected.
- [x] Run HTTP, mirror, Helm, Git, and OCI tests.

### Task 5: Require maintainer rights for scan mutations

**Files:** Modify `internal/api/scans.go` and `internal/api/scans_test.go`.

**Interfaces:** Existing `repo.update` policy determines mutation access.

- [x] Add tests showing viewers receive 403 from artifact rescan, repository rescan, and prune while maintainers succeed.
- [x] Run tests and confirm viewer requests currently succeed.
- [x] Replace membership-only mutation checks with canonical policy checks.
- [x] Run API scan tests.

### Task 6: Make administrative user patch atomic

**Files:** Modify `internal/metadata/users.go`, metadata tests, `internal/api/admin_users_full.go`, and API tests.

**Interfaces:** Add an `AdminUserPatch` value and `UsersRepo.ApplyAdminPatch(ctx, id, patch) error` returning `ErrLastSuperAdmin` when appropriate.

- [x] Add metadata tests for last-admin demotion, two-admin demotion, atomic field updates, and transactional session revocation.
- [x] Run tests and confirm missing behavior.
- [x] Implement one writer transaction and update the handler to call it.
- [x] Add API tests for HTTP 409 and complete password-reset behavior.
- [x] Run metadata and API tests.

### Task 7: Full verification

**Files:** No additional production files.

- [x] Build the web UI in Docker.
- [x] Run `go test ./...` in the Debian Go container.
- [x] Run `govulncheck -scan symbol ./...` in a disposable Go container.
- [x] Build the production Docker image and smoke-test HTTPS-only health.
- [x] Review `git diff --check`, branch status, and final diff.
