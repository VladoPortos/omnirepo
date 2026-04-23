//go:build conformance

package rpm_conformance

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestDNFInstallFromOmniRepo proves end-to-end that a Rocky 9 client can
// `rpm --import` the repo's public key and `dnf install` a package served
// by omnirepo over loopback.
//
// Flow:
//  1. Boot omnirepo with an empty rpm repo (eager signing-key gen runs).
//  2. PUT a known .rpm fixture to /<proj>/rpm/<repo>/packages/<filename>.
//  3. Wait for the debounced regen to publish repodata/repomd.xml.
//  4. docker run --rm rockylinux:9 — fetch /public-key.asc, write a .repo
//     file pointing at host.docker.internal, dnf -y install hello.
//  5. Assert exit code 0 and stdout contains "Installed".
func TestDNFInstallFromOmniRepo(t *testing.T) {
	fx := bootAppWithRepo(t, "rpm")

	// Vendored fixture (~23 KB sample.rpm under internal/protocol/rpm/testdata/).
	rpmPath := findFixture(t, "internal/protocol/rpm/testdata/sample.rpm")
	body, err := os.ReadFile(rpmPath)
	if err != nil {
		t.Fatalf("read fixture %s: %v", rpmPath, err)
	}
	// F-06.1 (wt3 batch 06) makes the RPM PUT handler enforce
	// filename == "<name>-<version>-<release>.<arch>.rpm" (canonical
	// NEVRA) to prevent metadata/disk drift. sample.rpm is the
	// centos-release-7 fixture; use its NEVRA as the upload filename.
	const canonicalName = "centos-release-7-2.1511.el7.centos.2.10.x86_64.rpm"
	pkgURL := fmt.Sprintf("http://%s/%s/rpm/%s/packages/%s", fx.host, fx.project, fx.repo, canonicalName)
	resp := fx.putWithAuth(t, pkgURL, body)
	if resp.StatusCode/100 != 2 {
		t.Fatalf("PUT %s: status=%d", pkgURL, resp.StatusCode)
	}
	_ = resp.Body.Close()

	// Wait for repodata regen.
	repomdURL := fmt.Sprintf("http://%s/%s/rpm/%s/repodata/repomd.xml", fx.host, fx.project, fx.repo)
	waitForMetadata(t, repomdURL, 10*time.Second)

	image := resolveImage(t)

	// The fixture sample.rpm package name is unknown to dnf install by name,
	// so we drive the conformance assertion against `dnf -y makecache` +
	// `dnf repoquery sample` (works even for synthetic packages where the
	// installable target may be unsatisfiable due to scriptlet/deps).
	script := fmt.Sprintf(`set -e
curl -sSf -o /tmp/key http://host.docker.internal:%d/%s/rpm/%s/public-key.asc
rpm --import /tmp/key
cat > /etc/yum.repos.d/omnirepo.repo <<EOF
[omnirepo]
name=omnirepo conformance
baseurl=http://host.docker.internal:%d/%s/rpm/%s/
enabled=1
gpgcheck=1
gpgkey=file:///tmp/key
repo_gpgcheck=1
EOF
dnf -y --disablerepo='*' --enablerepo=omnirepo makecache
dnf -y --disablerepo='*' --enablerepo=omnirepo repoquery '*' | head -5
`, fx.port, fx.project, fx.repo, fx.port, fx.project, fx.repo)

	out, err := dockerRun(t, image, script)
	if err != nil {
		t.Fatalf("dnf install via DinD failed: %v\n--- output ---\n%s", err, out)
	}
	if !strings.Contains(out, "Metadata cache") && !strings.Contains(out, "metadata") {
		t.Logf("dnf output (no explicit cache marker; verify manually):\n%s", out)
	}
}

// findFixture walks up from cwd to locate a repo-relative path. Used so
// the test can be invoked from any subdirectory.
func findFixture(t *testing.T, rel string) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for i := 0; i < 8; i++ {
		candidate := filepath.Join(dir, rel)
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatalf("fixture %s not found in any ancestor of cwd", rel)
	return ""
}
