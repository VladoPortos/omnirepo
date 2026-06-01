package gogit_test

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	git "github.com/vladoportos/omnirepo/internal/protocol/git"
	"github.com/vladoportos/omnirepo/internal/protocol/git/gogit"
)

// bootRepo creates a bare repo using InitBare and returns its path.
func bootRepo(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "r.git")
	if err := git.InitBare(dir, "main"); err != nil {
		t.Fatal(err)
	}
	return dir
}

// serveHandler boots the gogit server and returns (srv, repoURL).
func serveHandler(t *testing.T, repoPath string) (*httptest.Server, string) {
	t.Helper()
	srv := gogit.New()
	h := srv.Handler(repoPath)
	// Mount at /git/r.git to simulate chi's strip-prefix behavior.
	mux := http.NewServeMux()
	mux.Handle("/git/r.git/", http.StripPrefix("/git/r.git", h))
	mux.Handle("/git/r.git", http.StripPrefix("/git/r.git", h))
	ts := httptest.NewServer(mux)
	return ts, ts.URL + "/git/r.git"
}

func TestBackendName(t *testing.T) {
	t.Parallel()
	if got := gogit.New().BackendName(); got != "gogit" {
		t.Fatalf("BackendName = %q, want %q", got, "gogit")
	}
}

func TestInfoRefsUploadPackPreamble(t *testing.T) {
	t.Parallel()
	path := bootRepo(t)
	ts, url := serveHandler(t, path)
	defer ts.Close()
	resp, err := http.Get(url + "/info/refs?service=git-upload-pack")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/x-git-upload-pack-advertisement" {
		t.Fatalf("Content-Type = %q", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	// Smart-HTTP preamble: packed "# service=git-upload-pack\n" + flush packet.
	if !bytes.Contains(body, []byte("# service=git-upload-pack\n")) {
		t.Fatalf("advertisement missing preamble; got:\n%s", body)
	}
}

func TestInfoRefsReceivePackEmpty(t *testing.T) {
	t.Parallel()
	path := bootRepo(t)
	ts, url := serveHandler(t, path)
	defer ts.Close()
	resp, err := http.Get(url + "/info/refs?service=git-receive-pack")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(body, []byte("# service=git-receive-pack\n")) {
		t.Fatalf("missing preamble: %s", body)
	}
}

// populateRepo uses git CLI to clone, add a commit, and push back.
func populateRepo(t *testing.T, repoURL string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git CLI not available")
	}
	tmp := t.TempDir()
	wd := filepath.Join(tmp, "clone")
	mustGit(t, tmp, "clone", repoURL, wd)
	mustGit(t, wd, "config", "user.email", "t@e.com")
	mustGit(t, wd, "config", "user.name", "T")
	if err := os.WriteFile(filepath.Join(wd, "README.md"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, wd, "add", "README.md")
	mustGit(t, wd, "commit", "-m", "initial")
	mustGit(t, wd, "push", "origin", "HEAD:refs/heads/main")
}

func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestCloneAndPushAndReclone(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git CLI not available")
	}
	path := bootRepo(t)
	ts, url := serveHandler(t, path)
	defer ts.Close()
	// Push a commit.
	populateRepo(t, url)

	// Reclone and check README.
	tmp := t.TempDir()
	wd := filepath.Join(tmp, "c2")
	mustGit(t, tmp, "clone", url, wd)
	b, err := os.ReadFile(filepath.Join(wd, "README.md"))
	if err != nil {
		t.Fatalf("read README: %v", err)
	}
	if string(b) != "hello" {
		t.Fatalf("got %q", b)
	}
}

func TestGzipRequestBody(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git CLI not available")
	}
	// git CLI will send Content-Encoding: gzip for larger request bodies;
	// push/fetch round-trip above exercises this path. Explicit smoke
	// test: send gzip-encoded garbage to git-upload-pack and expect a
	// non-500 (the handler must accept the encoding, then let the
	// upload-pack parser reject the garbage body gracefully).
	path := bootRepo(t)
	ts, url := serveHandler(t, path)
	defer ts.Close()
	// Build a gzip stream of an empty body.
	var buf bytes.Buffer
	// Minimal gzip header for empty payload.
	gzEmpty := []byte{
		0x1f, 0x8b, 0x08, 0x00, 0, 0, 0, 0, 0, 0xff,
		0x03, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
	}
	buf.Write(gzEmpty)
	req, err := http.NewRequest("POST", url+"/git-upload-pack", &buf)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Encoding", "gzip")
	req.Header.Set("Content-Type", "application/x-git-upload-pack-request")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	// Must not be 500 (internal error) — handler must have decoded gzip
	// successfully even if the payload is not a valid pack protocol body.
	if resp.StatusCode == http.StatusInternalServerError {
		t.Fatalf("gzip body should be decoded, got 500")
	}
}

func TestConcurrentClonesLockFree(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git CLI not available")
	}
	path := bootRepo(t)
	ts, url := serveHandler(t, path)
	defer ts.Close()
	populateRepo(t, url)

	// Fire 8 concurrent info/refs GETs (read path must be lock-free).
	const N = 8
	var wg sync.WaitGroup
	errs := make([]string, N)
	bodies := make([]string, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			resp, err := http.Get(url + "/info/refs?service=git-upload-pack")
			if err != nil {
				errs[i] = err.Error()
				return
			}
			defer resp.Body.Close()
			b, _ := io.ReadAll(resp.Body)
			bodies[i] = string(b)
		}(i)
	}
	wg.Wait()
	for i, e := range errs {
		if e != "" {
			t.Fatalf("goroutine %d: %s", i, e)
		}
	}
	// All bodies should be identical (same refs, same order).
	for i := 1; i < N; i++ {
		if bodies[i] != bodies[0] {
			t.Fatalf("goroutine %d body diverges", i)
		}
	}
	if !strings.Contains(bodies[0], "# service=git-upload-pack\n") {
		t.Fatalf("missing preamble in concurrent body")
	}
}
