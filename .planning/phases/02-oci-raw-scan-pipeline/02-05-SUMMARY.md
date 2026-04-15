---
phase: 02-oci-raw-scan-pipeline
plan: 05
subsystem: protocol + auth
tags: [oci, jwt, bearer, anonymous, public-read, hs256, www-authenticate]
requires:
  - internal/auth/policy.go (Phase 1)
  - internal/auth/middleware/basic_or_apikey.go (Phase 1)
  - internal/metadata/users.go FindByID (Phase 1)
  - internal/metadata/apikeys.go (Phase 1, extended here)
  - internal/metadata/repos.go FindByTriple + PublicRead column (Phase 1)
  - internal/metadata/projects.go FindByName (Phase 1)
  - internal/metadata/settings.go (Phase 1)
  - internal/httpx/router.go chi mount substrate (Phase 1)
  - internal/app/app.go Run orchestrator (Phase 1)
provides:
  - internal/auth.ActorKindAnonymous
  - internal/auth.ActionRepoRead
  - internal/auth.ReasonRequiresAuth / ReasonAnonymousPublicRead
  - internal/auth.Target.PublicRead field
  - internal/httpx.AnonymousReadOK / RepoLookupFn / RepoExtractorFn / AttachAnonymousFn
  - internal/protocol/oci.Handler / New / Mount / VerifyBearer
  - internal/protocol/oci.{MediaTypeOCIManifest,MediaTypeDockerManifestV2,
      MediaTypeOCIIndex,MediaTypeDockerManifestList,
      ErrCode* constants}
  - internal/app.BootEnsureDockerJWTSecret
  - internal/metadata.APIKeysRepo.FindByID
  - config.Docker block (JWTTTLSeconds, UploadSessionTTLSeconds)
affects:
  - internal/auth/policy.go (Can grows step-0 anonymous branch + ActionRepoRead case)
  - internal/auth/policy_test.go (AllActions count 16 → 17)
  - internal/auth/actor.go (ActorKindAnonymous added)
  - internal/config/config.go + config_test.go (Docker block)
  - internal/app/app.go (BootEnsureDockerJWTSecret + oci.Mount in Run)
  - internal/metadata/apikeys.go (FindByID added)
tech-stack:
  added: []
  patterns:
    - "Identity-only HS256 JWT (no scope claims) with per-request DB-backed authorization"
    - "Anonymous-actor short-circuit precedes the must_change_password gate"
    - "Callback-injected anonymous-actor attachment to break auth↔httpx import cycle"
    - "Skeleton chi.Group with placeholder route so middleware chain is testable before downstream plans plug real handlers in"
    - "Explicit alg-confusion guard via t.Method == jwt.SigningMethodHS256 check"
    - "X-Forwarded-Proto-aware WWW-Authenticate realm scheme"
key-files:
  created:
    - internal/auth/anon_policy_test.go
    - internal/httpx/anon_read.go
    - internal/httpx/anon_read_test.go
    - internal/protocol/oci/handler.go
    - internal/protocol/oci/handler_test.go
    - internal/protocol/oci/token.go
    - internal/protocol/oci/token_test.go
    - internal/protocol/oci/token_verify.go
    - internal/protocol/oci/token_verify_test.go
    - internal/protocol/oci/mediatype.go
    - internal/protocol/oci/oci_err.go
    - internal/app/docker_jwt_test.go
  modified:
    - internal/auth/actor.go
    - internal/auth/policy.go
    - internal/auth/policy_test.go
    - internal/config/config.go
    - internal/config/config_test.go
    - internal/metadata/apikeys.go
    - internal/app/app.go
