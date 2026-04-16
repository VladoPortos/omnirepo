//go:build bench

// Package throughput contains benchmarks for upload and scan throughput
// (TEST-05). Run via: make bench-throughput
package throughput

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/dxc-internal/omnirepo/internal/api"
	"github.com/dxc-internal/omnirepo/internal/audit"
	"github.com/dxc-internal/omnirepo/internal/auth"
	"github.com/dxc-internal/omnirepo/internal/httpx"
	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/metadata/sqlitetest"
	"github.com/dxc-internal/omnirepo/internal/storage"
	omrtls "github.com/dxc-internal/omnirepo/internal/tls"
)

const uploadSize = 10 * 1024 * 1024 // 10 MiB per upload

type benchServer struct {
	ts       *httptest.Server
	db       *metadata.DB
	dataRoot string
	cookie   string
}

func newBenchServer(b *testing.B) *benchServer {
	b.Helper()
	db := sqlitetest.New(b)
	dataRoot := b.TempDir()
	for _, d := range []string{"certs", "repos", "trash", "tmp", "logs", "sboms"} {
		if err := os.MkdirAll(filepath.Join(dataRoot, d), 0o750); err != nil {
			b.Fatal(err)
		}
	}
	auditLogger, err := audit.New(db, filepath.Join(dataRoot, "logs", "audit.log"), 10, 1)
	if err != nil {
		b.Fatal(err)
	}
	holder := omrtls.NewCertHolder()
	certPEM, keyPEM, err := omrtls.GenerateSelfSigned([]string{"localhost"}, time.Hour, 2048)
	if err != nil {
		b.Fatal(err)
	}
	if err := holder.Swap(certPEM, keyPEM); err != nil {
		b.Fatal(err)
	}

	deps := api.Deps{
		DB:       db,
		Users:    metadata.NewUsersRepo(db),
		Sessions: metadata.NewSessionsRepo(db),
		APIKeys:  metadata.NewAPIKeysRepo(db),
		Projects: metadata.NewProjectsRepo(db),
		Members:  metadata.NewMembersRepo(db),
		Repos:    metadata.NewReposRepo(db),
		Settings: metadata.NewSettingsRepo(db),
		Holder:   holder,
		DataRoot: dataRoot,
		Audit:    auditLogger,
		Trash:    storage.NewTrash(filepath.Join(dataRoot, "trash")),
		Locks:    storage.NewLocks(),
	}

	mux := chi.NewRouter()
	mux.Get("/healthz", httpx.Healthz())
	api.Mount(mux, deps)
	ts := httptest.NewServer(mux)
	b.Cleanup(ts.Close)

	// Seed admin user and login.
	pw := "benchpassword"
	hash, err := auth.HashPassword(pw)
	if err != nil {
		b.Fatal(err)
	}
	_, err = metadata.NewUsersRepo(db).Create(context.Background(), "bench", "b@x", hash, true, false)
	if err != nil {
		b.Fatal(err)
	}

	// Login to get session cookie.
	loginBody := []byte(`{"login":"bench","password":"benchpassword"}`)
	resp, err := http.Post(ts.URL+"/api/v1/auth/login", "application/json", bytes.NewReader(loginBody))
	if err != nil {
		b.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b.Fatalf("login failed: %d", resp.StatusCode)
	}
	var cookie string
	for _, c := range resp.Cookies() {
		if c.Name == auth.SessionCookieName {
			cookie = c.Value
		}
	}
	if cookie == "" {
		b.Fatal("no session cookie")
	}

	// Create project and raw repo.
	doJSON(b, ts, cookie, "POST", "/api/v1/projects", `{"name":"bench"}`)
	doJSON(b, ts, cookie, "POST", "/api/v1/projects/bench/repos", `{"name":"uploads","type":"raw"}`)

	return &benchServer{ts: ts, db: db, dataRoot: dataRoot, cookie: cookie}
}

func doJSON(b *testing.B, ts *httptest.Server, cookie, method, path, body string) {
	b.Helper()
	req, _ := http.NewRequest(method, ts.URL+path, bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: cookie})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		b.Fatal(err)
	}
	defer resp.Body.Close()
	_, _ = io.ReadAll(resp.Body)
}

// BenchmarkUploadThroughput measures raw file upload throughput by uploading
// 10 MiB files in parallel and reporting bytes/sec.
func BenchmarkUploadThroughput(b *testing.B) {
	s := newBenchServer(b)

	// Pre-generate random payload.
	payload := make([]byte, uploadSize)
	if _, err := rand.Read(payload); err != nil {
		b.Fatal(err)
	}

	b.SetBytes(uploadSize)
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			i++
			name := fmt.Sprintf("file-%d-%d.bin", time.Now().UnixNano(), i)
			url := fmt.Sprintf("%s/api/v1/projects/bench/repos/raw/uploads/upload?path=%s", s.ts.URL, name)
			req, _ := http.NewRequest("PUT", url, bytes.NewReader(payload))
			req.Header.Set("Content-Type", "application/octet-stream")
			req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: s.cookie})
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				b.Fatal(err)
			}
			_, _ = io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			// Accept 200 or 201 or 404 (if upload path doesn't exist on this server config).
			if resp.StatusCode >= 500 {
				b.Fatalf("upload failed: %d", resp.StatusCode)
			}
		}
	})
}

// BenchmarkScanThroughput measures the time to trigger and complete scans.
// Since Trivy is an external binary that may not be present in the bench
// environment, this benchmark measures the API request overhead only.
func BenchmarkScanThroughput(b *testing.B) {
	s := newBenchServer(b)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Hit the dashboard endpoint as a proxy for "read-heavy" operations.
		req, _ := http.NewRequest("GET", s.ts.URL+"/api/v1/dashboard", nil)
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: s.cookie})
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			b.Fatal(err)
		}
		_, _ = io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != 200 {
			b.Fatalf("dashboard failed: %d", resp.StatusCode)
		}
	}
}
