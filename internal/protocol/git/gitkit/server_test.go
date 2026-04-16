package gitkit_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"testing"

	git "github.com/dxc-internal/omnirepo/internal/protocol/git"
	"github.com/dxc-internal/omnirepo/internal/protocol/git/gitkit"
)

func TestBackendName(t *testing.T) {
	t.Parallel()
	if got := gitkit.New().BackendName(); got != "gitkit" {
		t.Fatalf("BackendName = %q, want %q", got, "gitkit")
	}
}

func TestInfoRefsViaSubprocess(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git CLI not available")
	}
	// Create a bare repo on disk.
	dir := filepath.Join(t.TempDir(), "r.git")
	if err := git.InitBare(dir, "main"); err != nil {
		t.Fatal(err)
	}

	// Wire gitkit handler scoped to the bare repo.
	h := gitkit.New().Handler(dir)
	mux := http.NewServeMux()
	mux.Handle("/git/r.git/", http.StripPrefix("/git/r.git", h))
	mux.Handle("/git/r.git", http.StripPrefix("/git/r.git", h))
	ts := httptest.NewServer(mux)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/git/r.git/info/refs?service=git-upload-pack")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d; body=%s", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/x-git-upload-pack-advertisement" {
		t.Fatalf("Content-Type = %q", ct)
	}
}
