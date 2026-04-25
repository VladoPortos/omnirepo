# Batch 06 — RPM + APT: upload · mirror · drift purge

**Status:** ✅ Passed (1 fix shipped, 0 open)
**Prereqs:** Batch 04 ✅
**State produced for later batches:**
- `acme/rpm/el9` — local-upload repo with `tree-sitter-cli-0.25.10-2.el9.x86_64.rpm` (from EPEL 9, 2.5 MB)
- `acme/rpm/docker-ce-2` — mirror from `https://download.docker.com/linux/centos/9/x86_64/stable/` filtered to `Names:["docker-buildx-plugin"]`, 34 packages, ~545 MB
- `acme/deb/deb` — local-upload repo with `hello_2.10-3_amd64.deb` (from deb.debian.org, 52 KB)

## Test cases

### 6.1 Create RPM + DEB repos via API ✅
- `POST /api/v1/projects/acme/repos {name:"el9", type:"rpm", public_read:true}` → `200`. Response includes `fingerprint` (PGP signing key for repodata, generated on create).
- `POST` for `{type:"deb"}` → `200` (NB: type is `deb`, not `apt` — the protocol name is `deb` per `metadata/repos.go`).
- Initial attempt `{type:"apt"}` → `HTTP 422 {message:"invalid type"}`.

### 6.2 RPM upload via PUT (real EPEL package) ✅
- `curl -u alice:<api-key> -X PUT http://localhost:28080/acme/rpm/el9/packages/tree-sitter-cli-0.25.10-2.el9.x86_64.rpm --data-binary @ts.rpm` → `HTTP 201`.
- Wait ~5s for the regen coalescer to write `repodata/repomd.xml` + `repodata/primary-<hash>.xml.gz`.
- `GET /acme/rpm/el9/repodata/repomd.xml` → `HTTP 200` with full XML schema (revision, primary block with sha256 + open-checksum + size + open-size + location href).
- Pull-back via `GET /acme/rpm/el9/packages/tree-sitter-cli-...rpm` produces a byte-identical SHA-256 match against the original file.

### 6.3 DEB upload via PUT (real Debian hello) ✅
- `PUT http://localhost:28080/acme/deb/deb/pool/main/h/hello/hello_2.10-3_amd64.deb` (canonical Debian pool path) → `HTTP 201`.
- `GET /acme/deb/deb/dists/stable/InRelease` → `HTTP 200` with PGP-signed clearsig: `Origin: OmniRepo · Label: deb · Suite: stable · Codename: stable · Architectures: all amd64 arm64 · Components: main` + MD5Sum/SHA256 sections listing `main/binary-amd64/Packages`, `Packages.gz`.
- `GET /dists/stable/main/binary-amd64/Packages.gz` → `HTTP 200`, 482 bytes.
- Pull-back via pool URL → byte-identical SHA-256 to original.

### 6.4 RPM mirror sync — F-06.1 BLOCKER 🟥 → ✅ fixed
- **Pre-fix repro** (`https://dl.fedoraproject.org/pub/epel/9/Everything/x86_64/`):
  - Sync job hit `last_error: rpm upstream: gunzip primary: gzip: invalid header`.
  - Investigation: EPEL serves primary as `repodata/<hash>-primary.xml.xz` (xz). OmniRepo hardcoded `gzip.NewReader`.
- **Then re-tested** (`https://download.docker.com/linux/centos/9/x86_64/stable/`):
  - Same failure mode but now `last_error: ... unsupported primary compression suffix in "...primary.xml.zst" (want .gz/.xz/.xml)` — Docker CE serves primary as zstd.
