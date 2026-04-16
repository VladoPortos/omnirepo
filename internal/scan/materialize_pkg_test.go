package scan

// Unit-test-only: exercises the extractRPM helper against a real fixture
// plus the PyPI Requires-Dist normalizer + METADATA → requirements.txt
// pipeline. All covered helpers are unexported, so the test must live
// inside the scan package.

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestExtractRPM_UnpacksPayload verifies that extractRPM walks past the
// RPM headers, decompresses the payload, and writes regular files into
// dstDir. S-1: prior behaviour was a no-op, which made Trivy RPM scans
// produce zero findings regardless of package contents.
func TestExtractRPM_UnpacksPayload(t *testing.T) {
	src := filepath.Join("testdata", "sample.rpm")
	dst := t.TempDir()

	if err := extractRPM(src, dst); err != nil {
		t.Fatalf("extractRPM: %v", err)
	}

	// The payload must have produced at least one regular file. The exact
	// layout depends on the fixture; walk the tree and count.
	var fileCount int
	err := filepath.Walk(dst, func(_ string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.Mode().IsRegular() {
			fileCount++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk dst: %v", err)
	}
	if fileCount == 0 {
		t.Fatalf("extractRPM wrote zero regular files into %s; expected payload files", dst)
	}
}

// TestExtractRPM_RejectsNonRPM verifies the caller-visible behaviour
// when handed a file that is not an RPM: return an error, do not corrupt
// dstDir. The outer materializePackage flow swallows this (raw file is
// still available to Trivy), but we lock the error path explicitly so a
// future refactor can't silently turn extractRPM into a no-op.
func TestExtractRPM_RejectsNonRPM(t *testing.T) {
	dst := t.TempDir()
	// Write a tiny file that is definitely not an RPM.
	fake := filepath.Join(t.TempDir(), "not.rpm")
	if err := os.WriteFile(fake, []byte("this is not an rpm file"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := extractRPM(fake, dst); err == nil {
		t.Fatal("extractRPM should have errored on a non-RPM payload")
	}
	entries, _ := os.ReadDir(dst)
	if len(entries) != 0 {
		t.Fatalf("dstDir should be untouched on failure, got %d entries", len(entries))
	}
}

// TestNormalizeRequiresDist covers the PEP 508 → pip syntax rewrite used
// to turn METADATA `Requires-Dist:` values into requirements.txt lines.
// S-2: prior behaviour synthesized only the wheel's own name+version,
// so Trivy missed every CVE in transitive deps.
func TestNormalizeRequiresDist(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"requests (>=2.25.0)", "requests>=2.25.0"},
		{"urllib3<2", "urllib3<2"},
		{"certifi", "certifi"},
		{`idna (>=2.5,<4); python_version >= "3"`, "idna>=2.5,<4"},
		{"pytest[testing] (>=6)", "pytest>=6"},
		{"  charset-normalizer  >= 2 , < 4  ", "charset-normalizer>=2,<4"},
		{"", ""},
		{"; only-a-marker", ""},
	}
	for _, c := range cases {
		got := normalizeRequiresDist(c.in)
		if got != c.want {
			t.Errorf("normalizeRequiresDist(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestExtractWheel_WritesRequirementsWithTransitiveDeps builds a fake
// .whl containing a `*.dist-info/METADATA` with a handful of
// `Requires-Dist:` lines, runs extractWheel, and asserts the resulting
// requirements.txt contains BOTH the wheel itself (name==version) and
// each transitive dep as a normalized pip line. Locks the S-2 fix.
func TestExtractWheel_WritesRequirementsWithTransitiveDeps(t *testing.T) {
	wheelDir := t.TempDir()
	wheelPath := filepath.Join(wheelDir, "example-1.2.3-py3-none-any.whl")

	f, err := os.Create(wheelPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	meta := strings.Join([]string{
		"Metadata-Version: 2.1",
		"Name: example",
		"Version: 1.2.3",
		"Requires-Dist: requests (>=2.25.0)",
		"Requires-Dist: urllib3<2",
		`Requires-Dist: idna (>=2.5,<4); python_version >= "3"`,
		"",
		"README body goes here — no more Requires-Dist past the blank line.",
		"Requires-Dist: should-not-appear",
	}, "\n")
	w, err := zw.Create("example-1.2.3.dist-info/METADATA")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(meta)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	dst := t.TempDir()
	if err := extractWheel(wheelPath, dst, "example-1.2.3-py3-none-any.whl"); err != nil {
		t.Fatalf("extractWheel: %v", err)
	}

	body, err := os.ReadFile(filepath.Join(dst, "requirements.txt"))
	if err != nil {
		t.Fatalf("read requirements.txt: %v", err)
	}
	got := string(body)
	wantLines := []string{
		"example==1.2.3",
		"requests>=2.25.0",
		"urllib3<2",
		"idna>=2.5,<4",
	}
	for _, want := range wantLines {
		if !strings.Contains(got, want+"\n") {
			t.Errorf("requirements.txt missing line %q.\nGot:\n%s", want, got)
		}
	}
	if strings.Contains(got, "should-not-appear") {
		t.Errorf("requirements.txt leaked post-blank-line content:\n%s", got)
	}
}
