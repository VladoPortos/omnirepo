//go:build conformance

// Package pypi_conformance drives `pip` and `uv` (python:3.12-alpine DinD)
// against an in-process omnirepo instance.
package pypi_conformance

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
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
const baseImageKey = "python:3.12-alpine"

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

// twineUpload posts a multipart body emulating `twine upload` to /legacy/.
func (f *bootFixture) twineUpload(t *testing.T, filename string, content []byte) {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("name", filename)
	_ = mw.WriteField("version", "0")
	_ = mw.WriteField("filetype", "bdist_wheel")
	fw, err := mw.CreateFormFile("content", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(fw, bytes.NewReader(content)); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	url := fmt.Sprintf("http://%s/%s/pypi/%s/legacy/", f.host, f.project, f.repo)
	req, _ := http.NewRequest(http.MethodPost, url, &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.SetBasicAuth(f.adminLogin, f.adminPassword)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("twine POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		t.Fatalf("twine POST %s: status=%d", url, resp.StatusCode)
	}
}

func waitForSimpleIndex(t *testing.T, url string, timeout time.Duration) {
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
	t.Fatalf("simple index at %s never returned 200 within %s", url, timeout)
}

// makeWheelBytes builds a minimal valid wheel file in-memory.
func makeWheelBytes(t *testing.T, name, version string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	distInfo := strings.ReplaceAll(name, "-", "_") + "-" + version + ".dist-info"
	w, err := zw.Create(distInfo + "/METADATA")
	if err != nil {
		t.Fatal(err)
	}
	body := "Metadata-Version: 2.1\nName: " + name + "\nVersion: " + version + "\nRequires-Python: >=3.8\nSummary: omnirepo conformance pkg\n"
	_, _ = w.Write([]byte(body))
	rw, _ := zw.Create(distInfo + "/RECORD")
	_, _ = rw.Write([]byte(""))
	wm, _ := zw.Create(distInfo + "/WHEEL")
	_, _ = wm.Write([]byte("Wheel-Version: 1.0\nGenerator: omnirepo-conf\nRoot-Is-Purelib: true\nTag: py3-none-any\n"))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
