# Security Policy

OmniRepo serves containers, OS packages, language packages, Helm charts,
S3-style blobs, and Git repos for closed corporate networks. A bug that
breaks isolation, leaks credentials, or corrupts a served artifact is a
supply-chain bug for everyone pulling from the server. Please report it
before disclosing publicly.

## Supported versions

| Version           | Supported |
| ----------------- | --------- |
| `main`            | ✅         |
| Latest tag (v1.8) | ✅         |
| Older tags        | ❌         |

Once an explicit LTS line exists, this table will switch to N / N-1
support. Until then the previous tag is treated as historical.

## Reporting a vulnerability

**Do not open a public GitHub issue for security bugs.** Use one of:

1. **GitHub Private Vulnerability Reporting** (preferred):
   <https://github.com/VladoPortos/omnirepo/security/advisories/new>
2. **Email fallback**: <vladoportos@gmail.com>

Please include:

- Affected version, branch, or commit SHA
- Reproduction steps or a proof-of-concept
- Observed vs. expected behaviour
- Impact assessment (auth bypass, RCE, data exfiltration, supply-chain
  forgery, denial of service, etc.)
- A suggested fix or mitigation if you have one

**Response targets:**

- Acknowledgement within 5 working days.
- Status update at least every 7 days until the report is closed.
- If you do not hear back in 5 working days, resend to the email above
  with `[OmniRepo SECURITY]` in the subject.

## In scope

- Authentication and authorisation bypass on any served protocol
  (OCI/Docker, RPM, APT, PyPI, Helm, raw blobs, S3 SigV4, Git Smart HTTP)
- Project-scope leakage: reading or writing artifacts in a project the
  authenticated user does not have access to
- Remote code execution in the omnirepo binary or its embedded Trivy
- Cache or mirror poisoning that lets an attacker inject artifacts into
  another tenant's view of an upstream
- Path traversal, symlink, or zip-slip against `/var/lib/omnirepo/`
- Cryptographic flaws in password hashing (argon2id), JWT bearer tokens,
  AWS SigV4 verification, or PGP InRelease signing
- Storage corruption that survives a restart and breaks served metadata
- Unauthenticated denial of service (single-request panics, OOM crashes,
  connection-handling regressions)

## Out of scope (still report, lower priority)

- Findings in third-party libraries with no demonstrated path to exploit
  through the omnirepo binary
- Issues that require admin credentials to begin with
- Missing security headers on endpoints that do not accept user-supplied
  content
- Reports that quote Trivy / CodeQL / Scorecard SARIF output without a
  triaged exploit path — those scanners run on every push and the
  findings are already in the Security tab
- Self-XSS or theoretical CSRF on endpoints with no state-changing effect
- Sustained brute-force from authenticated clients (rate-limit at your
  reverse proxy)
- Network-level attacks against the runtime environment (TLS termination,
  DNS, etc.) that are not OmniRepo-specific

## Disclosure

Coordinated. Once a fix is on `main` and a patch release is tagged, the
advisory is published 7 days later — or sooner if there is evidence of
in-the-wild exploitation. Reporters are credited in the advisory unless
they request otherwise.

## Related security automation

This repository runs the following on every push to `main` and on a
weekly schedule:

- **CodeQL** — Go and JS/TS static analysis ([workflow][codeql])
- **Trivy** — filesystem, Dockerfile, and container image scans ([workflow][trivy])
- **govulncheck** — Go call-graph-aware CVE scan ([workflow][govuln])
- **OpenSSF Scorecard** — supply-chain hygiene ([workflow][scorecard];
  requires a `SCORECARD_TOKEN` PAT to run on this private repo — see
  the workflow header for setup)
- **Dependabot** — gomod, npm, GitHub Actions, Docker base images
  ([alerts][dependabot])

Findings are surfaced in the repository's [Security tab][security].

[codeql]: https://github.com/VladoPortos/omnirepo/actions/workflows/codeql.yml
[trivy]: https://github.com/VladoPortos/omnirepo/actions/workflows/trivy.yml
[govuln]: https://github.com/VladoPortos/omnirepo/actions/workflows/govulncheck.yml
[scorecard]: https://github.com/VladoPortos/omnirepo/actions/workflows/scorecard.yml
[dependabot]: https://github.com/VladoPortos/omnirepo/security/dependabot
[security]: https://github.com/VladoPortos/omnirepo/security
