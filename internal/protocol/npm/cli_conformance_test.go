package npm_test

import (
	"encoding/base64"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestNpmCLIConformance drives the real npm CLI against the registry:
// `npm publish` a fixture package, then `npm install` it into a fresh
// project with an isolated cache. Skipped when npm is unavailable.
func TestNpmCLIConformance(t *testing.T) {
	npmBin, err := exec.LookPath("npm")
	if err != nil {
		t.Skip("npm binary not available")
	}
	f := newFixture(t)
	f.seedRepo("pub", "open", true) // public_read so install needs no creds

	registry := f.srv.URL + "/pub/npm/open/"
	registryHost := strings.TrimPrefix(registry, "http:")

	// .npmrc: registry + _auth scoped to it. always-auth is implied for
	// the scoped _auth form in npm >= 9.
	authB64 := base64.StdEncoding.EncodeToString([]byte(f.login + ":" + f.password))
	npmrc := filepath.Join(t.TempDir(), "npmrc")
	if err := os.WriteFile(npmrc, []byte(
		"registry="+registry+"\n"+
			registryHost+":_auth="+authB64+"\n",
	), 0o600); err != nil {
		t.Fatal(err)
	}

	env := append(os.Environ(),
		"NPM_CONFIG_USERCONFIG="+npmrc,
		"NPM_CONFIG_CACHE="+t.TempDir(),
		"NO_UPDATE_NOTIFIER=1",
	)

	// Fixture package.
	pkgDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(pkgDir, "package.json"), []byte(`{
		"name": "omni-cli-fixture",
		"version": "1.0.0",
		"description": "conformance fixture",
		"main": "index.js"
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkgDir, "index.js"),
		[]byte("module.exports = 42;\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	publish := exec.Command(npmBin, "publish", "--registry", registry)
	publish.Dir = pkgDir
	publish.Env = env
	if out, err := publish.CombinedOutput(); err != nil {
		t.Fatalf("npm publish failed: %v\n%s", err, out)
	}

	// Install into a fresh project.
	appDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(appDir, "package.json"),
		[]byte(`{"name":"consumer","version":"1.0.0"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	install := exec.Command(npmBin, "install", "omni-cli-fixture@1.0.0", "--registry", registry, "--no-audit", "--no-fund")
	install.Dir = appDir
	install.Env = env
	if out, err := install.CombinedOutput(); err != nil {
		t.Fatalf("npm install failed: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(appDir, "node_modules", "omni-cli-fixture", "index.js")); err != nil {
		t.Errorf("installed package missing: %v", err)
	}

	// The publish round-tripped through the registry, not a local link.
	resp, err := http.Get(registry + "omni-cli-fixture")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("packument after CLI publish = %d", resp.StatusCode)
	}
}
