package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dxc-internal/omnirepo/internal/config"
)

// Phase 4 Plan 03 — git_backend + repos.git.max_push_bytes + external_hostnames.

func TestPhase04DefaultsGitConfig(t *testing.T) {
	d := config.Defaults()
	if d.Server.GitBackend != "gogit" {
		t.Errorf("Server.GitBackend = %q, want gogit", d.Server.GitBackend)
	}
	if d.Repos.Git.MaxPushBytes != 524288000 {
		t.Errorf("Repos.Git.MaxPushBytes = %d, want 524288000 (500 MiB)", d.Repos.Git.MaxPushBytes)
	}
	if d.Server.ExternalHostnames == nil {
		t.Errorf("Server.ExternalHostnames should be an empty slice, not nil")
	}
	if len(d.Server.ExternalHostnames) != 0 {
		t.Errorf("Server.ExternalHostnames default len = %d, want 0", len(d.Server.ExternalHostnames))
	}
}

func TestPhase04LoadDefaultsYieldsGitConfig(t *testing.T) {
	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.GitBackend != "gogit" {
		t.Errorf("GitBackend = %q, want gogit", cfg.Server.GitBackend)
	}
	if cfg.Repos.Git.MaxPushBytes != 524288000 {
		t.Errorf("MaxPushBytes = %d, want 524288000", cfg.Repos.Git.MaxPushBytes)
	}
}

func TestPhase04GitBackendGitkitAccepted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	yaml := "server:\n  git_backend: gitkit\n"
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.GitBackend != "gitkit" {
		t.Errorf("GitBackend = %q, want gitkit", cfg.Server.GitBackend)
	}
}

func TestPhase04GitBackendInvalidRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	yaml := "server:\n  git_backend: svn\n"
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := config.Load(path)
	if err == nil {
		t.Fatal("Load: want error for invalid git_backend, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "git_backend") {
		t.Errorf("error %q does not mention git_backend", msg)
	}
	if !strings.Contains(msg, "gogit") || !strings.Contains(msg, "gitkit") {
		t.Errorf("error %q does not list valid backends gogit|gitkit", msg)
	}
}

func TestPhase04MaxPushBytesNegativeRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	yaml := "repos:\n  git:\n    max_push_bytes: -1\n"
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := config.Load(path)
	if err == nil {
		t.Fatal("Load: want error for negative max_push_bytes, got nil")
	}
	if !strings.Contains(err.Error(), "max_push_bytes") {
		t.Errorf("error %q does not mention max_push_bytes", err.Error())
	}
}

func TestPhase04MaxPushBytesZeroAppliesDefault(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	yaml := "repos:\n  git:\n    max_push_bytes: 0\n"
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Repos.Git.MaxPushBytes != 524288000 {
		t.Errorf("MaxPushBytes = %d, want 524288000 (default applied when 0)", cfg.Repos.Git.MaxPushBytes)
	}
}

func TestPhase04GitBackendEnvOverride(t *testing.T) {
	t.Setenv("OMNIREPO_SERVER__GIT_BACKEND", "gitkit")
	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.GitBackend != "gitkit" {
		t.Errorf("GitBackend = %q, want gitkit (env override)", cfg.Server.GitBackend)
	}
}

func TestPhase04MaxPushBytesEnvOverride(t *testing.T) {
	t.Setenv("OMNIREPO_REPOS__GIT__MAX_PUSH_BYTES", "1048576")
	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Repos.Git.MaxPushBytes != 1048576 {
		t.Errorf("MaxPushBytes = %d, want 1048576", cfg.Repos.Git.MaxPushBytes)
	}
}
