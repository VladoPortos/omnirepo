package scan_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/vladoportos/omnirepo/internal/config"
	"github.com/vladoportos/omnirepo/internal/scan"
)

// minimalFixture is a synthetic Trivy-shaped JSON used only by the exec
// tests. The full snapshot fixtures live under testdata/trivy/ and are
// exercised by parse_test.go.
const minimalFixture = `{
  "SchemaVersion": 2,
  "ArtifactName": "test-image",
  "ArtifactType": "container_image",
  "Metadata": {"OS": {"Family": "alpine", "Name": "3.19"}},
  "Results": [
    {"Target":"test","Class":"os-pkgs","Type":"alpine","Vulnerabilities":[
      {"VulnerabilityID":"CVE-9999-0001","PkgName":"busybox","InstalledVersion":"1.0","FixedVersion":"1.1","Severity":"HIGH","Title":"t","Description":"d"}
    ]}
  ]
}`

// writeMockTrivy installs a shell script at dir/trivy that records its
// argv to dir/argv.log and echoes fixture to stdout with exit 0.
func writeMockTrivy(t *testing.T, dir, fixture string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("mock shell script requires POSIX sh")
	}
	script := filepath.Join(dir, "trivy")
	logPath := filepath.Join(dir, "argv.log")
	// heredoc cat keeps JSON intact regardless of escaping.
	code := "#!/bin/sh\n" +
		"printf '%s\\n' \"$@\" > " + logPath + "\n" +
		"cat <<'TRIVY_JSON_EOF'\n" +
		fixture + "\n" +
		"TRIVY_JSON_EOF\n"
	if err := os.WriteFile(script, []byte(code), 0o755); err != nil {
		t.Fatal(err)
	}
	return script
}

func TestTrivyRunnerImageInvokesRequiredFlags(t *testing.T) {
	dir := t.TempDir()
	script := writeMockTrivy(t, dir, minimalFixture)
	ociDir := t.TempDir()

	runner := scan.NewTrivyRunner(config.Trivy{
		BinaryPath: script,
		DBPath:     "/db",
		CachePath:  "/cache",
	})
	res, err := runner.Image(context.Background(), ociDir)
	if err != nil {
		t.Fatalf("Image: %v", err)
	}

	argvBytes, readErr := os.ReadFile(filepath.Join(dir, "argv.log"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	argv := string(argvBytes)

	// Every token REQUIRED by the air-gap invariant + the image subcommand path.
	required := []string{
		"image",
		"--input",
		ociDir,
		"--cache-dir",
		"/cache",
		"--db-repository",
		"file:///db",
		"--offline-scan",
		"--skip-db-update",
		"--skip-check-update",
		"--format",
		"json",
	}
	for _, tok := range required {
		if !strings.Contains(argv, tok) {
			t.Errorf("argv missing required token %q; got:\n%s", tok, argv)
		}
	}
	// --insecure must never leak into argv.
	if strings.Contains(argv, "--insecure") {
		t.Errorf("argv contains forbidden --insecure flag: %s", argv)
	}
	if res.SchemaVersion != 2 {
		t.Errorf("SchemaVersion = %d, want 2", res.SchemaVersion)
	}
	if res.Summary["high"] != 1 {
		t.Errorf("Summary[high] = %d, want 1", res.Summary["high"])
	}
}

func TestTrivyRunnerFilesystemInvokesFsSubcommand(t *testing.T) {
	dir := t.TempDir()
	script := writeMockTrivy(t, dir, minimalFixture)
	fsDir := t.TempDir()

	runner := scan.NewTrivyRunner(config.Trivy{
		BinaryPath: script,
		DBPath:     "/db",
		CachePath:  "/cache",
	})
	if _, err := runner.Filesystem(context.Background(), fsDir); err != nil {
		t.Fatalf("Filesystem: %v", err)
	}
	argv, _ := os.ReadFile(filepath.Join(dir, "argv.log"))
	argvS := string(argv)
	if !strings.Contains(argvS, "fs") {
		t.Errorf("argv missing fs subcommand: %s", argvS)
	}
	for _, tok := range []string{"--offline-scan", "--skip-db-update", "--skip-check-update", "--cache-dir", "/cache"} {
		if !strings.Contains(argvS, tok) {
			t.Errorf("argv missing %q: %s", tok, argvS)
		}
	}
}

func TestTrivyRunnerSBOMInvokesRequiredFlags(t *testing.T) {
	dir := t.TempDir()
	script := writeMockTrivy(t, dir, "")
	// SBOM doesn't parse stdout; the script just needs to succeed.
	fsDir := t.TempDir()
	out := filepath.Join(t.TempDir(), "sbom.json")

	runner := scan.NewTrivyRunner(config.Trivy{
		BinaryPath: script,
		DBPath:     "/db",
		CachePath:  "/cache",
	})
	if err := runner.SBOM(context.Background(), fsDir, scan.FormatCycloneDX, out); err != nil {
		t.Fatalf("SBOM: %v", err)
	}
	argv, _ := os.ReadFile(filepath.Join(dir, "argv.log"))
	argvS := string(argv)
	required := []string{
		"image", "--input", fsDir,
		"--offline-scan", "--skip-db-update", "--skip-check-update",
		"--format", "cyclonedx",
		"--output", out,
	}
	for _, tok := range required {
		if !strings.Contains(argvS, tok) {
			t.Errorf("SBOM argv missing %q: %s", tok, argvS)
		}
	}
}

func TestTrivyRunnerExecFailurePropagatesStderr(t *testing.T) {
	dir := t.TempDir()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX sh only")
	}
	script := filepath.Join(dir, "trivy")
	code := "#!/bin/sh\necho 'synthetic trivy failure' 1>&2\nexit 3\n"
	if err := os.WriteFile(script, []byte(code), 0o755); err != nil {
		t.Fatal(err)
	}
	runner := scan.NewTrivyRunner(config.Trivy{
		BinaryPath: script,
		DBPath:     "/db",
		CachePath:  "/cache",
	})
	_, err := runner.Image(context.Background(), t.TempDir())
	if err == nil {
		t.Fatal("Image: want error on non-zero exit")
	}
	if !strings.Contains(err.Error(), "synthetic trivy failure") {
		t.Errorf("error missing stderr content: %v", err)
	}
}
