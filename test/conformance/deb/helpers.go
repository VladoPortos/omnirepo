//go:build conformance

// Package deb_conformance drives a real `apt-get` client (Debian 12 DinD)
// against an in-process omnirepo instance.
package deb_conformance

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

	"github.com/blakesmith/ar"

	"github.com/vladoportos/omnirepo/internal/app"
	"github.com/vladoportos/omnirepo/internal/config"
)

const imagesFile = "test/conformance/images.txt"
const baseImageKey = "debian:12-slim"

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

	// Bind 0.0.0.0 so DinD apt-get reaches host.docker.internal -> docker
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

// buildSyntheticDeb constructs a minimal valid .deb archive in-memory with
// the given control fields. The data.tar.gz is empty (apt-get install is
// expected to fail on dependency resolution at the dpkg step in some flows;
// the conformance test asserts apt-get update + apt-get install --download-only
// instead, which only exercises metadata + package fetch).
func buildSyntheticDeb(t *testing.T, pkg, version, arch string) []byte {
	t.Helper()

	control := fmt.Sprintf("Package: %s\nVersion: %s\nArchitecture: %s\n"+
		"Maintainer: OmniRepo Conformance <noreply@example.com>\n"+
		"Installed-Size: 1\nSection: misc\nPriority: optional\n"+
		"Description: synthetic conformance package\n", pkg, version, arch)

	// control.tar.gz with ./control
	var ctlTar bytes.Buffer
	twCtl := tar.NewWriter(&ctlTar)
	body := []byte(control)
	_ = twCtl.WriteHeader(&tar.Header{Name: "./control", Mode: 0o644, Size: int64(len(body))})
	_, _ = twCtl.Write(body)
	_ = twCtl.Close()
	var ctlGz bytes.Buffer
	gz := gzip.NewWriter(&ctlGz)
	_, _ = gz.Write(ctlTar.Bytes())
	_ = gz.Close()

	// data.tar.gz: empty tar stream
	var dataTar bytes.Buffer
	twData := tar.NewWriter(&dataTar)
	_ = twData.Close()
	var dataGz bytes.Buffer
	gz2 := gzip.NewWriter(&dataGz)
	_, _ = gz2.Write(dataTar.Bytes())
	_ = gz2.Close()

	var out bytes.Buffer
	aw := ar.NewWriter(&out)
	if err := aw.WriteGlobalHeader(); err != nil {
		t.Fatalf("ar global: %v", err)
	}
	writeMember := func(name string, body []byte) {
		hdr := &ar.Header{Name: name, Size: int64(len(body)), Mode: 0o644}
		if err := aw.WriteHeader(hdr); err != nil {
			t.Fatalf("ar hdr %s: %v", name, err)
		}
		if _, err := aw.Write(body); err != nil {
			t.Fatalf("ar write %s: %v", name, err)
		}
	}
	writeMember("debian-binary", []byte("2.0\n"))
	writeMember("control.tar.gz", ctlGz.Bytes())
	writeMember("data.tar.gz", dataGz.Bytes())
	return out.Bytes()
}
