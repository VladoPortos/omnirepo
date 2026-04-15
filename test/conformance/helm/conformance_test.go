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

	script := fmt.Sprintf(`set -e
export HOME=/tmp
helm repo add omnirepo %s --username %s --password %s
helm repo update
helm pull omnirepo/%s --version %s --destination /tmp
ls -la /tmp/%s-%s.tgz
helm install --dry-run testrelease omnirepo/%s --version %s
`, repoBaseURL, fx.adminLogin, fx.adminPassword,
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
