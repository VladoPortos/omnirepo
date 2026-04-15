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
	if d.Trivy.CachePath != "/var/lib/omnirepo/trivy/cache" {
		t.Errorf("Trivy.CachePath = %q, want /var/lib/omnirepo/trivy/cache", d.Trivy.CachePath)
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
