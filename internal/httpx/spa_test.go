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
func testDistFS() fstest.MapFS {
	return fstest.MapFS{
		"dist/index.html":            {Data: []byte("<html>SPA</html>")},
		"dist/assets/main-abc123.js": {Data: []byte("console.log('app')")},
		"dist/assets/style-def456.css": {Data: []byte("body{}")},
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

func TestIsDevMode(t *testing.T) {
	if httpx.IsDevMode() {
		t.Fatal("expected IsDevMode=false by default")
	}
	t.Setenv("OMNIREPO_DEV", "1")
	if !httpx.IsDevMode() {
		t.Fatal("expected IsDevMode=true when OMNIREPO_DEV=1")
	}
}
