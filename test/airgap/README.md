# Air-gap boot test (Phase 1)

`TestAirGapBoot` boots the full `omnirepo` binary in-process against a
`t.TempDir()` data root and a synthetic `bootstrap.json`, injects ephemeral
127.0.0.1 listeners for HTTP and HTTPS via `app.RunOptions`, and asserts that
`/healthz` and `/readyz` return 200 on both schemes.

This is the earliest Phase-1 guardrail against pitfall **P6** (air-gap
regressions). Every request target is `127.0.0.1`; any outbound dial a future
change introduces during startup would surface as a test failure when run in
a `--network=none` container.

Phase 5 extends this with a Playwright E2E scenario running under a
`--network=none` Docker container (see spec §14). The gate wired into CI is:

```
make test-airgap    # go test -mod=vendor ./test/airgap/...
```

Required-pass in `.github/workflows/ci.yml`. This runtime test is the
single source of truth for the air-gap invariant — the earlier
`grep-cdn` static-text gate was retired in v1.2 because it produced
false positives on bundled third-party library URLs (Swagger UI RFC
links, React error-page URLs, W3C / JSON Schema namespace URIs,
license-comment homepage fields) without catching the real invariant.
