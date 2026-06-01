// Air-gap extension: probes the four new package-protocol
// endpoints (rpm, deb, pypi, helm) plus their /public-key.asc files on the
// in-process app without ever dialing outside loopback. Also exercises the
// unreachable-upstream path with a deterministic failure bound:
// the test reserves then closes a local TCP port and overrides
// SyncConfig.UpstreamHTTPTimeout to 5s so the failure cannot exceed
// the per-test budget regardless of CI network-stack quirks.
//
// Runs alongside TestAirGapBoot + TestAirgapOCIRawEndpoints under
// `make test-airgap`; the --network=none enforcement itself is applied by
// the CI job wrapper.
package airgap

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vladoportos/omnirepo/internal/app"
	"github.com/vladoportos/omnirepo/internal/config"
)

// TestPhase3RoutesAirGap boots the app on loopback with all package-protocol
// repos pre-bootstrapped, hits each protocol's metadata endpoint, asserts
// /public-key.asc returns armored OpenPGP for rpm/deb, and runs an
// unreachable-upstream sync to prove the failure path lands within budget.
func TestPhase3RoutesAirGap(t *testing.T) {
	dataRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dataRoot, "config"), 0o750); err != nil {
		t.Fatal(err)
	}
	adminLogin := "admin"
	adminPassword := "airgap-phase3-correct-horse-staple"
	project := "p"

	// Bootstrap only seeds the admin + project. Repos are created via the
	// REST API below so the rpm/deb create hooks (eager signing-key
	// generation) actually fire — bootstrap inserts repos rows
	// directly without composing the hook chain.
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
	cfg.Server.ExternalHostnames = []string{"localhost"}
	// WithSyncUpstreamTimeout: override SyncConfig.UpstreamHTTPTimeout so the
	// unreachable-upstream sync fails inside a deterministic 5s budget.
	cfg.Sync.UpstreamHTTPTimeout = 5 * time.Second

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
	httpBase := fmt.Sprintf("http://127.0.0.1:%d", httpAddr.Port)

	// Reserve then close a local TCP port to obtain a provably-closed loopback
	// address. Targeting this address makes connect() fail fast with
	// ECONNREFUSED on Linux/macOS — independent of any non-routable public-IP
	// blackhole behaviour the CI runner might exhibit.
	closedLn, lerr := net.Listen("tcp", "127.0.0.1:0")
	if lerr != nil {
		t.Fatalf("reserve port: %v", lerr)
	}
	closedAddr := closedLn.Addr().String()
	_ = closedLn.Close()

	ready := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- app.Run(ctx, cfg, app.RunOptions{
			HTTPListener: httpLn, HTTPSListener: httpsLn, Ready: ready,
		})
	}()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("app.Run did not return within 5s of ctx cancel")
		}
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

	client := &http.Client{Timeout: 5 * time.Second}
	waitReady(t, httpBase+"/healthz", client, 5*time.Second)

	basic := "Basic " + base64.StdEncoding.EncodeToString([]byte(adminLogin+":"+adminPassword))

	// /api/v1/* uses session cookies, not Basic — log in once and reuse the
	// cookie for the four repo creates and the sync enqueue below.
	sessionCookie := loginAdminViaREST(t, client, httpBase, adminLogin, adminPassword)

	// Enroll the super-admin as a project member so the sync enqueue
	// further down satisfies the membership check.
	addProjectMember(t, client, httpBase, project, adminLogin, sessionCookie)

	// Create the four protocol repos via REST so the rpm/deb create hooks
	// (eager signing-key generation) actually fire. Each call must
	// complete on loopback only — no outbound network, no DNS lookup.
	createRepoViaREST(t, client, httpBase, project, "rpm", "rpm1", sessionCookie)
	createRepoViaREST(t, client, httpBase, project, "deb", "deb1", sessionCookie)
	createRepoViaREST(t, client, httpBase, project, "pypi", "pypi1", sessionCookie)
	createRepoViaREST(t, client, httpBase, project, "helm", "helm1", sessionCookie)

	// 1. Each protocol's metadata GET — expect either 200 (empty index) or
	//    404 (no regen yet). Critical: must NOT timeout, no DNS lookup.
	mustGet(t, client, httpBase+"/p/rpm/rpm1/repodata/repomd.xml", []int{200, 404})
	mustGet(t, client, httpBase+"/p/deb/deb1/dists/stable/InRelease", []int{200, 404})
	mustGet(t, client, httpBase+"/p/pypi/pypi1/simple/", []int{200, 404})
	mustGet(t, client, httpBase+"/p/helm/helm1/index.yaml", []int{200, 404})

	// 2. /public-key.asc for rpm + deb (eager key generation at repo create).
	rpmKey := mustGet(t, client, httpBase+"/p/rpm/rpm1/public-key.asc", []int{200})
	if !bytes.HasPrefix(rpmKey.Body, []byte("-----BEGIN PGP PUBLIC KEY BLOCK-----")) {
		t.Fatalf("rpm public-key.asc not armored: %q", rpmKey.Body[:min(80, len(rpmKey.Body))])
	}
	debKey := mustGet(t, client, httpBase+"/p/deb/deb1/public-key.asc", []int{200})
	if !bytes.HasPrefix(debKey.Body, []byte("-----BEGIN PGP PUBLIC KEY BLOCK-----")) {
		t.Fatalf("deb public-key.asc not armored: %q", debKey.Body[:min(80, len(debKey.Body))])
	}

	// 3. Unreachable-upstream path. Enqueue a helm sync against the
	//    closed local port; assert the row reaches status='failed' inside 10s
	//    (UpstreamHTTPTimeout=5s + ~1s slack for the SyncPool to dispatch).
	syncURL := httpBase + "/api/v1/projects/" + project + "/repos/helm/helm1/sync"
	body := fmt.Sprintf(`{"upstream_url":"http://%s/doesnotexist/"}`, closedAddr)
	req, _ := http.NewRequest(http.MethodPost, syncURL, strings.NewReader(body))
	req.AddCookie(sessionCookie)
	req.Header.Set("Content-Type", "application/json")
	_ = basic // retained: useful for ad-hoc Basic-auth probes if the test grows
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST sync: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusAccepted {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST sync: status=%d body=%s", resp.StatusCode, string(respBody))
	}
	var enqResp struct {
		JobID int64  `json:"job_id"`
		Kind  string `json:"kind"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&enqResp); err != nil {
		t.Fatalf("decode sync resp: %v", err)
	}
	if enqResp.JobID == 0 || enqResp.Kind != "helm_sync" {
		t.Fatalf("bad sync response: %+v", enqResp)
	}

	// Poll directly against the SQLite DB the in-process app is using.
	dbPath := filepath.Join(dataRoot, "db", "omnirepo.sqlite")
	dbConn, dberr := sql.Open("sqlite", dbPath)
	if dberr != nil {
		t.Fatalf("open sqlite: %v", dberr)
	}
	defer func() { _ = dbConn.Close() }()

	// Acceptance: the job MUST record a non-empty last_error inside 10s
	// (proving the unreachable upstream did not hang). The worker may either
	// have flipped status to 'failed' (no more attempts) or to 'pending'
	// (scheduled for retry) — both states are valid outcomes here; the
	// invariant under test is "fail-fast with a recorded error", not the
	// retry policy itself.
	deadline := time.Now().Add(10 * time.Second)
	var status, lastErr string
	for time.Now().Before(deadline) {
		row := dbConn.QueryRowContext(context.Background(),
			`SELECT status, COALESCE(last_error, '') FROM sync_jobs WHERE id=?`, enqResp.JobID)
		if err := row.Scan(&status, &lastErr); err == nil && lastErr != "" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if lastErr == "" {
		t.Fatalf("sync_job %d did not record last_error within 10s (status=%q)", enqResp.JobID, status)
	}
	if status != "failed" && status != "pending" {
		t.Fatalf("sync_job %d unexpected status=%q (want failed|pending) last_error=%q",
			enqResp.JobID, status, lastErr)
	}
	if strings.Contains(lastErr, "Authorization:") || strings.Contains(strings.ToLower(lastErr), "basic ") {
		t.Fatalf("sync_job last_error leaks credentials: %q", lastErr)
	}
}

// min returns the smaller of a and b. (Pre-Go 1.21 helper retained because
// the airgap package targets the same compiler floor as the rest of the repo.)
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// httpResp pairs an http.Response status with its body bytes.
type httpResp struct {
	Status int
	Body   []byte
}

// mustGet performs GET url with the given client, asserts the response
// status is in wantStatus, and returns the (status, body) pair.
func mustGet(t *testing.T, client *http.Client, url string, wantStatus []int) httpResp {
	t.Helper()
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	for _, want := range wantStatus {
		if resp.StatusCode == want {
			return httpResp{Status: resp.StatusCode, Body: body}
		}
	}
	t.Fatalf("GET %s: status=%d (want one of %v) body=%s", url, resp.StatusCode, wantStatus, string(body))
	return httpResp{}
}

// loginAdminViaREST POSTs to /api/v1/auth/login and returns the session
// cookie. The cookie is reused for the four repo creates and the sync
// enqueue below — the admin REST surface uses cookie-based session auth,
// not Basic.
func loginAdminViaREST(t *testing.T, client *http.Client, httpBase, login, password string) *http.Cookie {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"login": login, "password": password})
	url := httpBase + "/api/v1/auth/login"
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST %s: status=%d body=%s", url, resp.StatusCode, string(respBody))
	}
	for _, c := range resp.Cookies() {
		if c.Name == "omnirepo_session" {
			return c
		}
	}
	t.Fatalf("POST %s: no omnirepo_session cookie set", url)
	return nil
}

// createRepoViaREST POSTs to /api/v1/projects/{project}/repos so the
// composed repo-create hook chain (rpm/deb signing-key gen + deb apt_suites
// seed) runs inside the writer tx. public_read=true so subsequent
// anonymous metadata GETs in the same test work without auth.
func createRepoViaREST(t *testing.T, client *http.Client, httpBase, project, repoType, repoName string, sessionCookie *http.Cookie) {
	t.Helper()
	publicRead := true
	body, _ := json.Marshal(map[string]any{
		"name":        repoName,
		"type":        repoType,
		"public_read": publicRead,
	})
	url := httpBase + "/api/v1/projects/" + project + "/repos"
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	req.AddCookie(sessionCookie)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST %s (%s): %v", url, repoType, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST %s: status=%d body=%s", url, resp.StatusCode, string(respBody))
	}
}

// addProjectMember POSTs /api/v1/projects/{name}/members/{login} so the
// supplied login becomes a member of the project — required for the
// sync REST endpoint's project-write check (sync_rest.go IsMember gate).
func addProjectMember(t *testing.T, client *http.Client, httpBase, project, login string, sessionCookie *http.Cookie) {
	t.Helper()
	url := httpBase + "/api/v1/projects/" + project + "/members/" + login
	req, _ := http.NewRequest(http.MethodPost, url, nil)
	req.AddCookie(sessionCookie)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	// 200/204 = added; 409 = already a member (treat as success).
	if resp.StatusCode/100 != 2 && resp.StatusCode != http.StatusConflict {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST %s: status=%d body=%s", url, resp.StatusCode, string(respBody))
	}
}
