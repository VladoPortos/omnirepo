//go:build conformance

package pypi_conformance

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

const (
	pkgName = "omnirepo-conf"
	version = "0.1.0"
)

// wheelFilename is the canonical wheel filename for the synthetic package.
// (PEP 427: name/version normalized; tags py3-none-any.)
var wheelFilename = func() string {
	return strings.ReplaceAll(pkgName, "-", "_") + "-" + version + "-py3-none-any.whl"
}()

func uploadFixtureWheel(t *testing.T, fx *bootFixture) {
	t.Helper()
	wheel := makeWheelBytes(t, pkgName, version)
	fx.twineUpload(t, wheelFilename, wheel)
}

func simpleProjectURL(fx *bootFixture, project string) string {
	return fmt.Sprintf("http://%s/%s/pypi/%s/simple/%s/", fx.host, fx.project, fx.repo, project)
}

// TestPipInstallFromOmniRepo proves `pip install --index-url` succeeds
// against the served PEP 503 simple index.
func TestPipInstallFromOmniRepo(t *testing.T) {
	fx := bootAppWithRepo(t, "pypi")
	uploadFixtureWheel(t, fx)
	waitForSimpleIndex(t, simpleProjectURL(fx, pkgName), 10*time.Second)

	image := resolveImage(t)
	indexURL := fmt.Sprintf("http://host.docker.internal:%d/%s/pypi/%s/simple/", fx.port, fx.project, fx.repo)
	// Warm vpnkit before pip runs (Docker Desktop on WSL2 can race
	// freshly-bound host port forwarding; same flake class as helm test).
	script := fmt.Sprintf(`set -e
for i in 1 2 3 4 5 6 7 8 9 10; do
  if wget -q -O /dev/null --timeout=2 http://host.docker.internal:%d/healthz; then break; fi
  sleep 0.2
done
pip install --no-cache-dir --index-url %s --trusted-host host.docker.internal %s
python -c "import importlib.metadata; print(importlib.metadata.version('%s'))"
`, fx.port, indexURL, pkgName, pkgName)
	out, err := dockerRun(t, image, script)
	if err != nil {
		t.Fatalf("pip install via DinD failed: %v\n--- output ---\n%s", err, out)
	}
	if !strings.Contains(out, version) {
		t.Fatalf("expected installed version %s in pip output:\n%s", version, out)
	}
}

// TestUVInstallFromOmniRepo proves `uv pip install --index-url` succeeds.
// uv is installed on top of the python:3.12-alpine image via pip.
func TestUVInstallFromOmniRepo(t *testing.T) {
	fx := bootAppWithRepo(t, "pypi")
	uploadFixtureWheel(t, fx)
	waitForSimpleIndex(t, simpleProjectURL(fx, pkgName), 10*time.Second)

	image := resolveImage(t)
	indexURL := fmt.Sprintf("http://host.docker.internal:%d/%s/pypi/%s/simple/", fx.port, fx.project, fx.repo)
	// Warm vpnkit before uv runs (see TestPipInstallFromOmniRepo for rationale).
	script := fmt.Sprintf(`set -e
for i in 1 2 3 4 5 6 7 8 9 10; do
  if wget -q -O /dev/null --timeout=2 http://host.docker.internal:%d/healthz; then break; fi
  sleep 0.2
done
pip install --no-cache-dir uv >/dev/null
uv pip install --system --no-cache --index-url %s --index-strategy unsafe-best-match %s
python -c "import importlib.metadata; print(importlib.metadata.version('%s'))"
`, fx.port, indexURL, pkgName, pkgName)
	out, err := dockerRun(t, image, script)
	if err != nil {
		t.Fatalf("uv pip install via DinD failed: %v\n--- output ---\n%s", err, out)
	}
	if !strings.Contains(out, version) {
		t.Fatalf("expected installed version %s in uv output:\n%s", version, out)
	}
}

// TestPyPIContentNegotiationJSON proves the simple index honors PEP 691
// JSON content negotiation, returning application/vnd.pypi.simple.v1+json
// to clients that ask for it.
func TestPyPIContentNegotiationJSON(t *testing.T) {
	fx := bootAppWithRepo(t, "pypi")
	uploadFixtureWheel(t, fx)
	waitForSimpleIndex(t, simpleProjectURL(fx, pkgName), 10*time.Second)

	url := fmt.Sprintf("http://%s/%s/pypi/%s/simple/", fx.host, fx.project, fx.repo)
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Accept", "application/vnd.pypi.simple.v1+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "application/vnd.pypi.simple.v1+json") {
		t.Fatalf("Content-Type=%q (expected pypi.simple.v1+json)", ct)
	}
}
