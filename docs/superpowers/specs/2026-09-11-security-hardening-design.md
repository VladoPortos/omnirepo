# Security Hardening Design

## Scope

Address review findings 1, 2, 4, 5, and 8, plus make the plaintext HTTP listener optional. Keep existing public APIs compatible except where a request is actively unsafe or under authentication pressure.

## Design

RAW artifact responses classify browser-active media types as downloads. HTML, XHTML, XML, and SVG receive `application/octet-stream`, an attachment disposition, `X-Content-Type-Options: nosniff`, and a sandbox CSP. Other artifact media types retain their current inline behavior.

Password authentication uses one process-wide `auth.AttemptLimiter` constructed by `app.Run` and injected into REST and protocol authentication dependencies. It uses socket peer IP only, a bounded token-bucket map, and a fixed-capacity permit channel around Argon2 work. API-key paths do not consume permits. Rejected attempts return HTTP 429 and `Retry-After` without performing Argon2 work.

`server.http_enabled` defaults to true for compatibility. When false, `app.Run` neither binds nor serves the HTTP listener; HTTPS remains required. Tests may still inject listeners explicitly only when the corresponding listener is enabled.

All application-controlled upstream HTTP traffic uses an SSRF-guarded transport. The guard checks literal and DNS-resolved addresses at dial time, rejecting loopback, private, link-local, unspecified, and multicast destinations. Redirects inherit the guarded transport. External OCI pulls receive the same transport through go-containerregistry's remote options.

Scan rescan and prune mutations require the same maintainer-level `repo.update` authorization as other repository mutations. Read-only scan endpoints continue to allow project viewers.

Administrative user patching moves into one metadata transaction. It validates the last-live-superadmin invariant, applies all requested fields, updates password timestamps and forced-change state, and deletes sessions in the same transaction. The handler hashes before starting the transaction and reports an invariant violation as HTTP 409.

## Verification

Each behavior receives a regression test written and observed failing before production changes. Targeted tests run after every change. Completion requires the full Go suite in a Debian Go container, UI build, Docker image build, and `govulncheck`. The existing pinned govulncheck workflow remains the canonical CI integration.
