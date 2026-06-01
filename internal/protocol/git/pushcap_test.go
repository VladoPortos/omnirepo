package git_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vladoportos/omnirepo/internal/metadata"
	gitpkg "github.com/vladoportos/omnirepo/internal/protocol/git"
)

// --- Test 1: Push < cap succeeds ---

func TestPushCap_UnderLimit(t *testing.T) {
	repo := &metadata.Repo{ID: 1, ProjectID: 42, Name: "myrepo"}
	cap := int64(1024) // 1 KiB cap

	resolveCap := func(r *metadata.Repo) int64 { return cap }
	mw := gitpkg.PushSizeLimit(resolveCap)

	var innerCalled bool
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		innerCalled = true
		// Drain the body to exercise MaxBytesReader.
		buf := make([]byte, 2048)
		_, _ = r.Body.Read(buf)
		w.WriteHeader(http.StatusOK)
	})

	body := bytes.NewReader(make([]byte, 512)) // 512 bytes < 1024 cap
	req := httptest.NewRequest("POST", "/git/acme/myrepo.git/git-receive-pack", body)
	req = req.WithContext(gitpkg.WithRepo(req.Context(), repo))
	w := httptest.NewRecorder()
	mw(inner).ServeHTTP(w, req)

	if !innerCalled {
		t.Fatal("inner handler not called for under-cap push")
	}
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200", w.Code)
	}
}

// --- Test 2: Push > cap rejected with sideband + 413 ---

func TestPushCap_OverLimit(t *testing.T) {
	repo := &metadata.Repo{ID: 1, ProjectID: 42, Name: "myrepo"}
	capBytes := int64(100) // 100 bytes cap

	resolveCap := func(r *metadata.Repo) int64 { return capBytes }
	mw := gitpkg.PushSizeLimit(resolveCap)

	// Inner handler tries to read all the body — triggers MaxBytesError.
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 4096)
		for {
			_, err := r.Body.Read(buf)
			if err != nil {
				break
			}
		}
		// Do NOT write headers here — let pushcap handle the error response.
	})

	body := bytes.NewReader(make([]byte, 200)) // 200 bytes > 100 cap
	req := httptest.NewRequest("POST", "/git/acme/myrepo.git/git-receive-pack", body)
	req = req.WithContext(gitpkg.WithRepo(req.Context(), repo))
	w := httptest.NewRecorder()
	mw(inner).ServeHTTP(w, req)

	// Should get 413.
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d want 413", w.Code)
	}

	// Body should contain sideband packet with band-3 (0x03) marker.
	respBody := w.Body.Bytes()
	if len(respBody) < 5 {
		t.Fatalf("response too short: %d bytes", len(respBody))
	}
	// Byte 4 should be 0x03 (sideband band 3 = error).
	if respBody[4] != 0x03 {
		t.Fatalf("expected sideband band-3 marker (0x03) at byte 4, got 0x%02x", respBody[4])
	}
}

// --- Test 3: gzip body where WIRE bytes < cap but decoded >> cap → ACCEPTED ---

func TestPushCap_GzipWireBytesUnderCap(t *testing.T) {
	repo := &metadata.Repo{ID: 1, ProjectID: 42, Name: "myrepo"}
	capBytes := int64(1024 * 1024) // 1 MiB cap on wire bytes

	resolveCap := func(r *metadata.Repo) int64 { return capBytes }
	mw := gitpkg.PushSizeLimit(resolveCap)

	var innerCalled bool
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		innerCalled = true
		w.WriteHeader(http.StatusOK)
	})

	// Tiny wire body (< cap), happens to be "gzip" content.
	// MaxBytesReader only sees wire bytes, so this passes.
	body := bytes.NewReader(make([]byte, 100))
	req := httptest.NewRequest("POST", "/git/acme/myrepo.git/git-receive-pack", body)
	req.Header.Set("Content-Encoding", "gzip")
	req = req.WithContext(gitpkg.WithRepo(req.Context(), repo))
	w := httptest.NewRecorder()
	mw(inner).ServeHTTP(w, req)

	if !innerCalled {
		t.Fatal("inner not called for gzip body under wire cap")
	}
}