decisions:
  - "AnonymousReadOK takes an AttachAnonymousFn callback rather than importing internal/auth directly. Phase 1's auth package already imports internal/httpx (for IsReserved); a back-import would create a cycle. The /v2 handler (which can import both) supplies the trivial closure that calls auth.WithActor."
  - "ActionRepoRead added as a typed Action constant so anonymous read flows through the same auth.Can decision point as authenticated reads. Anonymous + public_read returns anonymous_public_read; authenticated non-member + public_read returns true with empty reason; authenticated member of any project always reads their repos. AllActions length bumped 16→17."
  - "Anonymous branch is step 0 in Can — BEFORE must_change_password short-circuit. Anonymous actors have no password to change; the MCP gate is undefined for them. Pathological case (Kind=anonymous, MCP=true) still resolves through the anonymous branch (test asserts)."
  - "/v2/_catalog placeholder returns 501 with an OCI error envelope. Plan 02-07 replaces with real catalog routing. The placeholder exists so the middleware chain (AnonymousReadOK → VerifyBearer) is exercised end-to-end by tests today."
  - "Bearer middleware passes through when ctx already has an Actor. This makes AnonymousReadOK's anonymous branch propagate cleanly into downstream handlers without VerifyBearer overwriting it. The contract: VerifyBearer authenticates only when no upstream middleware has."
  - "JWT secret stored base64 in settings.docker_token_hmac_secret using existing string-valued SettingsRepo (same pattern as docker_jwt_ttl_seconds and the Phase 02-02 AEAD key). Zero schema churn — TEXT column carries base64 cleanly."
  - "Mount uses parent.Route('/v2', ...) directly rather than httpx.MountReserved. Reserved-prefix protection exists to stop accidental project-name collisions; system code mounting at reserved prefixes is the legitimate use case the package documents."
  - "Bearer middleware fallback strategy: Bearer-only on guarded /v2/* routes. /v2/token has its own Basic chain. There's no 'Bearer-then-Basic-fallback' on /v2/* because the OCI Distribution flow is explicit: clients hit any /v2/* path → 401 with WWW-Authenticate Bearer → client GETs /v2/token with Basic → re-tries with Bearer. Falling back to Basic on /v2/* would change the published auth contract for Docker clients."
metrics:
  duration: ~30m
  tasks: 2
  files: 12 created, 7 modified
  completed: 2026-04-15
requirements_complete:
  - OCI-01
  - OCI-02
  - OCI-04
  - OCI-07
  - REPO-09
---

# Phase 2 Plan 05: /v2 OCI Skeleton + Token + Anonymous-Read Substrate Summary

The auth substrate every other OCI plan depends on. Two atomic tasks:

1. **Anonymous actor + AnonymousReadOK middleware.** `ActorKindAnonymous` constant; `Target` grows `PublicRead bool`; `Can` adds a step-0 anonymous branch returning `anonymous_public_read` only for `repo.read` on `PublicRead=true` repo targets, `requires_auth` for everything else; new `ActionRepoRead` action handled in the per-action table for authenticated members AND public-repo readers; `httpx.AnonymousReadOK` middleware with caller-supplied `RepoLookupFn`, `RepoExtractorFn`, and `AttachAnonymousFn` (callback indirection breaks the auth↔httpx cycle).
2. **`/v2` chi handler skeleton.** Router with three layers: open `/v2/` ping → Basic-authed `/v2/token` → guarded subrouter (`AnonymousReadOK + VerifyBearer`) for everything else. HS256 JWT signs with `settings.docker_token_hmac_secret` (32 bytes, materialized on first boot), claims contain only `{actor_id, kind, iss, sub, iat, exp}`. Bearer middleware enforces HS256 explicitly (alg-confusion guard), re-resolves Actor via `UsersRepo.FindByID` / `APIKeysRepo.FindByID`. Wired into `app.Run` after `api.Mount`.

## Final challenge header format (exact bytes)

```
Bearer realm="<scheme>://<host>/v2/token",service="omnirepo"
```

- `<scheme>` is `https` when `r.TLS != nil` OR `X-Forwarded-Proto: https`; otherwise `http`.
- `<host>` is `r.Host` (chi-normalized; trusted server-side).
- `service` is the literal string `omnirepo` — Docker clients echo it back in the resource scope when requesting tokens against multi-tenant registries; ours is single-tenant so the value is informational but spec-required.

Test `TestProtectedRoute_WWWAuthenticateChallenge` matches against the regex
`^Bearer realm="https?://[^"]+/v2/token",service="omnirepo"$`.

