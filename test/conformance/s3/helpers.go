//go:build conformance

package s3conf

import (
	"bufio"
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
	"runtime"
	"strings"
	"testing"
	"time"

	"database/sql"

	_ "modernc.org/sqlite"

	"github.com/vladoportos/omnirepo/internal/app"
	"github.com/vladoportos/omnirepo/internal/config"
)

const imagesFile = "test/conformance/images.txt"

// s3Fixture is the in-process app handle returned by bootAppWithS3Bucket.
type s3Fixture struct {
	host          string // "127.0.0.1:<port>"
	port          int
	httpEndpoint  string // "http://127.0.0.1:<port>"
	s3Endpoint    string // "http://127.0.0.1:<port>/s3" — for aws-sdk-go-v2 BaseEndpoint
	dataRoot      string
	adminLogin    string
	adminPassword string
	project       string
	bucketName    string
	akid          string
	secret        string
	cancel        context.CancelFunc
	doneCh        chan error
}

// bootAppWithS3Bucket boots omnirepo in-process with a project, creates an
// S3 access key via the admin REST API, and provisions a bucket via the
// backend's CreateBucketForProject. Returns a fixture with valid credentials
// ready for aws-sdk-go-v2 tests.
func bootAppWithS3Bucket(t *testing.T) *s3Fixture {
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
	project := "s3test"
	bucketName := "conformance-bucket"

	bs := map[string]any{
		"schema_version": 1,
		"super_admin": map[string]any{
			"login": adminLogin, "email": "admin@example.com", "password": adminPassword,
		},
		"users":    []any{},
		"projects": []any{map[string]any{"name": project, "members": []string{}}},
		"repos":    []any{},
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

	// Bind on 0.0.0.0 (all interfaces) so containers spawned via DinD can
	// reach the test server through host.docker.internal -> docker bridge IP
	// (Linux: --add-host host-gateway). 127.0.0.1 is reachable only from
	// the host's loopback, not from the docker bridge network. Mirrors the
	// pattern git conformance helpers already use.
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
	httpEndpoint := "http://" + host
	waitHealthy(t, httpEndpoint+"/healthz", 10*time.Second)

	f := &s3Fixture{
		host:          host,
		port:          httpAddr.Port,
		httpEndpoint:  httpEndpoint,
		s3Endpoint:    httpEndpoint + "/s3",
		dataRoot:      dataRoot,
		adminLogin:    adminLogin,
		adminPassword: adminPassword,
		project:       project,
		bucketName:    bucketName,
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

	// Create an S3 access key via the admin API.
	akid, secret := createS3Key(t, httpEndpoint, adminLogin, adminPassword, project)
	f.akid = akid
	f.secret = secret

	// Create the bucket via direct DB insert. The S3 protocol's CreateBucket
	// path is intentionally disabled in production (DefaultProjectID=0; bucket
	// provisioning is administrative). We insert the row directly to set up
	// the conformance fixture, then also create the on-disk directory.
	createBucketDirect(t, dataRoot, project, bucketName)

	return f
}

// loginAndGetCookie calls POST /api/v1/auth/login and returns the session
// cookie value. The admin REST API uses session cookies, not Basic auth.
func loginAndGetCookie(t *testing.T, baseURL, login, password string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"login": login, "password": password})
	req, err := http.NewRequest(http.MethodPost, baseURL+"/api/v1/auth/login", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("login request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("login: status=%d body=%s", resp.StatusCode, respBody)
	}

	for _, c := range resp.Cookies() {
		if c.Name == "omnirepo_session" {
			return c.Value
		}
	}
	t.Fatal("no omnirepo_session cookie in login response")
	return ""
}

// createS3Key calls POST /api/v1/projects/{name}/s3-access-keys and returns
// the AKID + plaintext secret (shown-once response). Uses session-cookie auth.
func createS3Key(t *testing.T, baseURL, login, password, project string) (akid, secret string) {
	t.Helper()
	cookie := loginAndGetCookie(t, baseURL, login, password)

	body := []byte(`{"label":"conformance-key"}`)
	url := fmt.Sprintf("%s/api/v1/projects/%s/s3-access-keys/", baseURL, project)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("create s3 key request: %v", err)
	}
	req.AddCookie(&http.Cookie{Name: "omnirepo_session", Value: cookie})
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("create s3 key: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("create s3 key: status=%d body=%s", resp.StatusCode, respBody)
	}

	var result struct {
		AccessKeyID string `json:"access_key_id"`
		Secret      string `json:"secret"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode s3 key response: %v", err)
	}
	if result.AccessKeyID == "" || result.Secret == "" {
		t.Fatal("s3 key response missing access_key_id or secret")
	}
	return result.AccessKeyID, result.Secret
}

// createBucketDirect provisions an S3 bucket by inserting directly into the
// running app's SQLite database and creating the on-disk directory. This
// bypasses the S3 protocol (whose CreateBucket is disabled in production
// wiring) and the REST API (which has no bucket-create endpoint yet).
func createBucketDirect(t *testing.T, dataRoot, projectName, bucketName string) {
	t.Helper()
	dbPath := filepath.Join(dataRoot, "db", "omnirepo.sqlite")
	db, err := sql.Open("sqlite", dbPath+"?_txlock=immediate")
	if err != nil {
		t.Fatalf("open db for bucket create: %v", err)
	}
	defer db.Close()

	// Look up the project ID.
	var projectID int64
	if err := db.QueryRow(`SELECT id FROM projects WHERE name=?`, projectName).Scan(&projectID); err != nil {
		t.Fatalf("lookup project %q: %v", projectName, err)
	}

	// Insert the bucket row.
	if _, err := db.Exec(`INSERT INTO s3_buckets(name, project_id) VALUES (?, ?)`,
		bucketName, projectID); err != nil {
		t.Fatalf("insert bucket %q: %v", bucketName, err)
	}

	// Create the on-disk directory.
	dir := filepath.Join(dataRoot, "s3", bucketName)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("mkdir bucket %q: %v", bucketName, err)
	}
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

func resolveImage(t *testing.T, key string) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	// key=value format: "aws-cli=amazon/aws-cli:2.17.0@sha256:..."
	prefix := key + "="
	for i := 0; i < 8; i++ {
		candidate := filepath.Join(dir, imagesFile)
		if data, err := os.ReadFile(candidate); err == nil {
			scanner := bufio.NewScanner(strings.NewReader(string(data)))
			for scanner.Scan() {
				line := strings.TrimSpace(scanner.Text())
				if line == "" || strings.HasPrefix(line, "#") {
					continue
				}
				if strings.HasPrefix(line, prefix) {
					return strings.TrimPrefix(line, prefix)
				}
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatalf("could not resolve %q in %s", key, imagesFile)
	return ""
}

func dindHostArg() []string {
	if runtime.GOOS == "linux" {
		return []string{"--add-host", "host.docker.internal:host-gateway"}
	}
	return nil
}

// dockerRun runs the specified image with entrypoint sh and the given script.
func dockerRun(t *testing.T, image, script string) (string, error) {
	t.Helper()
	containerName := fmt.Sprintf("omnirepo-s3conf-%d", time.Now().UnixNano())
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
