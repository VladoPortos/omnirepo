// Air-gap extension: probes /v2/_catalog, a /v2 manifest
// endpoint, and /<project>/raw/<repo>/<path> on the in-process app without
// ever dialing outside loopback. Runs alongside the boot test under
// `make test-airgap`; the --network=none enforcement itself is applied by
// the CI job wrapper.
package airgap

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vladoportos/omnirepo/internal/app"
	"github.com/vladoportos/omnirepo/internal/config"
)

// TestAirgapOCIRawEndpoints boots the app in-process on a 127.0.0.1:0
// listener, bootstraps a super-admin + project + docker repo + raw repo
// (both public_read=true), seeds a raw file via authenticated PUT, and
// probes three endpoints that must work entirely on loopback:
//
//  1. GET /v2/_catalog (anonymous, project-scoped)
//  2. GET /v2/<proj>/docker/<repo>/manifests/<tag> (Bearer — 404 ok; proves
//     the handler is wired and reachable without network)
//  3. GET /<proj>/raw/<repo>/<path> (anonymous, public_read)
//
// No external URL is ever dialed; the goroutine dump on failure would
// surface any accidental outbound call.
func TestAirgapOCIRawEndpoints(t *testing.T) {
	dataRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dataRoot, "config"), 0o750); err != nil {
		t.Fatal(err)
	}

	adminLogin := "admin"
	adminPassword := "airgap-correct-horse-battery-staple"
	project := "airp"
	dockerRepo := "img"
	rawRepo := "blobs"
	rawPath := "hello.txt"
	rawBody := []byte("hello from air-gap\n")

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
			map[string]any{"project": project, "type": "docker", "name": dockerRepo, "public_read": true},
			map[string]any{"project": project, "type": "raw", "name": rawRepo, "public_read": true},
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
	cfg.Server.ExternalHostnames = []string{"localhost"}

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
	case <-time.After(10 * time.Second):
		cancel()
		t.Fatal("app.Run did not signal ready within 10s")
	}

	waitReady(t, httpBase+"/healthz", http.DefaultClient, 5*time.Second)

	client := http.DefaultClient
	basic := "Basic " + base64.StdEncoding.EncodeToString([]byte(adminLogin+":"+adminPassword))

	// --- Seed the raw file (authenticated PUT). ---
	rawURL := fmt.Sprintf("%s/%s/raw/%s/%s", httpBase, project, rawRepo, rawPath)
	putReq, _ := http.NewRequest("PUT", rawURL, bytes.NewReader(rawBody))
	putReq.Header.Set("Authorization", basic)
	putReq.Header.Set("Content-Type", "text/plain")
	putResp, err := client.Do(putReq)
	if err != nil {
		t.Fatalf("PUT raw: %v", err)
	}
	_ = putResp.Body.Close()
	if putResp.StatusCode != 201 && putResp.StatusCode != 200 && putResp.StatusCode != 204 {
		t.Fatalf("PUT %s: unexpected status %d", rawURL, putResp.StatusCode)
	}

	// --- Probe 1: anonymous /v2/_catalog returns 200 and mentions the repo. ---
	catURL := httpBase + "/v2/_catalog"
	catResp, body := doGet(t, client, catURL, "")
	if catResp.StatusCode != 200 {
		t.Fatalf("GET %s: status=%d body=%s", catURL, catResp.StatusCode, string(body))
	}
	if !bytes.Contains(body, []byte(project+"/docker/"+dockerRepo)) {
		t.Fatalf("GET %s body missing %q: %s", catURL, project+"/docker/"+dockerRepo, string(body))
	}

	// --- Probe 2: GET /v2/<proj>/docker/<repo>/manifests/<tag> via Bearer. ---
	// No manifest has been pushed, so 404 MANIFEST_UNKNOWN is the expected
	// response. That still proves the handler is wired, reached without
	// network, and emits a spec-compliant OCI error envelope.
	tokenURL := httpBase + "/v2/token"
	tokReq, _ := http.NewRequest("GET", tokenURL, nil)
	tokReq.Header.Set("Authorization", basic)
	tokResp, err := client.Do(tokReq)
	if err != nil {
		t.Fatalf("GET /v2/token: %v", err)
	}
	var tokPayload struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(tokResp.Body).Decode(&tokPayload); err != nil {
		_ = tokResp.Body.Close()
		t.Fatalf("decode /v2/token: %v", err)
	}
	_ = tokResp.Body.Close()
	if tokPayload.Token == "" {
		t.Fatalf("/v2/token returned empty token (status=%d)", tokResp.StatusCode)
	}

	manifestURL := fmt.Sprintf("%s/v2/%s/docker/%s/manifests/latest", httpBase, project, dockerRepo)
	manResp, manBody := doGet(t, client, manifestURL, "Bearer "+tokPayload.Token)
	if manResp.StatusCode != 404 && manResp.StatusCode != 200 {
		t.Fatalf("GET %s: unexpected status=%d body=%s", manifestURL, manResp.StatusCode, string(manBody))
	}
	// On 404 the body must be an OCI error envelope with MANIFEST_UNKNOWN.
	if manResp.StatusCode == 404 && !bytes.Contains(manBody, []byte("MANIFEST_UNKNOWN")) {
		t.Fatalf("GET %s: 404 but missing MANIFEST_UNKNOWN error code: %s", manifestURL, string(manBody))
	}

	// --- Probe 3: anonymous raw GET on public_read=true repo returns 200 + body. ---
	rawResp, rawGetBody := doGet(t, client, rawURL, "")
	if rawResp.StatusCode != 200 {
		t.Fatalf("GET %s: status=%d body=%s", rawURL, rawResp.StatusCode, string(rawGetBody))
	}
	if !bytes.Equal(rawGetBody, rawBody) {
		t.Fatalf("GET %s: body mismatch: got=%q want=%q", rawURL, string(rawGetBody), string(rawBody))
	}
}

func doGet(t *testing.T, client *http.Client, url, auth string) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return resp, body
}
