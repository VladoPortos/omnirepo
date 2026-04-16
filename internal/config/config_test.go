package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dxc-internal/omnirepo/internal/config"
)

func TestDefaults(t *testing.T) {
	d := config.Defaults()

	if d.Server.HTTPPort != 8080 {
		t.Errorf("Server.HTTPPort = %d, want 8080", d.Server.HTTPPort)
	}
	if d.Server.HTTPSPort != 8443 {
		t.Errorf("Server.HTTPSPort = %d, want 8443", d.Server.HTTPSPort)
	}
	if d.DataRoot != "/var/lib/omnirepo" {
		t.Errorf("DataRoot = %q, want /var/lib/omnirepo", d.DataRoot)
	}
	if d.Auth.SessionTTL != 12*time.Hour {
		t.Errorf("Auth.SessionTTL = %s, want 12h", d.Auth.SessionTTL)
	}
	if d.Auth.DockerJWTTTL != 60*time.Minute {
		t.Errorf("Auth.DockerJWTTTL = %s, want 60m", d.Auth.DockerJWTTTL)
	}
	if d.Auth.SigV4Skew != 15*time.Minute {
		t.Errorf("Auth.SigV4Skew = %s, want 15m", d.Auth.SigV4Skew)
	}
	if d.Scan.DBWarnAgeDays != 7 {
		t.Errorf("Scan.DBWarnAgeDays = %d, want 7", d.Scan.DBWarnAgeDays)
	}
	if d.Log.Level != "info" {
		t.Errorf("Log.Level = %q, want info", d.Log.Level)
	}
	if d.Log.Format != "json" {
		t.Errorf("Log.Format = %q, want json", d.Log.Format)
	}
	if !d.AirGap.AllowExternalActions {
		t.Errorf("AirGap.AllowExternalActions = false, want true (default)")
	}
}

func TestLoadYAML(t *testing.T) {
	cfg, err := config.Load("testdata/omnirepo.example.yaml")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.Hostname != "omnirepo.dxc.local" {
		t.Errorf("Server.Hostname = %q, want omnirepo.dxc.local", cfg.Server.Hostname)
	}
	wantExt := []string{"omnirepo.dxc.local", "artifacts.dxc.local"}
	if len(cfg.Server.ExternalHostnames) != len(wantExt) {
		t.Fatalf("len ExternalHostnames = %d, want %d", len(cfg.Server.ExternalHostnames), len(wantExt))
	}
	for i, v := range wantExt {
		if cfg.Server.ExternalHostnames[i] != v {
			t.Errorf("ExternalHostnames[%d] = %q, want %q", i, cfg.Server.ExternalHostnames[i], v)
		}
	}
}

func TestEnvOverride(t *testing.T) {
	t.Setenv("OMNIREPO_SERVER__HTTP_PORT", "9000")
	t.Setenv("OMNIREPO_AIR_GAP__ALLOW_EXTERNAL_ACTIONS", "false")

	cfg, err := config.Load("testdata/omnirepo.example.yaml")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.HTTPPort != 9000 {
		t.Errorf("Server.HTTPPort = %d, want 9000 (env override)", cfg.Server.HTTPPort)
	}
	if cfg.AirGap.AllowExternalActions {
		t.Errorf("AirGap.AllowExternalActions = true, want false (env override)")
	}
}

func TestLoadMissingFileFromFlagErrors(t *testing.T) {
	_, err := config.Load("/nonexistent/path/to/config.yaml")
	if err == nil {
		t.Fatal("Load: want error for missing flag path, got nil")
	}
	if !strings.Contains(err.Error(), "/nonexistent/path/to/config.yaml") {
		t.Errorf("error %q does not name the missing path", err.Error())
	}
}

func TestLoadMissingFileFromEnvIsDefaultsOnly(t *testing.T) {
	t.Setenv("OMNIREPO_CONFIG", "/definitely/not/a/path.yaml")
	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load: want no error for missing env path, got %v", err)
	}
	if cfg.Server.HTTPPort != 8080 {
		t.Errorf("Server.HTTPPort = %d, want 8080 (defaults)", cfg.Server.HTTPPort)
	}
}

