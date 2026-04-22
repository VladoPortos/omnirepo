# Batch 07 — PyPI

**Status:** ⬜ Not started
**Prereqs:** Batch 04 ✅
**State produced for later batches:**
- `acme/pypi/local` with at least one uploaded wheel
- `acme/pypi/pypi-upstream` mirroring a tiny subset of pypi.org (or local fixture)
- block_on_severity=high exercised (WALKTHROUGH-2 §3a repro)

## Pre-flight

- [ ] `twine` and `pip` available on client machine / container
- [ ] `python -m build` or `pip wheel` to produce test wheels
- [ ] Logged in as alice
- [ ] Server log tail open

## Test cases

### 7.1 Create local PyPI repo
- [ ] Type `PyPI`, name `local`, mirror=false
- [ ] Empty state shows `pip install -i http://localhost:18080/acme/pypi/local/simple/ …` snippet

### 7.2 Upload wheel via twine
- [ ] Build a tiny wheel (e.g. `hello-1.0-py3-none-any.whl`)
- [ ] `twine upload --repository-url http://localhost:18080/acme/pypi/local/ -u alice -p <api-key> dist/*`
- [ ] **Expected:** 201; PEP 503 simple/ index regenerated
- [ ] UI shows the project `hello` with version 1.0

### 7.3 PEP 503 simple index content
- [ ] `curl http://localhost:18080/acme/pypi/local/simple/` → HTML listing of projects (normalized names)
- [ ] `curl http://localhost:18080/acme/pypi/local/simple/hello/` → HTML listing of files with `sha256=...` fragment
- [ ] `curl http://localhost:18080/acme/pypi/local/simple/Hello/` → redirect or 200 (normalized to `hello`)

### 7.4 `pip install` end-to-end
- [ ] Client: `pip install --index-url http://localhost:18080/acme/pypi/local/simple/ hello`
- [ ] **Expected:** install succeeds; digest verifies

### 7.5 Upload second version
- [ ] Build `hello-1.1` wheel; upload
- [ ] Index lists both; `pip install hello==1.0` works, `pip install hello` picks 1.1

### 7.6 Upload sdist
- [ ] Build sdist (`hello-1.0.tar.gz`); upload
- [ ] Simple/ lists it alongside the wheels; `pip install --no-binary :all: hello==1.0` picks it

### 7.7 Duplicate upload
- [ ] Re-upload `hello-1.0` wheel
- [ ] **Expected:** clean 409 envelope (PyPI semantics forbid re-upload of existing version)

### 7.8 Delete wheel
- [ ] UI row action → Delete `hello-1.1.whl`
- [ ] **Expected:** 204; index regenerated; `pip install hello==1.1` fails after cache clear
- [ ] Audit log: `pypi.delete`

### 7.9 Create mirror
- [ ] Type `PyPI`, name `pypi-upstream`, mirror=true
- [ ] Upstream URL: `https://pypi.org/simple/` (air-gap: if upstream unreachable, use local fixture)
- [ ] Filters: project-name glob limited to a few packages (e.g. `requests`, `urllib3`)
- [ ] scan_on_sync=true

### 7.10 Sync
- [ ] Click "Sync now"
- [ ] **Expected:** progress stream, final "Sync complete · N files · X MB"
- [ ] `pip install --index-url http://localhost:18080/acme/pypi/pypi-upstream/simple/ requests` succeeds

### 7.11 Severity gate — repro of WALKTHROUGH-2 §3a
- [ ] After sync, pick a version of `requests` with known HIGH CVE (e.g. 2.0.1)
- [ ] Set `block_on_severity=high` on the mirror
- [ ] `curl http://localhost:18080/acme/pypi/pypi-upstream/packages/requests-2.0.1-py2.py3-none-any.whl` → 403 envelope `blocked_by_scan`
- [ ] Clean version (e.g. 2.33.x) → 200 with wheel body
- [ ] Audit log: `scan.gate.blocked` with `source:"db"`, then `source:"cache"` on second call

### 7.12 Filter validation (mixed snake/Pascal — D-3 regression)
- [ ] POST a repo with mirror filter containing both `names` and `Names`
- [ ] **Expected:** 400 `repo.mirror_filter_invalid`

### 7.13 Console + network sweep
- [ ] Repo detail, mirror sync detail, scan results
- [ ] Zero errors/warnings

## Findings

_(F-07.N)_

## Sign-off

- [ ] All cases passed
- [ ] Final state:
  - [ ] `acme/pypi/local` has `hello-1.0.whl` + sdist
  - [ ] `acme/pypi/pypi-upstream` has at least `requests` synced
  - [ ] Severity gate proven against at least one package
- [ ] All F-07.* closed
- [ ] Codex pass on fixes
- [ ] README.md batch 07 status flipped to ✅
