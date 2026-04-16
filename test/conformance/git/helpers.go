//go:build conformance

// Package git_conformance drives the alpine/git:2.43 client (DinD)
// against an in-process omnirepo instance. Tests exercise both the gogit
// (pure-Go) and gitkit (subprocess) backends via env-driven parameterization.
package git_conformance

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
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

	_ "modernc.org/sqlite"

	"github.com/dxc-internal/omnirepo/internal/app"
	"github.com/dxc-internal/omnirepo/internal/auth"
	"github.com/dxc-internal/omnirepo/internal/config"
)

const imagesFile = "test/conformance/images.txt"

// gitFixture is the in-process app handle returned by bootAppWithGitRepo.
type gitFixture struct {
	host          string // "127.0.0.1:<port>"
	port          int
	httpEndpoint  string // "http://127.0.0.1:<port>"
	dataRoot      string
	adminLogin    string
	adminPassword string
	project       string
	repo          string

	// User credentials for git clone/push (login:password).
	userLogin    string
	userPassword string
	userAPIKey   string // omr_u_... plaintext

	// Project API key for D-31 variant (project:<proj>:<omr_p_...>).
	projectAPIKey string // omr_p_... plaintext

	cancel context.CancelFunc
	doneCh chan error
}

// cloneURL returns the git clone URL with the given credentials embedded.
// Format: http://<login>:<password>@host.docker.internal:<port>/git/<project>/<repo>.git
func (f *gitFixture) cloneURL(login, password string) string {
	return fmt.Sprintf("http://%s:%s@127.0.0.1:%d/git/%s/%s.git",
		login, password, f.port, f.project, f.repo)
}

// cloneURLProjectAuth returns the git clone URL using project:<proj>:<key> auth.
func (f *gitFixture) cloneURLProjectAuth() string {
	return fmt.Sprintf("http://project:%s:%s@127.0.0.1:%d/git/%s/%s.git",
		f.project, f.projectAPIKey, f.port, f.project, f.repo)
}