func TestBadYAMLReturnsTypedError(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(bad, []byte("server:\n  http_port: \"not-an-int\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := config.Load(bad)
	if err == nil {
		t.Fatal("Load: want error for bad YAML type, got nil")
	}
}

func TestLoadNoFileNoEnvReturnsDefaults(t *testing.T) {
	// Clear env override and use blank flag + default /var/lib/omnirepo/config/omnirepo.yaml path
	// which we can't stub without running as root. The contract is that missing defaults path
	// is silently tolerated (defaults-only).
	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.HTTPPort != 8080 {
		t.Errorf("Server.HTTPPort = %d, want 8080", cfg.Server.HTTPPort)
	}
}

func TestTrivyDefaults(t *testing.T) {
	d := config.Defaults()
	if d.Trivy.BinaryPath != "/usr/local/bin/trivy" {
		t.Errorf("Trivy.BinaryPath = %q, want /usr/local/bin/trivy", d.Trivy.BinaryPath)
	}
	if d.Trivy.DBPath != "/var/lib/omnirepo/trivy/db" {
		t.Errorf("Trivy.DBPath = %q, want /var/lib/omnirepo/trivy/db", d.Trivy.DBPath)
	}
	// P-2: Trivy resolves its DB at <--cache-dir>/db/. The default CachePath
	// MUST therefore be the parent directory of DBPath so the two align.
	// Prior default was /var/lib/omnirepo/trivy/cache which put Trivy's
	// --cache-dir next to the DB instead of above it, causing every fresh
	// install to fail with "DB error: --skip-db-update cannot be specified
	// on the first run" until an operator noticed.
	if d.Trivy.CachePath != "/var/lib/omnirepo/trivy" {
		t.Errorf("Trivy.CachePath = %q, want /var/lib/omnirepo/trivy", d.Trivy.CachePath)
	}
}

// TestTrivyDefaults_CachePathContainsDBSubdir locks the Trivy layout
// invariant: db_path must equal <cache_path>/db. If defaults ever drift,
// every fresh install silently scans with no DB. (P-2.)
func TestTrivyDefaults_CachePathContainsDBSubdir(t *testing.T) {
	d := config.Defaults()
	want := filepath.Join(d.Trivy.CachePath, "db")
	if d.Trivy.DBPath != want {
		t.Fatalf("Trivy.DBPath = %q, want %q (== <CachePath>/db)", d.Trivy.DBPath, want)
	}
}

func TestJobsDefaults(t *testing.T) {
	d := config.Defaults()
	if d.Jobs.SyncWorkers != 4 {
		t.Errorf("Jobs.SyncWorkers = %d, want 4", d.Jobs.SyncWorkers)
	}
	if d.Jobs.ScanWorkers != 2 {
		t.Errorf("Jobs.ScanWorkers = %d, want 2", d.Jobs.ScanWorkers)
	}
	if d.Jobs.PollInterval != 2*time.Second {
		t.Errorf("Jobs.PollInterval = %s, want 2s", d.Jobs.PollInterval)
	}
	if d.Jobs.ShutdownGraceSeconds != 30 {
		t.Errorf("Jobs.ShutdownGraceSeconds = %d, want 30", d.Jobs.ShutdownGraceSeconds)
	}
}

func TestJobsEnvOverride(t *testing.T) {
	t.Setenv("OMNIREPO_JOBS__SYNC_WORKERS", "8")
	t.Setenv("OMNIREPO_JOBS__SCAN_WORKERS", "3")
	t.Setenv("OMNIREPO_JOBS__POLL_INTERVAL", "500ms")
	t.Setenv("OMNIREPO_JOBS__SHUTDOWN_GRACE_SECONDS", "15")
	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Jobs.SyncWorkers != 8 {
		t.Errorf("Jobs.SyncWorkers = %d, want 8", cfg.Jobs.SyncWorkers)
	}
	if cfg.Jobs.ScanWorkers != 3 {
		t.Errorf("Jobs.ScanWorkers = %d, want 3", cfg.Jobs.ScanWorkers)
	}
	if cfg.Jobs.PollInterval != 500*time.Millisecond {
		t.Errorf("Jobs.PollInterval = %s, want 500ms", cfg.Jobs.PollInterval)
	}
	if cfg.Jobs.ShutdownGraceSeconds != 15 {
		t.Errorf("Jobs.ShutdownGraceSeconds = %d, want 15", cfg.Jobs.ShutdownGraceSeconds)
	}
}

func TestDockerDefaults(t *testing.T) {
	d := config.Defaults()
	if d.Docker.JWTTTLSeconds != 3600 {
		t.Errorf("Docker.JWTTTLSeconds = %d, want 3600", d.Docker.JWTTTLSeconds)
	}
	if d.Docker.UploadSessionTTLSeconds != 3600 {
		t.Errorf("Docker.UploadSessionTTLSeconds = %d, want 3600", d.Docker.UploadSessionTTLSeconds)
	}
}

func TestDockerEnvOverride(t *testing.T) {
	t.Setenv("OMNIREPO_DOCKER__JWT_TTL_SECONDS", "900")
	t.Setenv("OMNIREPO_DOCKER__UPLOAD_SESSION_TTL_SECONDS", "1800")
	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Docker.JWTTTLSeconds != 900 {
		t.Errorf("Docker.JWTTTLSeconds = %d, want 900", cfg.Docker.JWTTTLSeconds)
	}
	if cfg.Docker.UploadSessionTTLSeconds != 1800 {
		t.Errorf("Docker.UploadSessionTTLSeconds = %d, want 1800", cfg.Docker.UploadSessionTTLSeconds)
	}
}

func TestTrivyEnvOverride(t *testing.T) {
	t.Setenv("OMNIREPO_TRIVY__BINARY_PATH", "/x")
	t.Setenv("OMNIREPO_TRIVY__DB_PATH", "/y")
	t.Setenv("OMNIREPO_TRIVY__CACHE_PATH", "/z")
	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Trivy.BinaryPath != "/x" {
		t.Errorf("Trivy.BinaryPath = %q, want /x", cfg.Trivy.BinaryPath)
	}
	if cfg.Trivy.DBPath != "/y" {
		t.Errorf("Trivy.DBPath = %q, want /y", cfg.Trivy.DBPath)
	}
	if cfg.Trivy.CachePath != "/z" {
		t.Errorf("Trivy.CachePath = %q, want /z", cfg.Trivy.CachePath)
	}
}

// Phase 3 Plan 01 — new Regen / Sync / Signing config sections (D-35).

func TestRegenSyncSigningDefaults(t *testing.T) {
	d := config.Defaults()
	if d.Regen.DebounceMs != 2000 {
		t.Errorf("Regen.DebounceMs = %d, want 2000", d.Regen.DebounceMs)
	}
	if d.Regen.MaxWaitMs != 30000 {
		t.Errorf("Regen.MaxWaitMs = %d, want 30000", d.Regen.MaxWaitMs)
	}
	if d.Sync.MaxParallelDownloadsPerJob != 4 {
		t.Errorf("Sync.MaxParallelDownloadsPerJob = %d, want 4", d.Sync.MaxParallelDownloadsPerJob)
	}
	if d.Sync.UpstreamHTTPTimeout != 60*time.Second {
		t.Errorf("Sync.UpstreamHTTPTimeout = %s, want 60s", d.Sync.UpstreamHTTPTimeout)
	}
	if d.Signing.GPGKeyBits != 4096 {
		t.Errorf("Signing.GPGKeyBits = %d, want 4096", d.Signing.GPGKeyBits)
	}
}

func TestRegenSyncSigningEnvOverride(t *testing.T) {
	t.Setenv("OMNIREPO_REGEN__DEBOUNCE_MS", "500")
	t.Setenv("OMNIREPO_REGEN__MAX_WAIT_MS", "10000")
	t.Setenv("OMNIREPO_SYNC__MAX_PARALLEL_DOWNLOADS_PER_JOB", "8")
	t.Setenv("OMNIREPO_SYNC__UPSTREAM_HTTP_TIMEOUT", "30s")
	t.Setenv("OMNIREPO_SIGNING__GPG_KEY_BITS", "2048")
	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Regen.DebounceMs != 500 {
		t.Errorf("Regen.DebounceMs = %d, want 500", cfg.Regen.DebounceMs)
	}
	if cfg.Regen.MaxWaitMs != 10000 {
		t.Errorf("Regen.MaxWaitMs = %d, want 10000", cfg.Regen.MaxWaitMs)
	}
	if cfg.Sync.MaxParallelDownloadsPerJob != 8 {
		t.Errorf("Sync.MaxParallelDownloadsPerJob = %d, want 8", cfg.Sync.MaxParallelDownloadsPerJob)
	}
	if cfg.Sync.UpstreamHTTPTimeout != 30*time.Second {
		t.Errorf("Sync.UpstreamHTTPTimeout = %s, want 30s", cfg.Sync.UpstreamHTTPTimeout)
	}
	if cfg.Signing.GPGKeyBits != 2048 {
		t.Errorf("Signing.GPGKeyBits = %d, want 2048", cfg.Signing.GPGKeyBits)
	}
}
