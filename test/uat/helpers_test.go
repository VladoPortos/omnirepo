//go:build uat

// Package uat holds end-to-end acceptance tests that drive real external
// clients (docker CLI, trivy binary, crane) against a booted omnirepo
// server. All tests are `//go:build uat` gated so the default CI gate
// doesn't try to run them.
package uat

import (
	"bytes"
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

	"github.com/dxc-internal/omnirepo/internal/app"
	"github.com/dxc-internal/omnirepo/internal/config"
)

// uatFixture is the in-process app handle returned by bootApp. Mirrors
// the conformance fixture but lives under the `uat` build tag so it is
// free to add UAT-specific knobs (trivy cache path, projects/repo
// pre-creation via REST, etc.).
type uatFixture struct {
	host          string // "127.0.0.1:<port>" — plain HTTP
	dataRoot      string
	adminLogin    string
	adminPassword string
	project       string
	repo          string
	port          int
	cancel        context.CancelFunc
	doneCh        chan error
	httpClient    *http.Client
}

// bootOpts tunes the fixture. Zero value is fine for most tests.
type bootOpts struct {
	// ExtraBootstrap is merged into the default bootstrap.json. Use it to
	// add projects / repos / api keys that the test wants pre-seeded.
	ExtraBootstrap map[string]any
	// TrivyCachePath points the server's scan runner at a pre-populated
	// Trivy cache directory (for the trivy UAT test). Leave empty for
	// tests that don't exercise scan.
	TrivyCachePath string
	// Project / Repo override the default seeded project+docker repo names.
	Project string
	Repo    string
	// RepoType overrides the default "docker" repo type.
	RepoType string
	// BlockOnSeverity is applied to the seeded repo if non-empty.
	BlockOnSeverity string
	// BindAll binds listeners on 0.0.0.0 instead of 127.0.0.1 so
	// external-to-this-host clients (notably the docker daemon which
	// runs in its own network namespace under Docker Desktop/WSL2) can
	// reach the server.
	BindAll bool
}

func bootApp(t *testing.T, opts bootOpts) *uatFixture {
	t.Helper()
	if opts.Project == "" {
		opts.Project = "uat"
	}
	if opts.Repo == "" {
		opts.Repo = "app"
	}
	if opts.RepoType == "" {
		opts.RepoType = "docker"
	}

	dataRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dataRoot, "config"), 0o750); err != nil {
		t.Fatal(err)
	}
	adminLogin := "admin"
	adminPassword := fmt.Sprintf("uat-pw-%d", time.Now().UnixNano())

	repoEntry := map[string]any{
		"project":     opts.Project,
		"type":        opts.RepoType,
		"name":        opts.Repo,
		"public_read": true,
	}
	if opts.BlockOnSeverity != "" {
		repoEntry["block_on_severity"] = opts.BlockOnSeverity
	}
	bs := map[string]any{
		"schema_version": 1,
		"super_admin": map[string]any{
			"login":    adminLogin,
			"email":    "admin@example.com",
			"password": adminPassword,
		},
		"users":    []any{},
		"projects": []any{map[string]any{"name": opts.Project, "members": []string{}}},
		"repos":    []any{repoEntry},
		"api_keys": []any{},
	}
	for k, v := range opts.ExtraBootstrap {
		bs[k] = v
	}
	bsBytes, _ := json.Marshal(bs)
	bsPath := filepath.Join(dataRoot, "config", "bootstrap.json")
	if err := os.WriteFile(bsPath, bsBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := config.Defaults()
	cfg.DataRoot = dataRoot
	cfg.Bootstrap.Path = bsPath
	cfg.Server.ExternalHostnames = []string{"localhost"}
	if opts.TrivyCachePath != "" {
		cfg.Trivy.CachePath = opts.TrivyCachePath
	} else {
		// Always redirect cache to a tmp dir so tests never touch
		// /var/lib/omnirepo/trivy/cache on the host.
		cfg.Trivy.CachePath = filepath.Join(dataRoot, "trivy-cache")
	}

	bindAddr := "127.0.0.1"
	if opts.BindAll {
		bindAddr = "0.0.0.0"
	}
	httpLn, err := net.Listen("tcp", bindAddr+":0")
	if err != nil {
		t.Fatal(err)
	}
	httpsLn, err := net.Listen("tcp", bindAddr+":0")
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
	case <-time.After(15 * time.Second):
		cancel()
		t.Fatal("app.Run did not signal ready within 15s")
	}

	host := fmt.Sprintf("127.0.0.1:%d", httpAddr.Port)
	waitHealthy(t, "http://"+host+"/healthz", 10*time.Second)

	f := &uatFixture{
		host:          host,
		dataRoot:      dataRoot,
		adminLogin:    adminLogin,
		adminPassword: adminPassword,
		project:       opts.Project,
		repo:          opts.Repo,
		port:          httpAddr.Port,
		cancel:        cancel,
		doneCh:        done,
		httpClient:    &http.Client{Timeout: 30 * time.Second},
	}
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Log("WARN: app.Run did not return within 10s of ctx cancel")
		}
	})
	return f
}

