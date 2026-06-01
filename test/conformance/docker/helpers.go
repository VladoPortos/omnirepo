//go:build conformance

// Package docker contains the OCI Distribution conformance suite. It boots
// the full omnirepo binary in-process on an ephemeral port, bootstraps a
// super-admin + project + docker repo, and drives the running app via the
// vendored `crane` CLI (see test/conformance/bin/README.md).
//
// Build-tag gated (`//go:build conformance`) so the default `make test`
// never requires the crane binary. `make conformance-oci` flips the tag on.
package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vladoportos/omnirepo/internal/app"
	"github.com/vladoportos/omnirepo/internal/config"
)

// CranePath is the vendored crane binary the suite drives. Resolved
// relative to the repo root via the GOMODCACHE-independent path below.
const CranePath = "test/conformance/bin/crane"

// bootFixture is the in-process app handle returned by bootApp.
type bootFixture struct {
	host           string // "127.0.0.1:<port>"
	dataRoot       string
	adminLogin     string
	adminPassword  string
	project        string
	repo           string // docker repo name
	cancel         context.CancelFunc
	doneCh         chan error
}

// bootApp starts the full app on a random loopback port, applies a
// bootstrap.json with a super-admin + project + docker repo, and returns a
// fixture handle. t.Cleanup registers graceful shutdown.
func bootApp(t *testing.T) *bootFixture {
	t.Helper()

	dataRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dataRoot, "config"), 0o750); err != nil {
		t.Fatal(err)
	}
	adminLogin := "admin"
	adminPassword := fmt.Sprintf("conf-pw-%d", time.Now().UnixNano())
	project := "conf"
	repo := "app"

	bs := map[string]any{
		"schema_version": 1,
		"super_admin": map[string]any{
			"login":    adminLogin,
			"email":    "admin@example.com",
			"password": adminPassword,
		},
		"users":    []any{},
		"projects": []any{map[string]any{"name": project, "members": []string{}}},
		"repos": []any{
			map[string]any{"project": project, "type": "docker", "name": repo, "public_read": true},
			map[string]any{"project": project, "type": "docker", "name": "b", "public_read": true},
		},
		"api_keys": []any{},
	}
	bsBytes, _ := json.Marshal(bs)
	bsPath := filepath.Join(dataRoot, "config", "bootstrap.json")
	if err := os.WriteFile(bsPath, bsBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := config.Defaults()
	cfg.DataRoot = dataRoot
	cfg.Bootstrap.Path = bsPath
	// Leave ExternalHostnames empty so the OCI Bearer challenge realm
	// falls back to r.Host (127.0.0.1:<dynamic-port>). Pinning it to
	// "localhost" pointed crane at http://localhost/v2/token (no port),
	// which it cannot reach.

	httpLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	httpsLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		_ = httpLn.Close()
		t.Fatal(err)
	}
	httpAddr := httpLn.Addr().(*net.TCPAddr)

	ready := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- app.Run(ctx, cfg, app.RunOptions{
			HTTPListener:  httpLn,
			HTTPSListener: httpsLn,
			Ready:         ready,
		})
	}()

	select {
	case <-ready:
	case err := <-done:
		cancel()
		t.Fatalf("app.Run returned before ready: %v", err)
	case <-time.After(10 * time.Second):
		cancel()
		t.Fatal("app.Run did not signal ready within 10s")
	}

	host := fmt.Sprintf("127.0.0.1:%d", httpAddr.Port)
	waitHealthy(t, "http://"+host+"/healthz", 5*time.Second)

	f := &bootFixture{
		host:          host,
		dataRoot:      dataRoot,
		adminLogin:    adminLogin,
		adminPassword: adminPassword,
		project:       project,
		repo:          repo,
		cancel:        cancel,
		doneCh:        done,
	}
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Log("WARN: app.Run did not return within 5s of ctx cancel")
		}
	})
	return f
}

// ref returns a crane-style image reference <host>/<project>/docker/<repo>:<tag>
// matching omnirepo's OCI URL convention.
func (f *bootFixture) ref(repo, tag string) string {
	if repo == "" {
		repo = f.repo
	}
	return fmt.Sprintf("%s/%s/docker/%s:%s", f.host, f.project, repo, tag)
}

// waitHealthy polls url until GET returns 200 or deadline passes.
func waitHealthy(t *testing.T, url string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("%s never returned 200 within %s", url, timeout)
}

// runCrane executes the vendored crane binary with `--insecure` (omnirepo
// listens on plain HTTP in these tests) and the fixture's Basic credentials
// wired through per-repo CRANE_AUTH env. Returns (stdout, stderr, err).
func runCrane(t *testing.T, f *bootFixture, args ...string) (string, string, error) {
	t.Helper()
	cranePath := resolveCrane(t)
	// Prepend --insecure before the subcommand-specific flags so it affects
	// the global TLS behavior.
	full := append([]string{"--insecure"}, args...)
	cmd := exec.Command(cranePath, full...)
	cmd.Env = append(os.Environ(),
		// crane auth via env. `crane auth login` would persist to a docker
		// config file — per-test auth via CRANE_USER/CRANE_PASSWORD is
		// simpler and safer under t.TempDir().
		fmt.Sprintf("HOME=%s", t.TempDir()),
	)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

// craneLogin calls `crane auth login` against the fixture's super-admin
// credentials so subsequent crane calls reuse the stored token.
func craneLogin(t *testing.T, f *bootFixture, home string) {
	t.Helper()
	cranePath := resolveCrane(t)
	cmd := exec.Command(cranePath, "--insecure", "auth", "login", f.host,
		"-u", f.adminLogin, "-p", f.adminPassword)
	cmd.Env = append(os.Environ(), fmt.Sprintf("HOME=%s", home))
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("crane auth login: %v; stderr=%s", err, stderr.String())
	}
}

// craneAuthed runs crane with HOME pointing at home so the login token from
// craneLogin is picked up automatically.
func craneAuthed(t *testing.T, home string, args ...string) (string, string, error) {
	t.Helper()
	cranePath := resolveCrane(t)
	full := append([]string{"--insecure"}, args...)
	cmd := exec.Command(cranePath, full...)
	cmd.Env = append(os.Environ(), fmt.Sprintf("HOME=%s", home))
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

// resolveCrane locates the vendored crane binary by walking up from cwd
// until it finds test/conformance/bin/crane. Fails the test with an
// actionable message if absent (mirrors the Makefile check).
func resolveCrane(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for i := 0; i < 8; i++ {
		candidate := filepath.Join(dir, CranePath)
		if fi, err := os.Stat(candidate); err == nil && !fi.IsDir() {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatalf("crane binary not found at %s (relative to any ancestor of cwd); see test/conformance/bin/README.md", CranePath)
	return ""
}

// countCASBlobs returns the number of regular files under <dataRoot>/blobs/sha256.
// Used by the blob-mount test to assert zero-copy semantics.
func countCASBlobs(t *testing.T, dataRoot string) int {
	t.Helper()
	root := filepath.Join(dataRoot, "blobs", "sha256")
	count := 0
	err := filepath.Walk(root, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if !info.IsDir() {
			count++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk CAS: %v", err)
	}
	return count
}

// httpGetWithBearer fetches url with Bearer header. Used to capture
// Docker-Content-Digest round-trip directly (crane echos it but we prefer
// asserting the wire).
func httpGetWithBearer(t *testing.T, url, bearer string) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return resp, body
}
