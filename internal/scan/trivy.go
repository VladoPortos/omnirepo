package scan

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/dxc-internal/omnirepo/internal/config"
)

// trivyRunner is the production exec-based Runner. Every invocation uses
// exec.CommandContext with an argv slice (no shell interpolation,
// T-02-03-01) and includes the four air-gap flags (T-02-03-02).
type trivyRunner struct {
	binary    string
	dbPath    string
	cachePath string
}

// NewTrivyRunner constructs a Runner that invokes the trivy binary at
// cfg.BinaryPath. The returned Runner is safe for concurrent use; each call
// spawns its own subprocess.
func NewTrivyRunner(cfg config.Trivy) Runner {
	return &trivyRunner{
		binary:    cfg.BinaryPath,
		dbPath:    cfg.DBPath,
		cachePath: cfg.CachePath,
	}
}

// baseFlags returns the D-22 mandatory flag set. Centralized so the
// air-gap invariant is impossible to drop accidentally — grep tests assert
// these appear in EVERY exec call path.
//
// Scanners: vuln + secret + misconfig. Misconfig was added for F-4 so
// Helm chart scans surface Kubernetes misconfigurations (e.g., containers
// using images from untrusted registries). Vuln stays for OS-package +
// language-lockfile CVEs; secret stays for the existing RAW pathway.
func (t *trivyRunner) baseFlags() []string {
	return []string{
		"--cache-dir", t.cachePath,
		"--db-repository", "file://" + t.dbPath,
		"--offline-scan",
		"--skip-db-update",
		// Java DB is a separate OCI artifact Trivy auto-fetches on first
		// Java analyzer hit. The air-gap invariant requires we never
		// touch the network; --skip-java-db-update is the sibling of
		// --skip-db-update for that database.
		"--skip-java-db-update",
		// VEX repo is similarly fetched on first hit; same rule applies.
		"--skip-vex-repo-update",
		"--scanners", "vuln,secret,misconfig",
		"--format", "json",
	}
}

func (t *trivyRunner) runJSON(ctx context.Context, args []string) (Result, error) {
	cmd := exec.CommandContext(ctx, t.binary, args...)
	cmd.Env = append(os.Environ(), "TRIVY_NO_PROGRESS=1")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return Result{}, fmt.Errorf("trivy exec failed: %w (stderr=%s)", err, stderr.String())
	}
	return ParseTrivyJSON(stdout.Bytes())
}

func (t *trivyRunner) Image(ctx context.Context, ociLayoutDir string) (Result, error) {
	args := append([]string{"image", "--input", ociLayoutDir}, t.baseFlags()...)
	return t.runJSON(ctx, args)
}

func (t *trivyRunner) Filesystem(ctx context.Context, dir string) (Result, error) {
	args := append([]string{"fs", dir}, t.baseFlags()...)
	return t.runJSON(ctx, args)
}

func (t *trivyRunner) SBOM(ctx context.Context, dir string, format SBOMFormat, outPath string) error {
	// SBOM path re-spells the flags explicitly rather than reusing baseFlags
	// because --format varies (cyclonedx | spdx-json) and we add --output.
	// The grep gate asserts --offline-scan / --skip-db-update appear ≥ 2x
	// across this file (here + baseFlags).
	args := []string{
		"image", "--input", dir,
		"--cache-dir", t.cachePath,
		"--db-repository", "file://" + t.dbPath,
		"--offline-scan",
		"--skip-db-update",
		"--skip-java-db-update",
		"--skip-vex-repo-update",
		"--format", string(format),
		"--output", outPath,
	}
	cmd := exec.CommandContext(ctx, t.binary, args...)
	cmd.Env = append(os.Environ(), "TRIVY_NO_PROGRESS=1")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("trivy sbom failed: %w (stderr=%s)", err, stderr.String())
	}
	return nil
}