// ref returns a docker-style reference <host>/<project>/docker/<repo>:<tag>.
func (f *uatFixture) ref(tag string) string {
	return fmt.Sprintf("%s/%s/docker/%s:%s", f.host, f.project, f.repo, tag)
}

func (f *uatFixture) refRepo(repo, tag string) string {
	return fmt.Sprintf("%s/%s/docker/%s:%s", f.host, f.project, repo, tag)
}

// baseURL returns the plain-HTTP base URL for the fixture.
func (f *uatFixture) baseURL() string { return "http://" + f.host }

// dockerReachableHost returns "<ip>:<port>" on an interface reachable from
// the docker daemon. Under Docker Desktop + WSL2, dockerd lives in a
// separate network namespace and cannot reach the WSL user-distro's
// 127.0.0.1; it CAN reach the WSL eth0 IP when the server binds on
// 0.0.0.0. This helper probes the first non-loopback IPv4 interface.
// Falls back to f.host when no such interface is found. The caller must
// have booted with BindAll=true for this to be meaningful.
func (f *uatFixture) dockerReachableHost(t *testing.T) string {
	t.Helper()
	ifaces, err := net.Interfaces()
	if err != nil {
		return f.host
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, _ := iface.Addrs()
		for _, a := range addrs {
			var ip net.IP
			switch v := a.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip == nil || ip.To4() == nil {
				continue
			}
			return fmt.Sprintf("%s:%d", ip.String(), f.port)
		}
	}
	return f.host
}

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
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("%s never returned 200 within %s", url, timeout)
}

// ---------- REST helpers (session cookie based) ----------

// loginSession logs the super-admin in via /api/v1/auth/login and returns
// a cookie jar so subsequent REST calls attach the session.
func (f *uatFixture) loginSession(t *testing.T) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{
		"login":    f.adminLogin,
		"password": f.adminPassword,
	})
	req, _ := http.NewRequest("POST", f.baseURL()+"/api/v1/auth/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := f.httpClient.Do(req)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		rb, _ := io.ReadAll(resp.Body)
		t.Fatalf("login: status=%d body=%s", resp.StatusCode, rb)
	}
	for _, c := range resp.Cookies() {
		if c.Name == "omnirepo_session" {
			return c.Value
		}
	}
	t.Fatal("login: no omnirepo_session cookie")
	return ""
}

// doJSON sends an authenticated JSON request. cookie is the session value.
// Returns the response (caller closes body).
func (f *uatFixture) doJSON(t *testing.T, method, path string, body any, cookie string) *http.Response {
	t.Helper()
	var rb io.Reader
	if body != nil {
		buf, _ := json.Marshal(body)
		rb = bytes.NewReader(buf)
	}
	req, err := http.NewRequest(method, f.baseURL()+path, rb)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: "omnirepo_session", Value: cookie})
	}
	resp, err := f.httpClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp
}

// createRepo POSTs /api/v1/projects/{project}/repos creating a repo of the
// given type with public_read=true. Idempotent in practice against 409
// (repo already exists from bootstrap).
func (f *uatFixture) createRepo(t *testing.T, cookie, repoType, repoName string, extra map[string]any) {
	t.Helper()
	body := map[string]any{
		"name":        repoName,
		"type":        repoType,
		"public_read": true,
	}
	for k, v := range extra {
		body[k] = v
	}
	resp := f.doJSON(t, "POST", "/api/v1/projects/"+f.project+"/repos", body, cookie)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 && resp.StatusCode != 409 {
		rb, _ := io.ReadAll(resp.Body)
		t.Fatalf("create repo %s/%s: status=%d body=%s", repoType, repoName, resp.StatusCode, rb)
	}
}

// patchRepo PATCHes /api/v1/projects/{project}/repos/{type}/{repo}.
func (f *uatFixture) patchRepo(t *testing.T, cookie, repoType, repoName string, body map[string]any) {
	t.Helper()
	resp := f.doJSON(t, "PATCH",
		"/api/v1/projects/"+f.project+"/repos/"+repoType+"/"+repoName, body, cookie)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		rb, _ := io.ReadAll(resp.Body)
		t.Fatalf("patch repo %s/%s: status=%d body=%s", repoType, repoName, resp.StatusCode, rb)
	}
}

// ---------- Binary resolution ----------

// mustFindBin returns the absolute path to name on $PATH; t.Skip if missing.
func mustFindBin(t *testing.T, name string) string {
	t.Helper()
	p, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("%s binary not on $PATH — skipping this UAT test", name)
	}
	return p
}

// runCmd runs a command and returns stdout+stderr. Fails the test on error
// unless allowErr is true (for negative-path assertions).
func runCmd(t *testing.T, env []string, allowErr bool, name string, args ...string) (string, string, error) {
	t.Helper()
	cmd := exec.Command(name, args...)
	if env != nil {
		cmd.Env = append(os.Environ(), env...)
	}
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil && !allowErr {
		t.Fatalf("%s %v: %v\nstdout=%s\nstderr=%s",
			name, args, err, stdout.String(), stderr.String())
	}
	return stdout.String(), stderr.String(), err
}
