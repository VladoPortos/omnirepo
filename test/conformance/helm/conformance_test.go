//go:build conformance

package helm_conformance

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestHelmRepoAddPullInstallDryRun proves a real `helm` 3.20 client can
// add the omnirepo repo, pull a chart, and render `helm install --dry-run`
// against it. Validates index.yaml + chart .tgz serving end-to-end.
func TestHelmRepoAddPullInstallDryRun(t *testing.T) {
	fx := bootAppWithRepo(t, "helm")

	chartName := "omnirepo-conf"
	chartVersion := "0.1.0"
	chartBytes := makeChartTGZ(t, chartName, chartVersion, "1.0")

	uploadURL := fmt.Sprintf("http://%s/%s/helm/%s/charts/%s-%s.tgz",
		fx.host, fx.project, fx.repo, chartName, chartVersion)
	resp := fx.putWithAuth(t, uploadURL, chartBytes)
	if resp.StatusCode/100 != 2 {
		t.Fatalf("PUT %s: status=%d", uploadURL, resp.StatusCode)
	}
	_ = resp.Body.Close()

	indexURL := fmt.Sprintf("http://%s/%s/helm/%s/index.yaml", fx.host, fx.project, fx.repo)
	waitForIndexYaml(t, indexURL, 10*time.Second)

	image := resolveImage(t)
	repoBaseURL := fmt.Sprintf("http://host.docker.internal:%d/%s/helm/%s",
		fx.port, fx.project, fx.repo)

	// Warm up Docker Desktop's vpnkit port forwarder before invoking helm.
	// On WSL2 + Docker Desktop, a freshly-bound host port can take ~200-500ms
	// to register in vpnkit; helm starts faster than that and races the
	// registration with a `connection refused`. Looping wget against /healthz
	// blocks until vpnkit picks up the route. RPM tests don't need this
	// because `dnf makecache` is naturally slow enough.
	//
	// Per-attempt timeout is enforced via the `timeout` busybox applet
	// rather than `wget --timeout=...` because alpine/helm:3.20's wget
	// silently ignores --timeout (empirically: 3+ minutes per attempt
	// against an unreachable IP). `timeout 2` works portably across all
	// alpine-based conformance images.
	script := fmt.Sprintf(`set -e
export HOME=/tmp
for i in 1 2 3 4 5 6 7 8 9 10; do
  if timeout 2 wget -q -O /dev/null http://host.docker.internal:%d/healthz; then break; fi
  sleep 0.2
done
helm repo add omnirepo %s --username %s --password %s
helm repo update
helm pull omnirepo/%s --version %s --destination /tmp
ls -la /tmp/%s-%s.tgz
helm template testrelease omnirepo/%s --version %s
`, fx.port,
		repoBaseURL, fx.adminLogin, fx.adminPassword,
		chartName, chartVersion,
		chartName, chartVersion,
		chartName, chartVersion)

	out, err := dockerRun(t, image, script)
	if err != nil {
		t.Fatalf("helm pull/install via DinD failed: %v\n--- output ---\n%s", err, out)
	}
	if !strings.Contains(out, "ConfigMap") {
		t.Fatalf("expected rendered ConfigMap in helm dry-run output:\n%s", out)
	}
	if !strings.Contains(out, "testrelease") {
		t.Fatalf("expected release name in helm output:\n%s", out)
	}
}
