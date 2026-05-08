//go:build conformance

// Package rpm_conformance drives a real `dnf` client (Rocky 9 DinD) against
// an in-process omnirepo instance. Build-tag gated (`//go:build conformance`)
// so the default `make test` never requires docker.
package rpm_conformance

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/dxc-internal/omnirepo/internal/app"
	"github.com/dxc-internal/omnirepo/internal/config"
)

const imagesFile = "test/conformance/images.txt"
const baseImageKey = "rockylinux/rockylinux:9"

// bootFixture is the in-process app handle returned by bootAppWithRepo.
type bootFixture struct {
	host          string // "127.0.0.1:<port>"
	port          int
	dataRoot      string
	adminLogin    string
	adminPassword string
	project       string
	repo          string
	cancel        context.CancelFunc
	doneCh        chan error
}

// bootAppWithRepo boots the omnirepo binary in-process with bootstrap.json
// that creates a single super-admin + project + repo of repoType. RPM/DEB
// repo types trigger eager signing-key generation in the create hook.
func bootAppWithRepo(t *testing.T, repoType string) *bootFixture {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker CLI not available; skipping DinD conformance")
	}

	dataRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dataRoot, "config"), 0o750); err != nil {
		t.Fatal(err)
	}
	adminLogin := "admin"
	adminPassword := fmt.Sprintf("conf-pw-%d", time.Now().UnixNano())
	project := "conf"
	repo := "main"

	repoEntry := map[string]any{"project": project, "type": repoType, "name": repo, "public_read": true}
	bs := map[string]any{
		"schema_version": 1,
		"super_admin": map[string]any{
			"login": adminLogin, "email": "admin@example.com", "password": adminPassword,
		},
		"users":    []any{},
		"projects": []any{map[string]any{"name": project, "members": []string{}}},
		"repos":    []any{repoEntry},
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
	cfg.Server.ExternalHostnames = []string{"localhost", "host.docker.internal"}

	// Bind 0.0.0.0 so DinD dnf reaches host.docker.internal -> docker
	// bridge IP (Linux: --add-host host-gateway). 127.0.0.1 is reachable
	// only from the host's loopback.
	httpLn, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	httpsLn, err := net.Listen("tcp", "0.0.0.0:0")
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
			HTTPListener: httpLn, HTTPSListener: httpsLn, Ready: ready,
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

	f := &bootFixture{
		host:          host,
		port:          httpAddr.Port,
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

// waitHealthy polls url until GET 200 or deadline passes.
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

// resolveImage looks up the pinned digest reference for baseImageKey in
// test/conformance/images.txt. Walks up from cwd until it finds the file.
func resolveImage(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for i := 0; i < 8; i++ {
		candidate := filepath.Join(dir, imagesFile)
		if data, err := os.ReadFile(candidate); err == nil {
			scanner := bufio.NewScanner(strings.NewReader(string(data)))
			for scanner.Scan() {
				line := strings.TrimSpace(scanner.Text())
				if line == "" || strings.HasPrefix(line, "#") {
					continue
				}
				field := strings.Fields(line)[0]
				if strings.HasPrefix(field, baseImageKey+"@") {
					return field
				}
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatalf("could not resolve %q in %s (any ancestor of cwd)", baseImageKey, imagesFile)
	return ""
}

// dindHostArg returns the docker `--add-host` flag pair so the container
// can reach the host's loopback at "host.docker.internal". Linux requires
// an explicit gateway alias; macOS/Windows resolve it natively but the
// flag is harmless on those platforms.
func dindHostArg() []string {
	if runtime.GOOS == "linux" {
		return []string{"--add-host", "host.docker.internal:host-gateway"}
	}
	return nil
}

// dockerRun executes `docker run --rm --network bridge <hostArgs> <image> sh -c <script>`
// with a 2-minute timeout. Returns combined stdout/stderr and exit error.
// Generates a unique container name so cleanup is reliable in t.Cleanup.
func dockerRun(t *testing.T, image, script string) (string, error) {
	t.Helper()
	containerName := fmt.Sprintf("omnirepo-conf-%d", time.Now().UnixNano())
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	args := []string{"run", "--rm", "--name", containerName}
	args = append(args, dindHostArg()...)
	args = append(args, image, "sh", "-c", script)
	cmd := exec.CommandContext(ctx, "docker", args...)
	out, err := cmd.CombinedOutput()
	t.Cleanup(func() {
		_ = exec.Command("docker", "rm", "-f", containerName).Run()
	})
	return string(out), err
}

// putWithAuth PUTs body to fullURL with the fixture's Basic admin auth.
func (f *bootFixture) putWithAuth(t *testing.T, fullURL string, body []byte) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, fullURL, strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.SetBasicAuth(f.adminLogin, f.adminPassword)
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT %s: %v", fullURL, err)
	}
	return resp
}

// waitForMetadata polls a URL until it returns 200 or the deadline expires.
// Used after upload to ensure the debounced regen coalescer has rebuilt
// metadata before driving the client subprocess.
func waitForMetadata(t *testing.T, url string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			body := resp.StatusCode
			_ = resp.Body.Close()
			if body == http.StatusOK {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("metadata at %s never returned 200 within %s", url, timeout)
}
