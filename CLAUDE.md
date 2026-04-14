# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Repository status

This repo currently contains **design documentation only** — no source code, build system, or tests yet. The project (OmniRepo) is in the planning/blueprint phase.

- `README.md` — GitLab-generated boilerplate, not project-specific.
- `tools.md` — the real entry point: a detailed technical blueprint for the system to be built.

When asked to "run tests", "build", etc., there is nothing to run yet. Surface this rather than inventing commands.

## What OmniRepo is (per `tools.md`)

A single Go binary intended to serve multiple artifact protocols on one port:

- **OCI/Docker registry** — embed `google/go-containerregistry/pkg/registry` (or `rogpeppe/ociregistry`) as an `http.Handler`.
- **RPM repo** — parse with `cavaliergopher/rpm`; generate `repomd.xml` + `primary.xml.gz` via `encoding/xml`.
- **APT/Debian repo** — build from scratch (ar archives, Packages/Release/InRelease); sign with `ProtonMail/go-crypto`.
- **PyPI** — PEP 503 Simple API, ~100 lines of HTML generation.
- **Helm** — `helm.sh/helm/v3/pkg/repo` for `index.yaml`.
- **S3-compatible** — embed `johannesboyne/gofakes3` (MinIO is AGPLv3 and uses `internal/`, so it can't be embedded).
- **Git hosting** — go-git v6's new `backend/http` (pure-Go Smart/Dumb HTTP, pre-release Feb 2026).
- **Vulnerability scanning** — embed `golang.org/x/vuln/scan` for Go; run Trivy/Grype as sidecars for other ecosystems.
- **Routing** — `go-chi/chi` to multiplex the protocols on one port.

Architectural principle: **embed as `http.Handler`, don't fork.** Protocol handlers share one storage backend behind pluggable interfaces (`BlobHandler`, gofakes3 `Backend`, etc.). Most package-repo "protocols" are just metadata-over-HTTP and need no special library.

License watch-outs called out in the blueprint: MinIO (AGPLv3), `containers/image` (needs CGo), `distribution/distribution` (unstable interfaces).

Consult `tools.md` before proposing libraries or architecture — it already contains researched choices with rationale, stars, licenses, and update dates.