- **Fix shipped** (commit `25c1f7b`): factor `openPrimaryReader(href, body) (io.ReadCloser, error)` dispatching by suffix; wire `compress/gzip`, `github.com/ulikunitz/xz`, `github.com/klauspost/compress/zstd` (both already vendored). Unsupported suffix returns a typed error including the offending href.
- **Tests added** (`internal/protocol/rpm/upstream_parse_test.go`):
  - `TestRPMParseUpstream_XZPrimary` — pins xz path
  - `TestRPMParseUpstream_ZSTPrimary` — pins zstd path
  - `TestRPMParseUpstream_PlainXMLPrimary` — pins uncompressed path
- **End-to-end re-verification**: `acme/rpm/docker-ce-2` mirror with filter `{"Names":["docker-buildx-plugin"]}` synced **34 packages / ~545 MB** from download.docker.com. Local `repodata/repomd.xml` regenerated cleanly. Sync job: `id=4 status=done files=34 bytes=545494934/545494934`.

### 6.5 RPM mirror UI ✅
- `acme/rpm/docker-ce-2` page lists all 34 mirrored packages with "Synced from upstream" / mirror badge. Search by name works.

### 6.6 RBAC — viewer cannot upload ✅
- Bob (viewer on acme) `PUT /acme/rpm/el9/packages/foo.rpm` → `HTTP 403` (covered by the same auth chain that gated bob's repo create).

### 6.7 Filter shape mismatch (silent acceptance — minor) ⚠
- Initial attempt to create mirror with `mirror_filter_json:"{\"include\":[\"jq*\"]}"` was silently accepted but ignored — the create-time field is `mirror_filter` (raw JSON object, not `mirror_filter_json` string), and `validateMirrorFilter` is keyed on the right field. Result: filter `{}` stored, mirror downloaded everything (would have been 20 GB of EPEL). Discovered when re-typing the request with `mirror_filter:{...}`.
- Not a fix — the request schema is correctly documented in `CreateRepoRequest` and PATCH-time `mirror_filter` validation does enforce shape. But silent acceptance of unknown field-names at create time is a polish opportunity; consider `decodeJSONBody` adding `DisallowUnknownFields()` for create requests in v1.8+. Logged as wt4 carry-over.

### 6.8 Drift purge ⬜ deferred to batch 16
- Drift purge is exercised by the v1.5/1.6/1.7 delta tests in batch 16 across all 4 mirror protocols (PyPI/RPM/DEB/Helm). Not duplicated here.

### 6.9 dnf / apt round-trip in container ⬜ covered by `make conformance-{rpm,deb}`
- `make conformance-rpm` and `make conformance-deb` exercise full `dnf install` / `apt-get install` round-trips in DinD containers. Both green per v1.7 STATE.md.

## Findings

### F-06.1 RPM mirror parser hardcodes gzip — fails on Fedora/EPEL/Rocky/Alma/Docker-CE upstreams
- **Severity:** **B / blocker** (every modern RPM upstream)
- **Area:** `internal/protocol/rpm/upstream_parse.go:90` (line numbers as of pre-fix commit)
- **Symptom:** Mirror sync from `dl.fedoraproject.org/pub/epel/9/...` → `last_error: rpm upstream: gunzip primary: gzip: invalid header`. Same against download.docker.com → `... unsupported primary compression suffix ...zst`.
- **Root cause:** `gzip.NewReader(strings.NewReader(string(primaryGZ)))` invoked unconditionally regardless of the codec advertised by `repomd.xml`'s `<location href="...primary.xml.{gz,xz,zst}">`.
- **Fix:** commit `25c1f7b` — `openPrimaryReader(href, body)` dispatches by suffix using `compress/gzip`, `ulikunitz/xz`, `klauspost/compress/zstd` (both new deps were already vendored for deb/scan).
- **Codex verify:** ⬜ Pending (will batch with 07–09)
- **Retest:** ✅ `acme/rpm/docker-ce-2` mirror synced 34 packages from download.docker.com (zstd primary).
- **Status:** ✅ Closed

## Sign-off

- [x] All in-scope test cases marked
- [x] Backend log gate: 0 hits
- [ ] Codex batch-end review (will batch with 07–09)
- [x] Status flipped to ✅ in this file
