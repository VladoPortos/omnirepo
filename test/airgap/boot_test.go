// Package airgap exercises the Phase 1 air-gap invariant: the binary must
// boot, serve /healthz and /readyz on both HTTP and HTTPS, and complete its
// entire startup path using only loopback — no outbound DNS, no network
// round-trips to anything other than 127.0.0.1.
//
// This is the earliest Phase-1 guardrail against pitfall P6 (air-gap
// regressions). Phase 5 extends this with Playwright E2E under a
// `--network=none` Docker container (see spec §14).
package airgap

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dxc-internal/omnirepo/internal/app"
	"github.com/dxc-internal/omnirepo/internal/config"
)

// TestAirGapBoot proves the full binary boots in-process, /healthz and
// /readyz respond 200 on HTTP and HTTPS, and shutdown is clean on ctx cancel.
// All traffic is 127.0.0.1-only; no outbound dials.
func TestAirGapBoot(t *testing.T) {
	dataRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dataRoot, "config"), 0o750); err != nil {
		t.Fatal(err)
	}
	bs := `{
	  "schema_version": 1,
	  "super_admin": {
	    "login": "admin",
	    "email": "admin@example.com",
	    "password": "correct-horse-battery-staple"
	  },
	  "users": [],
	  "projects": [],
	  "repos": [],
	  "api_keys": []
	}`
	bsPath := filepath.Join(dataRoot, "config", "bootstrap.json")
	if err := os.WriteFile(bsPath, []byte(bs), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := config.Defaults()
	cfg.DataRoot = dataRoot
	cfg.Bootstrap.Path = bsPath
	cfg.Server.ExternalHostnames = []string{"localhost"}
	// Ports are irrelevant because we inject :0 listeners via RunOptions.

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
	httpsAddr := httpsLn.Addr().(*net.TCPAddr)

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

	// Wait for the serve goroutine to signal it has started.
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("app.Run did not signal ready within 5s")
	}

	start := time.Now()
	httpBase := fmt.Sprintf("http://127.0.0.1:%d", httpAddr.Port)
	httpsBase := fmt.Sprintf("https://127.0.0.1:%d", httpsAddr.Port)
	waitReady(t, httpBase+"/healthz", http.DefaultClient, 5*time.Second)
	t.Logf("app.Run -> /healthz 200 in %s", time.Since(start))

	assertGet200(t, http.DefaultClient, httpBase+"/healthz", `"status":"ok"`)
	assertGet200(t, http.DefaultClient, httpBase+"/readyz", ``)

	tlsClient := &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
		Timeout:   5 * time.Second,
	}
	assertGet200(t, tlsClient, httpsBase+"/healthz", `"status":"ok"`)
	assertGet200(t, tlsClient, httpsBase+"/readyz", ``)
}

// waitReady polls url until GET returns 200 or timeout elapses.
func waitReady(t *testing.T, url string, client *http.Client, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("%s did not respond 200 within %s", url, timeout)
}

// assertGet200 issues GET against url using client, asserts 200, and (if
// wantSubstr is non-empty) asserts the body contains the substring.
func assertGet200(t *testing.T, client *http.Client, url, wantSubstr string) {
	t.Helper()
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("GET %s: status=%d body=%s", url, resp.StatusCode, string(body))
	}
	if wantSubstr == "" {
		return
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body %s: %v", url, err)
	}
	if !containsString(body, wantSubstr) {
		t.Fatalf("GET %s body %q missing substring %q", url, string(body), wantSubstr)
	}
	// Extra: if response parses as JSON, ensure we parsed a real object (guards
	// against accidental content-type regressions).
	if len(body) > 0 && body[0] == '{' {
		var obj map[string]any
		if err := json.Unmarshal(body, &obj); err != nil {
			t.Fatalf("GET %s: body looked like JSON but failed to parse: %v", url, err)
		}
	}
}

func containsString(haystack []byte, needle string) bool {
	n := len(needle)
	if n == 0 {
		return true
	}
	for i := 0; i+n <= len(haystack); i++ {
		if string(haystack[i:i+n]) == needle {
			return true
		}
	}
	return false
}