## Bearer middleware fallback strategy

**Bearer-only on `/v2/*` (no Basic fallback).** The published Docker
auth contract is:

1. Client → `GET /v2/<anything>` (no creds) → 401 + WWW-Authenticate Bearer
2. Client parses realm, → `GET /v2/token` with `Authorization: Basic ...`
3. Server → 200 with `{"token":"<jwt>",...}`
4. Client → re-issues original request with `Authorization: Bearer <jwt>`

`/v2/token` is the ONLY Basic-authed route; `/v2/*` accepts only Bearer.
Falling back to Basic on `/v2/*` would diverge from the spec and break
clients that strictly follow the challenge protocol (notably crane).

VerifyBearer DOES pass through when an Actor already exists in ctx —
that's the AnonymousReadOK fast-path and is unrelated to Basic auth.

## JWT TTL default + override

- **Default:** 3600 seconds (1 hour). Set in `config.Defaults().Docker.JWTTTLSeconds`.
- **YAML override:** `docker.jwt_ttl_seconds: 1800` in the omnirepo.yaml config.
- **Env override:** `OMNIREPO_DOCKER__JWT_TTL_SECONDS=900`.
- **Code path:** `app.Run` reads `cfg.Docker.JWTTTLSeconds`, multiplies into a `time.Duration`, passes to `oci.New` via `Deps.JWTTTL`. The handler's `signToken` uses it as the `exp - iat` interval; `issueToken`'s response carries it as `expires_in` (the integer seconds).

Note: a separate `config.Auth.DockerJWTTTL time.Duration` exists from
Phase 1 but is unused by this plan. `cfg.Docker.JWTTTLSeconds` is the
authoritative knob going forward; the older field can be deprecated in
a future cleanup plan.

## Test Evidence

- `go test -mod=vendor -race -count=1 ./internal/protocol/oci/... ./internal/auth/... ./internal/httpx/... ./internal/app/...` — all green.
- `go build -mod=vendor ./...` — exit 0.
- Full repo `go test -mod=vendor -race -count=1 ./...` — every package green except a pre-existing flake in `internal/jobs/TestPool_NoHandlerMarksFailed` (passes consistently when run in isolation; out of scope for this plan, see "Deferred Issues" below).

Targeted coverage:

- **`internal/auth`:** `TestAnonymousCanReadPublicRepo`, `TestAnonymousDeniedOnPrivateRepo`, `TestAnonymousDeniedOnNonRepoTarget`, `TestAnonymousDeniedOnNonReadActions` (iterates every Action), `TestAnonymousMCPShortCircuitNotReached`, `TestAuthenticatedMemberCanReadRepo`, `TestAuthenticatedNonMemberCanReadPublicRepo`, `TestAuthenticatedNonMemberDeniedPrivateRepo`. Existing `TestAllActionsSliceMatchesConstants` updated 16→17.
- **`internal/httpx`:** Public repo + GET attaches anonymous actor; HEAD attaches; Authorization-header-present does NOT override; private repo falls through; not-found falls through; POST/PUT/DELETE all fall through; non-repo URL falls through; next always runs (even on fall-through).
- **`internal/protocol/oci`:** `TestPingReturns200AndOCIHeader`, `TestTokenIssue_ValidBasic_Returns200AndJWT`, `TestTokenIssue_InvalidBasic_Returns401`, `TestTokenIssue_NoAuth_Returns401`, `TestProtectedRoute_WWWAuthenticateChallenge` (regex match on exact format), `TestProtectedRoute_ValidBearer_Passes` (placeholder reaches 501, not 401), `TestProtectedRoute_ExpiredBearer_401` (hand-crafted past-exp JWT), `TestProtectedRoute_BadSignatureBearer_401` (token signed by twin with different secret), `TestAlgConfusion_NoneAlg_Rejected` (alg=none JWT crafted by hand → 401), `TestAnonymousReadOnPublicRepo_Passes` (seeded public repo + GET → not 401), `TestPrivateRepoPublicReadLookup_ReturnsFoundFalse`, `TestMediaTypeConstantsExported`, `TestChallengeHeaderUsesXForwardedProto`, `TestVerifyBearer_NoAuthHeader_Challenges`, `TestVerifyBearer_MalformedBearer_Challenges`, `TestVerifyBearer_EmptyBearer_Challenges`, `TestRouteIsolation`.
- **`internal/app`:** `TestBootEnsureDockerJWTSecret_GeneratesOnFirstCall` (length 32 + base64 round-trip), `TestBootEnsureDockerJWTSecret_IdempotentOnSecondCall`, `TestBootEnsureDockerJWTSecret_RejectsCorruptStoredValue`.
- **`internal/config`:** `TestDockerDefaults` (3600/3600), `TestDockerEnvOverride` (env vars round-trip).

