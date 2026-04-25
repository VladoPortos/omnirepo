//go:build conformance

// Package helm_conformance drives the alpine/helm:3.20 client (DinD)
// against an in-process omnirepo instance.
package helm_conformance

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
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
const baseImageKey = "alpine/helm:3.20"

type bootFixture struct {
	host          string
	port          int
	dataRoot      string
	adminLogin    string
	adminPassword string
	project       string
	repo          string
	cancel        context.CancelFunc
	doneCh        chan error
}

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

	bs := map[string]any{
		"schema_version": 1,
		"super_admin": map[string]any{
			"login": adminLogin, "email": "admin@example.com", "password": adminPassword,
		},
		"users":    []any{},
		"projects": []any{map[string]any{"name": project, "members": []string{}}},
		"repos": []any{
			map[string]any{"project": project, "type": repoType, "name": repo, "public_read": true},
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
	cfg.Server.ExternalHostnames = []string{"localhost", "host.docker.internal"}
	// Conformance test fixtures fire one upload per test; production
	// debounce (2000ms / 30s) is anti-test latency. 10ms / 100ms preserves
	// debounce semantics without dominating wall-clock.
	cfg.Regen.DebounceMs = 10
	cfg.Regen.MaxWaitMs = 100

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
		host: host, port: httpAddr.Port, dataRoot: dataRoot,
		adminLogin: adminLogin, adminPassword: adminPassword,
		project: project, repo: repo, cancel: cancel, doneCh: done,
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
	t.Fatalf("could not resolve %q in %s", baseImageKey, imagesFile)
	return ""
}

func dindHostArg() []string {
	if runtime.GOOS == "linux" {
		return []string{"--add-host", "host.docker.internal:host-gateway"}
	}
	return nil
}

// dockerRun runs the helm image with --entrypoint sh (alpine/helm uses
// /usr/bin/helm by default; we need a shell to chain commands).
func dockerRun(t *testing.T, image, script string) (string, error) {
	t.Helper()
	containerName := fmt.Sprintf("omnirepo-conf-%d", time.Now().UnixNano())
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	args := []string{"run", "--rm", "--name", containerName, "--entrypoint", "sh"}
	args = append(args, dindHostArg()...)
	args = append(args, image, "-c", script)
	cmd := exec.CommandContext(ctx, "docker", args...)
	out, err := cmd.CombinedOutput()
	t.Cleanup(func() {
		_ = exec.Command("docker", "rm", "-f", containerName).Run()
	})
	return string(out), err
}

func (f *bootFixture) putWithAuth(t *testing.T, fullURL string, body []byte) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, fullURL, bytes.NewReader(body))
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

func waitForIndexYaml(t *testing.T, url string, timeout time.Duration) {
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
	t.Fatalf("index.yaml at %s never returned 200 within %s", url, timeout)
}

// makeChartTGZ builds an in-memory minimal Helm chart archive.
func makeChartTGZ(t *testing.T, name, version, appVersion string) []byte {
	t.Helper()
	chartYAML := fmt.Sprintf(`apiVersion: v2
name: %s
version: %s
appVersion: "%s"
description: omnirepo conformance chart
type: application
`, name, version, appVersion)
	notes := "Test chart NOTES\n"
	values := "replicas: 1\n"

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	writeTarFile := func(path, body string) {
		h := &tar.Header{
			Name:     path,
			Mode:     0o644,
			Size:     int64(len(body)),
			Typeflag: tar.TypeReg,
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatalf("tar header %s: %v", path, err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatalf("tar body %s: %v", path, err)
		}
	}
	writeTarFile(name+"/Chart.yaml", chartYAML)
	writeTarFile(name+"/values.yaml", values)
	writeTarFile(name+"/templates/NOTES.txt", notes)
	// Add a minimal renderable template so `helm install --dry-run` produces output.
	writeTarFile(name+"/templates/configmap.yaml", `apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ .Release.Name }}-cm
data:
  hello: world
`)
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gz close: %v", err)
	}
	return buf.Bytes()
}
