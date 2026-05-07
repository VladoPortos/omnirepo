<!--
Thanks for the change! Fill in the sections below before requesting
review. Delete sections that genuinely don't apply (most should).
-->

## Summary

<!-- One paragraph: what changes and why. Link to the issue / phase /
finding ID this addresses (e.g. F-12.1, audit #14, phase 7). -->

## Type of change

<!-- Tick the one that fits. If two fit, prefer the riskier one. -->
- [ ] Bug fix (non-breaking)
- [ ] New feature (non-breaking)
- [ ] Breaking change (API, on-disk layout, or config schema)
- [ ] Refactor (no behaviour change)
- [ ] Performance
- [ ] Documentation only
- [ ] CI / tooling

## Test plan

<!--
Required. Be concrete. "Tested locally" is not a test plan.

Examples:
- `make test` green at <commit>.
- Added unit test in internal/protocol/rpm/mirror_test.go covering
  the xz-compressed primary.xml path.
- Manual: `crane push localhost:8080/foo/bar:1 ./image.tar` round-trip
  succeeds, digest stable across two pushes.
- Playwright: `cd web && npx playwright test admin-trash` green.
-->

## Affected protocols / surfaces

<!-- Tick everything this PR touches. -->
- [ ] OCI / Docker registry
- [ ] RPM
- [ ] APT / Debian
- [ ] PyPI
- [ ] Helm
- [ ] Raw blobs
- [ ] S3 (SigV4)
- [ ] Git Smart-HTTP
- [ ] Storage / CAS
- [ ] Auth / RBAC / session
- [ ] Trivy / scan pipeline
- [ ] Frontend (React)
- [ ] CI / workflows / Dockerfile
- [ ] Docs

## Security impact

<!--
Required if any of these are touched: auth, RBAC, crypto (argon2id, JWT,
SigV4, PGP, TLS), storage layout, mirror logic, scan pipeline. Otherwise
"None" is acceptable.

Be specific:
- Does this widen the auth surface? Add a route reachable without auth?
- Does it change what data lands in audit logs?
- Does it change how passwords / tokens / secrets are handled at rest?
- Does it change Trivy invocation or scan results?
-->

None.

## Air-gap impact

<!-- Required. OmniRepo's no-outbound-network-at-runtime invariant is
non-negotiable. -->
- [ ] No new outbound HTTP/DNS calls at runtime.
- [ ] `make test-airgap` passes locally.

## Checklist

- [ ] Tests added or updated for every code change in this PR.
- [ ] `make lint` clean.
- [ ] `make test` green locally.
- [ ] Conformance suite for affected protocols run locally if relevant
      (`make conformance-oci`, `conformance-all`, `conformance-s3`,
      `conformance-git`).
- [ ] No vendored deps changed without `make vendor` re-running.
- [ ] No new dependency outside Apache-2.0-compatible licences.
- [ ] CHANGELOG.md updated under `[Unreleased]` if user-visible.
- [ ] Docs / runbook updated if operator-visible.
- [ ] No AI co-author or "Generated with…" footers in commits.
