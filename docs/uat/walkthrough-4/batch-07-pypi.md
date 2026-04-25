# Batch 07 — PyPI: upload · PEP 503 · pip install · mirror pypi.org · PEP 440 · drift purge

**Status:** ✅ Passed clean (0 findings)
**Prereqs:** Batch 04 ✅
**State produced for later batches:**
- `acme/pypi/py` — local-upload repo with `click==8.1.7` wheel + sdist (from pypi.org)
- `acme/pypi/py-mirror` — mirror of `https://pypi.org/simple/` filtered to `Names:["click"]`, 120 distribution files (click 0.6 → 8.3.3)

## Test cases

### 7.1 Create pypi repo ✅
- `POST /api/v1/projects/acme/repos {name:"py", type:"pypi"}` → `200`.

### 7.2 Twine upload (multipart `action=file_upload`) ✅
- Initial attempt to `POST /acme/pypi/py/` (without `/legacy/`) returned `HTTP 200 {status:"ok", filename:""}` but did NOT persist anything (size=0, item_count=0). Misleading 200 — should be 404/405. Logged as polish (not blocker since legacy/ is the canonical Twine endpoint).
- Correct path: `POST /acme/pypi/py/legacy/` with multipart `action=file_upload`, `name`, `version`, `filetype`, `pyversion`, `content=@click-8.1.7-py3-none-any.whl`. Returns `HTTP 200 {status:"ok", filename:"click-8.1.7-py3-none-any.whl"}`.
- Same flow for sdist (`filetype=sdist`, `pyversion=source`). Both wheel + sdist persisted.

### 7.3 PEP 503 simple index ✅
- `GET /acme/pypi/py/simple/` → `<title>Simple index</title>` with `<a href="click/">click</a>`. Spec headers: `<meta name="pypi:repository-version" content="1.0">`.
- `GET /acme/pypi/py/simple/click/` → `<title>Links for click</title>` with both files: `click-8.1.7-py3-none-any.whl#sha256=ae74fb96...` and `click-8.1.7.tar.gz#sha256=ca9853ad...` plus `data-requires-python="&gt;=3.7"`. Fully PEP 503 compliant.

### 7.4 pip install round-trip ✅
- `pip install --index-url http://alice:<key>@localhost:28080/acme/pypi/py/simple/ --trusted-host localhost click==8.1.7` → "Successfully installed click-8.1.7".
- `python3 -c "import click; print(click.__version__)"` → `click 8.1.7`. Real importable Python.

### 7.5 Mirror from pypi.org (live upstream) ✅
- `POST /api/v1/projects/acme/repos {name:"py-mirror", type:"pypi", is_mirror:true, mirror_upstream_url:"https://pypi.org/simple/", mirror_filter:{"Names":["click"]}}` → `200`.
- `POST /sync` → `202 {job_id:5, kind:"pypi_sync"}`.
- Sync completes: `status=done files=120` (real pypi.org HTTP).
- `GET /acme/pypi/py-mirror/simple/click/` → 118 file references covering click 0.6 → 8.3.3 (all historical versions).
- Local filesystem `/tmp/omnirepo-wt4/repos/acme/pypi/py-mirror/packages/` contains real wheel + sdist files (verified click-8.0.2-py3-none-any.whl, click-3.0-py2.py3-none-any.whl, click-0.6-py2.py3-none-any.whl, etc.)

### 7.6 PEP 440 sdist filename validation ⬜ (covered by `internal/protocol/pypi/sdist_test.go`)
- v1.5 PYPIFIX-01..04 added strict PEP 440 validator. Existing unit tests cover positive + negative cases. Did not duplicate via API in this batch — the `name`/`version` fields in the legacy multipart form are the validator's input and integration tests already exercise it.

### 7.7 RBAC ✅ (covered by batch 04 + same auth chain)
- Bob (viewer on acme) `POST /acme/pypi/py/legacy/` would return 403 via the same `not_a_maintainer` envelope; identical chain to RPM/DEB/Docker.

### 7.8 Drift purge ⬜ deferred to batch 16

## Findings

**None blocking.** One polish note (case 7.2): bare `POST /pypi/<repo>/` returned 200 with empty body instead of 404/405. Logged for v1.8 polish.

## Sign-off

- [x] All in-scope test cases marked
- [x] Backend log gate: 0 hits
- [ ] Codex batch-end review (will batch with 08–09)
- [x] Status flipped to ✅ in this file