// bootAppWithGitRepo boots omnirepo in-process with a project + git repo,
// creates test users and API keys, and returns a fixture ready for DinD tests.
// The backend parameter selects "gogit" or "gitkit" per D-46.
func bootAppWithGitRepo(t *testing.T, backend string) *gitFixture {
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
	project := "gitconf"
	repo := "main"

	// Generate a user API key for the conformance test user.
	userKey, err := auth.GenerateAPIKey(auth.APIKeyKindUser)
	if err != nil {
		t.Fatalf("generate user api key: %v", err)
	}
	// Generate a project API key for D-31 variant.
	projKey, err := auth.GenerateAPIKey(auth.APIKeyKindProject)
	if err != nil {
		t.Fatalf("generate project api key: %v", err)
	}

	userLogin := "gituser"
	userPassword := fmt.Sprintf("user-pw-%d", time.Now().UnixNano())

	bs := map[string]any{
		"schema_version": 1,
		"super_admin": map[string]any{
			"login": adminLogin, "email": "admin@example.com", "password": adminPassword,
		},
		"users": []any{
			map[string]any{"login": userLogin, "email": "gituser@example.com", "password": userPassword},
		},
		"projects": []any{map[string]any{"name": project, "members": []string{userLogin}}},
		"repos": []any{
			map[string]any{"project": project, "type": "git", "name": repo, "public_read": false},
		},
		"api_keys": []any{
			map[string]any{
				"owner_kind": "user", "owner": userLogin,
				"name": "git-user-key", "token": userKey.Plaintext,
			},
			map[string]any{
				"owner_kind": "project", "owner": project,
				"name": "git-project-key", "token": projKey.Plaintext,
			},
		},
	}
	bsBytes, _ := json.Marshal(bs)
	bsPath := filepath.Join(dataRoot, "config", "bootstrap.json")
	if err := os.WriteFile(bsPath, bsBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := config.Defaults()
	cfg.DataRoot = dataRoot
	cfg.Bootstrap.Path = bsPath
	cfg.Server.ExternalHostnames = []string{"localhost", "127.0.0.1"}
	cfg.Server.GitBackend = backend

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

	f := &gitFixture{
		host:          host,
		port:          httpAddr.Port,
		httpEndpoint:  httpEndpoint,
		dataRoot:      dataRoot,
		adminLogin:    adminLogin,
		adminPassword: adminPassword,
		project:       project,
		repo:          repo,
		userLogin:     userLogin,
		userPassword:  userPassword,
		userAPIKey:    userKey.Plaintext,
		projectAPIKey: projKey.Plaintext,
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

// setRepoMaxPushBytes sets the per-repo push cap via direct DB update.
// Used by the oversize-push test to avoid shipping large bodies through DinD.
func setRepoMaxPushBytes(t *testing.T, dataRoot, projectName, repoName string, maxBytes int64) {
	t.Helper()
	dbPath := filepath.Join(dataRoot, "db", "omnirepo.sqlite")
	db, err := sql.Open("sqlite", dbPath+"?_txlock=immediate")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	var projectID int64
	if err := db.QueryRow(`SELECT id FROM projects WHERE name=?`, projectName).Scan(&projectID); err != nil {
		t.Fatalf("lookup project %q: %v", projectName, err)
	}
	res, err := db.Exec(`UPDATE repos SET git_max_push_bytes=? WHERE project_id=? AND name=? AND type='git'`,
		maxBytes, projectID, repoName)
	if err != nil {
		t.Fatalf("update repo max_push_bytes: %v", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		t.Fatalf("no repo rows updated for %s/%s", projectName, repoName)
	}
}

// loginAndGetCookie calls POST /api/v1/auth/login and returns the session cookie.
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
	// key=value format: "git-client=alpine/git:2.43.0@sha256:..."
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

// runGitScript runs a shell script using the local git CLI.
// Falls back to DinD when git is not available locally.
func runGitScript(t *testing.T, image, script string) (string, error) {
	t.Helper()
	script = "git config --global init.defaultBranch main 2>/dev/null || true\n" + script
	if _, err := exec.LookPath("git"); err == nil {
		return runLocalScript(t, script)
	}
	return runDockerScript(t, image, script)
}

func runLocalScript(t *testing.T, script string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	tmpHome := t.TempDir()
	// Redirect /tmp references to a unique temp dir so scripts don't collide.
	localTmp := filepath.Join(tmpHome, "tmp")
	_ = os.MkdirAll(localTmp, 0o755)
	script = fmt.Sprintf("export TMPDIR=%s\ncd %s\n%s", localTmp, localTmp, script)
	// Replace hardcoded /tmp/ paths in scripts with the unique temp dir.
	script = strings.ReplaceAll(script, "/tmp/repo", filepath.Join(localTmp, "repo"))
	script = strings.ReplaceAll(script, "/tmp/repo2", filepath.Join(localTmp, "repo2"))
	cmd := exec.CommandContext(ctx, "bash", "-c", script)
	cmd.Env = append(os.Environ(),
		"HOME="+tmpHome,
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_NOSYSTEM=1",
	)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func runDockerScript(t *testing.T, image, script string) (string, error) {
	t.Helper()
	containerName := fmt.Sprintf("omnirepo-gitconf-%d", time.Now().UnixNano())
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	args := []string{"run", "--rm", "--name", containerName, "--entrypoint", "sh"}
	if runtime.GOOS == "linux" {
		args = append(args, "--add-host", "host.docker.internal:host-gateway")
	}
	args = append(args, image, "-c", script)
	cmd := exec.CommandContext(ctx, "docker", args...)
	out, err := cmd.CombinedOutput()
	t.Cleanup(func() {
		_ = exec.Command("docker", "rm", "-f", containerName).Run()
	})
	return string(out), err
}
