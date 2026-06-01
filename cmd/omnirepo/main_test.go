package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestRunBadBootstrapExits2 injects a panicking osExit shim and asserts that
// a malformed bootstrap.json causes the dispatcher to call osExit(2) —
// proving the *app.ErrBootstrap → exit code 2 contract (pitfall "Bootstrap
// atomicity" in RESEARCH.md).
func TestRunBadBootstrapExits2(t *testing.T) {
	dir := t.TempDir()
	dataRoot := filepath.Join(dir, "data")
	if err := os.MkdirAll(filepath.Join(dataRoot, "config"), 0o700); err != nil {
		t.Fatal(err)
	}
	bootstrapPath := filepath.Join(dataRoot, "config", "bootstrap.json")
	raw, _ := json.Marshal(map[string]any{
		"schema_version": 99, // V1 violation
		"super_admin":    map[string]any{"login": "a", "email": "a@x", "password": "p"},
	})
	if err := os.WriteFile(bootstrapPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	// Config file.
	cfgPath := filepath.Join(dir, "omnirepo.yaml")
	yaml := []byte("server:\n  http_port: 0\n  https_port: 0\ndata_root: " + dataRoot + "\nbootstrap:\n  path: " + bootstrapPath + "\n")
	if err := os.WriteFile(cfgPath, yaml, 0o600); err != nil {
		t.Fatal(err)
	}

	// Shim osExit.
	orig := osExit
	defer func() { osExit = orig }()
	var got int
	osExit = func(code int) { got = code; panic(code) }

	defer func() {
		_ = recover() // swallow the panic from osExit
		if got != 2 {
			t.Fatalf("expected exit code 2, got %d", got)
		}
	}()
	runAndExit([]string{"serve", "--config", cfgPath})
	t.Fatalf("runAndExit should have panicked via osExit")
}
