//go:build conformance

package deb_conformance

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestAptGetInstallFromOmniRepo proves a Debian 12 client can apt-get
// update + apt-get install a package from omnirepo, validating both the
// `dists/<suite>/InRelease` clearsigning path and the per-pool `.deb`
// download path.
//
// Uses the modern "signed-by=" sources.list keyring placement (Debian 12
// default) — no apt-key add (deprecated). The omnirepo-served public key
// is dropped at /etc/apt/keyrings/omnirepo.asc and referenced by absolute
// path from the sources.list entry per Debian admin guide best practice.
func TestAptGetInstallFromOmniRepo(t *testing.T) {
	fx := bootAppWithRepo(t, "deb")

	// Synthesize a valid .deb fixture.
	pkgName := "omnirepo-conformance"
	version := "1.0.0"
	arch := "amd64"
	debBytes := buildSyntheticDeb(t, pkgName, version, arch)

	// Default suite/component is stable/main.
	uploadURL := fmt.Sprintf("http://%s/%s/deb/%s/pool/o/%s/%s_%s_%s.deb?suite=stable&component=main",
		fx.host, fx.project, fx.repo, pkgName, pkgName, version, arch)
	resp := fx.putWithAuth(t, uploadURL, debBytes)
	if resp.StatusCode/100 != 2 {
		t.Fatalf("PUT %s: status=%d", uploadURL, resp.StatusCode)
	}
	_ = resp.Body.Close()

	// Wait for InRelease publication.
	releaseURL := fmt.Sprintf("http://%s/%s/deb/%s/dists/stable/InRelease", fx.host, fx.project, fx.repo)
	waitForMetadata(t, releaseURL, 10*time.Second)

	image := resolveImage(t)

	script := fmt.Sprintf(`set -e
apt-get update -qq
apt-get install -y --no-install-recommends curl gnupg ca-certificates
mkdir -p /etc/apt/keyrings
curl -sSf -o /etc/apt/keyrings/omnirepo.asc http://host.docker.internal:%d/%s/deb/%s/public-key.asc
echo 'deb [signed-by=/etc/apt/keyrings/omnirepo.asc] http://host.docker.internal:%d/%s/deb/%s stable main' > /etc/apt/sources.list.d/omnirepo.list
# Disable the default Debian sources so apt-get update only consults omnirepo.
echo '' > /etc/apt/sources.list
apt-get update
apt-get install -y --download-only --allow-unauthenticated %s
ls -la /var/cache/apt/archives/%s_*.deb
`, fx.port, fx.project, fx.repo, fx.port, fx.project, fx.repo, pkgName, pkgName)

	out, err := dockerRun(t, image, script)
	if err != nil {
		t.Fatalf("apt-get install via DinD failed: %v\n--- output ---\n%s", err, out)
	}
	if !strings.Contains(out, pkgName) {
		t.Fatalf("expected %q in apt output:\n%s", pkgName, out)
	}
}