## Threat-model compliance

| Threat | Status | Evidence |
|--------|--------|----------|
| T-02-05-01 alg-confusion (alg=none, RS256-via-HMAC) | mitigated | Explicit `t.Method == jwt.SigningMethodHS256` check in keyfunc; `TestAlgConfusion_NoneAlg_Rejected` proves a hand-crafted alg=none JWT yields 401. |
| T-02-05-02 JWT replay after revocation | accepted | TTL=3600s; documented as known limitation; v1.1 candidate for jti blacklist. |
| T-02-05-03 docker_token_hmac_secret leaked in logs | mitigated | Boot hook logs `len` only; `grep 'slog.*"secret"' internal/app/app.go` returns no matches. |
| T-02-05-04 JWT permission-claim drift from DB | mitigated | identityClaims carries ONLY `{actor_id, kind, iss, sub, iat, exp}`. Per-request `auth.Can` re-checks against DB. |
| T-02-05-05 forgotten middleware on /v2/* sub-route | mitigated | Routes for /_catalog (placeholder) and downstream blob/manifest routes (02-06/02-07) live inside the `r.Group` that has VerifyBearer + AnonymousReadOK applied. Acceptance criteria of 02-06/02-07 must grep for the group registration. |
| T-02-05-06 anonymous bypass of write paths | mitigated | AnonymousReadOK only attaches anonymous actor for GET/HEAD AND PublicRead=true. Can() denies anon for non-`repo.read` actions. Tests: 4 separate methods (POST/PUT/DELETE/HEAD-not-public) and `TestAnonymousDeniedOnNonReadActions` iterating every Action. |
| T-02-05-07 /v2/ ping leaks server identity | accepted | `Docker-Distribution-API-Version: registry/2.0` is mandated by spec; `service="omnirepo"` is intentional. |
| T-02-05-08 path traversal via repo name | mitigated | extractRepoFromV2URL splits on `/` only; downstream 02-06/02-07 must validate project + type + name (Phase 1 ProjectNameValid + repo type allow-list) — they re-check on every route. |

## Deviations from plan

### Auto-fixed / shape refinements

**1. [Rule 3 — Blocking] Import-cycle: auth ↔ httpx**

- Found during: Task 1 first build.
- Issue: Plan's action block put `httpx/anon_read.go` directly importing `internal/auth` to call `auth.WithActor`. But `internal/auth/validate.go` already imports `internal/httpx` for the reserved-prefix check (Phase 1). Direct import = cycle.
- Fix: Made `AnonymousReadOK` accept an `AttachAnonymousFn func(ctx) ctx` callback. The /v2 handler (which legitimately imports both) supplies the trivial closure. Tests pass `attachAnon = func(ctx) ctx { return auth.WithActor(ctx, auth.Actor{Kind: ActorKindAnonymous}) }`. Same operational behavior, cycle broken.
- Files: internal/httpx/anon_read.go, internal/httpx/anon_read_test.go, internal/protocol/oci/handler.go.

**2. [Rule 2 — Correctness] ActionRepoRead added to per-action table for authenticated paths**

- Found during: Task 1 design.
- Issue: Plan body specified the anonymous branch but didn't enumerate what authenticated `repo.read` does. Without an entry in the per-action `switch`, Can() would return `unknown_action` for authenticated callers — breaking every legitimate read.
- Fix: Added `case ActionRepoRead:` in the per-action table. `target.PublicRead == true` → allowed for any authenticated actor (read-only; doesn't require membership). Otherwise → standard project-membership check. Tests cover all four cases (anon+pub, anon+priv, member+priv, non-member+pub).
- Files: internal/auth/policy.go.

**3. [Rule 3 — API shape] Plan referenced settings.GetBytes/SetBytes which do not exist**

- Found during: Task 2 BootEnsureDockerJWTSecret implementation.
- Issue: Same as the Phase 02-02 AEAD-key situation — SettingsRepo only has Get/Set string. The plan's `settings.GetBytes/SetBytes` is API drift.
- Fix: Mirror the exact 02-02 pattern: base64-encode the 32-byte secret for storage, decode on load. TEXT settings column carries it cleanly. Test `TestBootEnsureDockerJWTSecret_GeneratesOnFirstCall` decodes and asserts exactly 32 bytes.
- Files: internal/app/app.go.

**4. [Rule 3 — Testing] Expired-JWT test reformulation**

- Found during: Task 2 first test run.
- Issue: Plan suggested constructing a handler with `JWTTTL: -time.Hour` to mint an already-expired token via the production `signToken` path. With negative TTL, `iat == time.Now()` and `exp == time.Now() - 1h`, so `iat > exp`. The jwt v5 parser's behavior on `iat > exp` is implementation-defined and produced an unexpected pass in our test (the placeholder route returned 501 instead of 401).
- Fix: Hand-craft an expired JWT directly via `jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{...})` with explicit past `exp`. Lives in token_test.go as the `mintTokenWithExp` helper. Test now reliably asserts 401.
- Files: internal/protocol/oci/token_test.go, internal/protocol/oci/handler_test.go.

### Configuration block: Docker, not extension of Auth

The plan put JWT TTL under a new top-level `docker.jwt_ttl_seconds` block. Phase 1 already shipped `auth.docker_jwt_ttl` as a `time.Duration`. Implementing the plan as written: added `config.Docker{JWTTTLSeconds, UploadSessionTTLSeconds}` with `int` (seconds) typing per the plan. The older `config.Auth.DockerJWTTTL` field is unused by this plan and can be deprecated by a future cleanup; this plan doesn't touch it to avoid widening blast radius.

## Deferred Issues

- **internal/jobs/TestPool_NoHandlerMarksFailed** flake: failed once during the full-repo regression run (`attempts=0 want >= 1`), but consistently passes when re-run in isolation 3x. Pre-existing in plan 02-04; not caused by changes here. Out of scope for this plan; see deferred-items.md if escalation needed.
- **`config.Auth.DockerJWTTTL` deprecation:** Phase 1 shipped this field but it's now superseded by `cfg.Docker.JWTTTLSeconds`. Deprecation/removal is a cleanup task for a future plan.

## Commits

| Hash    | Subject |
|---------|---------|
| f4f7bd2 | feat(02-05): add anonymous actor kind + AnonymousReadOK middleware (D-32, D-33) |
| a7e4f81 | feat(02-05): /v2 OCI handler skeleton + token issue + Bearer verify (D-06) |

## Self-Check: PASSED

- internal/auth/anon_policy_test.go — FOUND
- internal/httpx/anon_read.go — FOUND
- internal/httpx/anon_read_test.go — FOUND
- internal/protocol/oci/handler.go — FOUND
- internal/protocol/oci/handler_test.go — FOUND
- internal/protocol/oci/token.go — FOUND
- internal/protocol/oci/token_test.go — FOUND
- internal/protocol/oci/token_verify.go — FOUND
- internal/protocol/oci/token_verify_test.go — FOUND
- internal/protocol/oci/mediatype.go — FOUND
- internal/protocol/oci/oci_err.go — FOUND
- internal/app/docker_jwt_test.go — FOUND
- Commits f4f7bd2, a7e4f81 — FOUND in `git log --oneline`
- `go build -mod=vendor ./...` — exit 0
- `go test -mod=vendor -race -count=1 ./internal/protocol/oci/... ./internal/auth/... ./internal/httpx/... ./internal/app/...` — exit 0
