package app_test

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dxc-internal/omnirepo/internal/app"
	"github.com/dxc-internal/omnirepo/internal/config"
)

// newTestConfig returns a config pointing at a fresh temp data root. Bootstrap
// path may be empty (skip) or a file to ingest.
func newTestConfig(t *testing.T, bootstrapPath string) config.Config {
	t.Helper()
	cfg := config.Defaults()
	cfg.DataRoot = t.TempDir()
	cfg.Bootstrap.Path = bootstrapPath
	return cfg
}

// tcpPair returns two net.Listeners bound to 127.0.0.1:0 (ephemeral).
func tcpPair(t *testing.T) (net.Listener, net.Listener) {
	t.Helper()
	a, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	b, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		_ = a.Close()
		t.Fatal(err)
	}
	return a, b
}

func waitFor(t *testing.T, url string, transport http.RoundTripper, timeout time.Duration) *http.Response {
	t.Helper()
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second}
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			return resp
		}
		lastErr = err
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("wait for %s timed out: %v", url, lastErr)
	return nil
}

func TestRunHappyPath(t *testing.T) {
	cfg := newTestConfig(t, "") // no bootstrap file
	httpLn, httpsLn := tcpPair(t)
	httpAddr := httpLn.Addr().String()
	httpsAddr := httpsLn.Addr().String()

	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- app.Run(ctx, cfg, app.RunOptions{HTTPListener: httpLn, HTTPSListener: httpsLn, Ready: ready})
	}()
	<-ready

	// HTTP /healthz.
	resp := waitFor(t, "http://"+httpAddr+"/healthz", http.DefaultTransport, 3*time.Second)
	if resp.StatusCode != 200 {
		t.Fatalf("http code=%d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// HTTPS /healthz with self-signed cert.
	tr := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	resp = waitFor(t, "https://"+httpsAddr+"/healthz", tr, 3*time.Second)
	if resp.StatusCode != 200 {
		t.Fatalf("https code=%d", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if !strings.Contains(string(b), `"status":"ok"`) {
		t.Fatalf("https body=%s", b)
	}

	// /readyz on both.
	for _, u := range []string{"http://" + httpAddr + "/readyz", "https://" + httpsAddr + "/readyz"} {
		resp, err := (&http.Client{Transport: tr, Timeout: 2 * time.Second}).Get(u)
		if err != nil {
			t.Fatalf("readyz %s: %v", u, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("readyz %s code=%d", u, resp.StatusCode)
		}
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run returned: %v", err)
	}
}

func TestRun_FirstBootWritesSelfSignedCert(t *testing.T) {
	cfg := newTestConfig(t, "")
	httpLn, httpsLn := tcpPair(t)

	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan struct{})
	go func() { _ = app.Run(ctx, cfg, app.RunOptions{HTTPListener: httpLn, HTTPSListener: httpsLn, Ready: ready}) }()
	<-ready
	defer cancel()

	if _, err := os.Stat(filepath.Join(cfg.DataRoot, "certs", "server.crt")); err != nil {
		t.Fatalf("first-boot cert not written: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg.DataRoot, "certs", "server.key")); err != nil {
		t.Fatalf("first-boot key not written: %v", err)
	}
}

func TestRun_BadBootstrapReturnsErrBootstrap(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bootstrap.json")
	// schema_version 99 → V1 violation.
	raw, _ := json.Marshal(map[string]any{
		"schema_version": 99,
		"super_admin":    map[string]any{"login": "a", "email": "a@x", "password": "p"},
	})
	if err := os.WriteFile(bad, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := newTestConfig(t, bad)
	httpLn, httpsLn := tcpPair(t)
	defer func() { _ = httpLn.Close(); _ = httpsLn.Close() }()

	err := app.Run(context.Background(), cfg, app.RunOptions{HTTPListener: httpLn, HTTPSListener: httpsLn})
	if err == nil {
		t.Fatalf("expected error")
	}
	var be *app.ErrBootstrap
	if !errors.As(err, &be) {
		t.Fatalf("expected *app.ErrBootstrap, got %T: %v", err, err)
	}
}

func TestRun_IdempotentWhenDBAlreadySeeded(t *testing.T) {
	// 1st pass: seed.
	goodPath := filepath.Join(t.TempDir(), "bootstrap.json")
	b := goodBootstrap()
	raw, _ := json.Marshal(b)
	if err := os.WriteFile(goodPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := newTestConfig(t, goodPath)

	httpLn, httpsLn := tcpPair(t)
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan struct{})
	go func() { _ = app.Run(ctx, cfg, app.RunOptions{HTTPListener: httpLn, HTTPSListener: httpsLn, Ready: ready}) }()
	<-ready
	resp := waitFor(t, "http://"+httpLn.Addr().String()+"/healthz", http.DefaultTransport, 3*time.Second)
	_ = resp.Body.Close()
	cancel()
	time.Sleep(100 * time.Millisecond)

	// 2nd pass: should skip bootstrap silently (DB already has users).
	httpLn2, httpsLn2 := tcpPair(t)
	ctx2, cancel2 := context.WithCancel(context.Background())
	ready2 := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- app.Run(ctx2, cfg, app.RunOptions{HTTPListener: httpLn2, HTTPSListener: httpsLn2, Ready: ready2})
	}()
	<-ready2
	resp = waitFor(t, "http://"+httpLn2.Addr().String()+"/healthz", http.DefaultTransport, 3*time.Second)
	if resp.StatusCode != 200 {
		t.Fatalf("healthz code=%d", resp.StatusCode)
	}
	_ = resp.Body.Close()
	cancel2()
	if err := <-done; err != nil {
		t.Fatalf("second run: %v", err)
	}
}