// --- Test 4: gzip body where WIRE bytes > cap → rejected with 413 ---

func TestPushCap_GzipWireBytesOverCap(t *testing.T) {
	repo := &metadata.Repo{ID: 1, ProjectID: 42, Name: "myrepo"}
	capBytes := int64(50) // 50 bytes wire cap

	resolveCap := func(r *metadata.Repo) int64 { return capBytes }
	mw := gitpkg.PushSizeLimit(resolveCap)

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 4096)
		for {
			_, err := r.Body.Read(buf)
			if err != nil {
				break
			}
		}
	})

	body := bytes.NewReader(make([]byte, 200)) // 200 wire bytes > 50 cap
	req := httptest.NewRequest("POST", "/git/acme/myrepo.git/git-receive-pack", body)
	req.Header.Set("Content-Encoding", "gzip")
	req = req.WithContext(gitpkg.WithRepo(req.Context(), repo))
	w := httptest.NewRecorder()
	mw(inner).ServeHTTP(w, req)

	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d want 413 (gzip wire over cap)", w.Code)
	}
}

// --- Test 5: per-repo override ---

func TestPushCap_PerRepoOverride(t *testing.T) {
	overrideBytes := int64(50 * 1024 * 1024) // 50 MiB
	repo := &metadata.Repo{ID: 1, ProjectID: 42, Name: "myrepo", GitMaxPushBytes: &overrideBytes}

	globalCap := int64(500 * 1024 * 1024) // 500 MiB global
	resolveCap := gitpkg.ResolveMaxPushBytes(globalCap)

	got := resolveCap(repo)
	if got != overrideBytes {
		t.Fatalf("resolveCap=%d want %d (per-repo override)", got, overrideBytes)
	}

	// nil override -> falls back to global
	repo2 := &metadata.Repo{ID: 2, Name: "other"}
	got2 := resolveCap(repo2)
	if got2 != globalCap {
		t.Fatalf("resolveCap=%d want %d (global fallback)", got2, globalCap)
	}
}

// --- Test: sideband message includes the expected literal text ---

func TestPushCap_SidebandMessage(t *testing.T) {
	repo := &metadata.Repo{ID: 1, ProjectID: 42, Name: "myrepo"}
	capBytes := int64(100)

	resolveCap := func(r *metadata.Repo) int64 { return capBytes }
	mw := gitpkg.PushSizeLimit(resolveCap)

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 4096)
		for {
			_, err := r.Body.Read(buf)
			if err != nil {
				break
			}
		}
	})

	body := bytes.NewReader(make([]byte, 200))
	req := httptest.NewRequest("POST", "/git/acme/myrepo.git/git-receive-pack", body)
	req = req.WithContext(gitpkg.WithRepo(req.Context(), repo))
	w := httptest.NewRecorder()
	mw(inner).ServeHTTP(w, req)

	// Check that the message contains the expected literal format.
	respStr := w.Body.String()
	if !strings.Contains(respStr, "push exceeds repo limit of") {
		t.Fatalf("sideband message missing expected text: %q", respStr)
	}
	if !strings.Contains(respStr, "contact a project admin") {
		t.Fatalf("sideband message missing admin contact: %q", respStr)
	}
}

// --- Test: read operations bypass push cap ---

func TestPushCap_UploadPackBypassed(t *testing.T) {
	repo := &metadata.Repo{ID: 1, ProjectID: 42, Name: "myrepo"}
	capBytes := int64(10) // very small cap

	resolveCap := func(r *metadata.Repo) int64 { return capBytes }
	mw := gitpkg.PushSizeLimit(resolveCap)

	var innerCalled bool
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		innerCalled = true
		w.WriteHeader(http.StatusOK)
	})

	body := bytes.NewReader(make([]byte, 1024)) // well over cap
	req := httptest.NewRequest("POST", "/git/acme/myrepo.git/git-upload-pack", body)
	req = req.WithContext(gitpkg.WithRepo(req.Context(), repo))
	w := httptest.NewRecorder()
	mw(inner).ServeHTTP(w, req)

	if !innerCalled {
		t.Fatal("upload-pack should bypass push cap")
	}
}
