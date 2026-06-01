# Contributing

Thanks for working on OmniRepo. This document is the short version of
"how to land a change here without breaking things". Read once before
your first PR; revisit when something below changes.

## Quick start

```bash
# One-time setup
make vendor                   # ensures vendor/ matches go.mod / go.sum

# Day-to-day
make dev                      # live-reload Go (air) + Vite frontend
make test                     # unit tests + air-gap boot test + lint
make build                    # produce ./bin/omnirepo
make docker                   # build the multi-stage production image

# Targeted suites (only what you touched)
make conformance-oci          # OCI Distribution conformance (crane-driven)
make conformance-all          # rpm + deb + pypi + helm via DinD
make conformance-s3           # SigV4 + aws-cli E2E
make conformance-git          # Git Smart-HTTP via real git CLI
make bench-sqlite             # SQLite contention bench (D-42 gate)
make bench-git                # git clone memory bench (TEST-07 gate)
```

The full target list lives in the `Makefile` header.

## Hard rules

These are not stylistic preferences — CI enforces every one of them.

1. **Every feature ships with tests.** No exceptions. A PR that adds
   behaviour without tests covering it does not merge. Bug fixes ship
   with a regression test that fails before the fix and passes after.
2. **`make test` must be green before you push.** Locally, in CI, on
   any branch you ask others to look at.
3. **Vendor mode.** Every Go invocation uses `-mod=vendor`. After
   bumping a dep, run `make vendor` and commit the resulting tree.
4. **No outbound network at runtime.** OmniRepo is air-gap-first. The
   binary makes zero outbound calls without explicit user action. Any
   change that violates this fails `make test-airgap`.
5. **No in-process schedulers.** Sync is triggered by the UI button or
   the `/sync` REST endpoint. Cron / timers / time-based fires are out
   of scope; an external scheduler does the firing.
6. **Apache-2.0-compatible licences only.** Every new direct dep must
   ship under a licence we can ship inside the binary. No AGPLv3
   (MinIO), no GPL-only.
7. **No documentation files unless explicitly required.** README,
   SECURITY, CONTRIBUTING, CHANGELOG, LICENSE, NOTICE, and the
   `docs/operations/` runbooks are the canon. Don't add `*.md` notes
   alongside source.

## Commit and branch hygiene

- Branch names are descriptive: `feat/<scope>`, `fix/<scope>`,
  `docs/<scope>`, `ci/<scope>`. Worktree-managed in-flight branches
  under any prefix are fine; squash before merge.
- Commit messages follow a Conventional-Commits-ish form already in
  use across the history. The first line is `type(scope): subject`,
  lowercase, no trailing period, ≤ 72 chars. Examples from `git log`:
  - `fix(wt4): rpm mirror parses xz + zstd primary.xml (F-06.1)`
  - `feat(s3): SigV4 RequestTimeTooSkewed window`
  - `ci: add CodeQL, Scorecard, Trivy, govulncheck, Dependabot`
  Allowed types: `feat`, `fix`, `docs`, `ci`, `test`, `refactor`,
  `chore`, `perf`, `build`.
- The body explains *why*, not *what* — the diff already shows what.
  Reference phase IDs, finding IDs, and audit numbers when relevant.
- Squash work-in-progress commits before opening a PR. One logical
  change = one commit, ideally.
- Do not add AI co-author trailers, "Generated with…" footers, or
  similar. The author of a commit is the person who took
  responsibility for it.

## Code style

- **Go** — `gofmt`-clean, `golangci-lint` clean (`make lint`). The
  ruleset is in `.golangci.yml`. New lints are opt-in conversations,
  not surprise-merges.
- **TypeScript / React** — Tailwind utility classes; shadcn/ui
  components live in `web/src/components/ui/` and are owned by the
  repo (no upstream re-import). Avoid one-off CSS files.
- **No comments that paraphrase the code.** Comment only when the
  *why* is non-obvious — a hidden constraint, an upstream bug
  workaround, an invariant that costs ten minutes to re-derive.
- **No premature abstractions.** Three similar lines beat a generic
  helper used once. Refactor when the third instance lands.

## Test layering

| Suite                     | What it covers                                        | Speed  |
| ------------------------- | ----------------------------------------------------- | ------ |
| `go test ./...`           | unit + small integration                              | fast   |
| `make test-airgap`        | "no network at runtime" invariant                     | fast   |
| `make conformance-oci`    | crane vs the registry                                 | medium |
| `make conformance-all`    | dnf, apt, pip, helm DinD clients                      | slow   |
| `make conformance-s3`     | aws-sdk-go-v2 + aws-cli SigV4                         | medium |
| `make conformance-git`    | real `git` CLI clone/push/fetch                       | medium |
| `make bench-sqlite`       | 30 s × 16 workers, zero `SQLITE_BUSY`                 | medium |
| `make bench-git`          | git clone memory bench, peak RSS < 3× repo size       | slow   |
| Playwright E2E (in `web/`)| browser-driven UI flows                               | slow   |

Pick the smallest suite that proves your change. CI runs all of them
on every push to `main` and every PR.

## Pull requests

- Open against `main`. There are no long-lived release branches yet.
- Fill in the PR template — the test plan section is not optional.
- Keep PRs focused. Splitting a 12-file refactor into "behaviour
  change" + "rename pass" is almost always worth the extra PR.
- Expect at least one review pass. CI must be green before merge.
- After merge, the worktree branch is deleted. Don't reuse it.

## Reporting bugs and security issues

- **Functional bugs** — open a GitHub issue with reproduction steps
  and the affected commit / version.
- **Security issues** — do *not* open a public issue. Follow
  [SECURITY.md](SECURITY.md).

## Where things live

```
cmd/                — entry points (omnirepo + bench tools)
internal/           — non-importable packages (the bulk of the code)
internal/protocol/  — one subpkg per served protocol
internal/storage/   — CAS + per-protocol layout
internal/auth/      — argon2id, JWT, session, RBAC
test/               — integration + conformance suites
test/conformance/   — DinD matrix, pinned base-image digests
web/                — React 19 + Vite + Tailwind frontend
docs/operations/    — operator-facing runbooks (cron, TLS, etc.)
vendor/             — committed deps; run `make vendor` to refresh
```

When in doubt, grep `git log --oneline` near a similar past change to
see how it was structured.
