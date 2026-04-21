package httpx_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/dxc-internal/omnirepo/internal/httpx"
)

// testDistFS is a minimal in-memory FS matching the expected dist/ layout.
// Includes a swagger/ subdirectory with its own index.html so the
// embedded-subapp serving guard is exercised by TestSPAHandler_SubdirIndex.
func testDistFS() fstest.MapFS {
	return fstest.MapFS{
		"dist/index.html":              {Data: []byte("<html>SPA</html>")},
		"dist/assets/main-abc123.js":   {Data: []byte("console.log('app')")},
		"dist/assets/style-def456.css": {Data: []byte("body{}")},
		"dist/swagger/index.html":      {Data: []byte("<html>SWAGGER</html>")},
		"dist/swagger/swagger-ui.css":  {Data: []byte("/* swagger css */")},
	}
}

func TestSPAHandler_RootServesIndexHTML(t *testing.T) {
	handler := httpx.SPAHandler(testDistFS())
	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("code=%d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "SPA") {
		t.Fatalf("expected index.html content, got %q", body)
	}
}

func TestSPAHandler_StaticAssetServed(t *testing.T) {
	handler := httpx.SPAHandler(testDistFS())
	req := httptest.NewRequest("GET", "/assets/main-abc123.js", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("code=%d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "console.log") {
		t.Fatalf("expected JS content, got %q", body)
	}
}

func TestSPAHandler_UnknownPathFallsBackToIndex(t *testing.T) {
	handler := httpx.SPAHandler(testDistFS())
	// Simulates a React Router path like /projects/foo.
	req := httptest.NewRequest("GET", "/projects/foo", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("code=%d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "SPA") {
		t.Fatalf("expected fallback to index.html, got %q", body)
	}
}

func TestSPAHandler_DeepPathFallback(t *testing.T) {
	handler := httpx.SPAHandler(testDistFS())
	req := httptest.NewRequest("GET", "/projects/myproj/repos/docker/myrepo", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("code=%d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "SPA") {
		t.Fatalf("expected fallback to index.html for deep path, got %q", body)
	}
}

// TestSPAHandler_SubdirIndex serves an embedded subapp's index.html when
// the user requests the directory (trailing slash). Walkthrough 2026-04-21:
// /swagger/ used to fall through to the React SPA shell because
// http.FileServer's directory-redirect (/swagger/index.html → ./) bounced
// us into the SPA fallback. Guards against the regression.
func TestSPAHandler_SubdirIndex(t *testing.T) {
	handler := httpx.SPAHandler(testDistFS())
	req := httptest.NewRequest("GET", "/swagger/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("code=%d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "SWAGGER") {
		t.Fatalf("expected swagger/index.html, got %q", body)
	}
	if strings.Contains(body, "SPA") {
		t.Fatalf("got SPA shell instead of swagger/index.html: %q", body)
	}
}

// TestSPAHandler_SubdirAsset serves files under an embedded subapp's
// directory directly, not via the SPA fallback.
func TestSPAHandler_SubdirAsset(t *testing.T) {
	handler := httpx.SPAHandler(testDistFS())
	req := httptest.NewRequest("GET", "/swagger/swagger-ui.css", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("code=%d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "swagger css") {
		t.Fatalf("expected swagger asset, got %q", w.Body.String())
	}
}

// TestSPAHandler_APIPathReturns404JSON guards WALKTHROUGH-FINDINGS F-2a:
// unknown /api/* must not fall through to index.html — that confused the
// walkthrough because HTTP 200 with a SPA body looked like the call
// succeeded until the caller tried to JSON-parse the body.
func TestSPAHandler_APIPathReturns404JSON(t *testing.T) {
	handler := httpx.SPAHandler(testDistFS())
	for _, path := range []string{"/api/v1/missing", "/api/", "/v2/unknown/manifest/sha256:deadbeef"} {
		req := httptest.NewRequest("GET", path, nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != http.StatusNotFound {
			t.Errorf("%s code=%d, want 404", path, w.Code)
		}
		ct := w.Header().Get("Content-Type")
		if !strings.HasPrefix(ct, "application/json") {
			t.Errorf("%s content-type=%q, want application/json", path, ct)
		}
		// Phase 6 / plan 04: API-like NotFound paths now emit the canonical
		// ApiErrorEnvelope (code=resource.not_found, class=validation).
		if !strings.Contains(w.Body.String(), `"code":"resource.not_found"`) {
			t.Errorf("%s body=%q missing envelope code", path, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), `"class":"validation"`) {
			t.Errorf("%s body=%q missing envelope class", path, w.Body.String())
		}
	}
}

func TestIsDevMode(t *testing.T) {
	if httpx.IsDevMode() {
		t.Fatal("expected IsDevMode=false by default")
	}
	t.Setenv("OMNIREPO_DEV", "1")
	if !httpx.IsDevMode() {
		t.Fatal("expected IsDevMode=true when OMNIREPO_DEV=1")
	}
}
