# UAT (User Acceptance Tests)

This package holds end-to-end user-acceptance tests for the OCI/RAW/scan
pipeline that drive the full omnirepo binary against **real external
clients** and **real upstream registries** (Docker Hub). They exercise the
five UAT items for that pipeline.

All tests are gated behind the `uat` build tag and are therefore excluded
from the default `make test` / CI merge gate. They exist to be run on
demand by developers (and a UAT runner in the nightly/manual lane) after
installing the required host binaries.

## Required host binaries

| Binary   | Version | Install |
|----------|---------|---------|
| `docker` | 24+     | System package / Docker Desktop |
| `crane`  | v0.21.5 | `go install github.com/google/go-containerregistry/cmd/crane@v0.21.5` |
| `trivy`  | v0.69.x | Download a release tarball from <https://github.com/aquasecurity/trivy/releases> |

Each binary is resolved from `$PATH`; tests call `t.Skip` when a required
binary is missing, so a developer with only `crane` installed can still run
the non-docker tests.

## Network

Outbound network access is required. Tests pull image metadata + layers
from `registry-1.docker.io`. The air-gap invariant applies only to the
running server process at rest — pulling seed images from a real upstream
is what "UAT" means here.

## Running

```
go test -mod=vendor -tags=uat -count=1 ./test/uat/...
```

Flake tolerance: upstream registries occasionally rate-limit anonymous
pulls. A single retry is fine; a second flake is a logged failure.
