# Conformance test binaries (not committed)

This directory hosts vendored CLI binaries the per-protocol conformance
suites drive via `os/exec`. The binaries themselves are **not** committed —
they are installed locally (and in CI) on demand.

## crane (OCI conformance)

Used by `test/conformance/docker/*_test.go` to drive the `/v2` registry.

Pinned version: **v0.21.5** (matches `go-containerregistry` version in
`go.mod`; see plan 02-10).

### Install locally

```
go install github.com/google/go-containerregistry/cmd/crane@v0.21.5
cp "$(go env GOPATH)/bin/crane" test/conformance/bin/crane
chmod +x test/conformance/bin/crane
```

Or download a release tarball from
<https://github.com/google/go-containerregistry/releases/tag/v0.21.5> and
extract `crane` into this directory.

### CI

`.github/workflows/ci.yml` job `conformance-oci` runs the two commands above
before invoking `make conformance-oci`. Fresh install every CI run; nothing
is ever committed.

### Why not commit the binary?

1. Binaries bloat the repo and drift from upstream without a paper trail.
2. `go install` with Go module checksums protects upstream integrity.
3. Running `make conformance-oci` without the binary prints a clear error
   so the developer knows what to do.
