package git_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// --- Plan 11-07 Task 2 — GITMIRROR-04 / D-12 LFS batch 501 ---
//
// /info/lfs/objects/batch MUST return 501 with envelope code
// `lfs.not_supported` for BOTH mirror and non-mirror Git repos (D-12
// scope is universal in v1.4 — go-git has no LFS client, so gating only
// mirrors would leave a silent-success path on dev repos where a pointer
// file could slip in and route clients to the upstream LFS host on clone).
//
// Reuses the newMirrorGateHarness() fixture from handler_test.go which
// seeds one mirror + one plain git repo under "testproj" and mounts the
// handler via TestRouter (no auth).

// TestLFSBatch_MirrorRepo_Returns501 asserts a mirror repo rejects the
// LFS batch API with 501 and envelope code "lfs.not_supported".
func TestLFSBatch_MirrorRepo_Returns501(t *testing.T) {
	t.Parallel()
	ts, rec := newMirrorGateHarness(t)

	url := ts.URL + "/testproj/git/mirror-repo.git/info/lfs/objects/batch"
	body := strings.NewReader(`{"operation":"download","objects":[]}`)
	req, err := http.NewRequest(http.MethodPost, url, body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "application/vnd.git-lfs+json")
	req.Header.Set("Content-Type", "application/vnd.git-lfs+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST lfs batch (mirror): %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501; backend lastPath=%q", resp.StatusCode, rec.lastPath)
	}
	if rec.lastPath != "" {
		t.Fatalf("backend MUST NOT be invoked for LFS batch; got lastPath=%q", rec.lastPath)
	}
	assertMirrorEnvelope(t, readAll(t, resp), "lfs.not_supported")
}

// TestLFSBatch_DevRepo_AlsoReturns501 pins D-12's "BOTH dev and mirror
// repos" scope: a plain (non-mirror) Git repo must also 501 on LFS batch.
// Prevents the regression where gating only mirrors leaves dev repos as
// a silent-success LFS passthrough.
func TestLFSBatch_DevRepo_AlsoReturns501(t *testing.T) {
	t.Parallel()
	ts, rec := newMirrorGateHarness(t)

	url := ts.URL + "/testproj/git/plain-repo.git/info/lfs/objects/batch"
	body := strings.NewReader(`{"operation":"download","objects":[]}`)
	req, err := http.NewRequest(http.MethodPost, url, body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/vnd.git-lfs+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST lfs batch (non-mirror): %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501 (D-12 applies to dev repos too); lastPath=%q", resp.StatusCode, rec.lastPath)
	}
	if rec.lastPath != "" {
		t.Fatalf("backend MUST NOT be invoked for LFS batch on dev repo; got lastPath=%q", rec.lastPath)
	}
	assertMirrorEnvelope(t, readAll(t, resp), "lfs.not_supported")
}

// TestLFSBatch_GET_Also501 covers the unusual but possible case of a
// client probing the LFS endpoint with a HEAD-like GET. The handler uses
// chi's method-agnostic r.Handle so every method at the path returns 501,
// not just POST.
func TestLFSBatch_GET_Also501(t *testing.T) {
	t.Parallel()
	ts, _ := newMirrorGateHarness(t)

	url := ts.URL + "/testproj/git/mirror-repo.git/info/lfs/objects/batch"
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET lfs batch: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501", resp.StatusCode)
	}
	assertMirrorEnvelope(t, readAll(t, resp), "lfs.not_supported")
}

// TestLFSBatch_IsSpecificRoute_NotCatchAll pins chi's specific-pattern-
// beats-wildcard precedence: other /info/* paths (here /info/refs, which
// is a real Smart-HTTP endpoint) MUST continue to reach the backend and
// return non-501. The regression this guards: accidentally mounting the
// LFS route as a `/info/*` prefix that swallows all info/* traffic.
func TestLFSBatch_IsSpecificRoute_NotCatchAll(t *testing.T) {
	t.Parallel()
	ts, rec := newMirrorGateHarness(t)

	// info/refs for a plain repo is a valid read-side capability query.
	url := ts.URL + "/testproj/git/plain-repo.git/info/refs?service=git-upload-pack"
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET info/refs: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotImplemented {
		t.Fatalf("info/refs returned 501 — LFS route is over-matching")
	}
	if rec.lastPath == "" {
		t.Fatalf("backend NOT invoked for info/refs — LFS route stole the catch-all path")
	}
}

// Ensure httptest.NewServer is still the chosen fixture — keeps the
// import required even if future test refactors move httptest usage.
var _ = httptest.NewServer
