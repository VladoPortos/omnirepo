// Package scan defines the Runner interface for invoking Trivy and parsing its
// output. Production callers use NewTrivyRunner; tests use NewFakeRunner.
//
// Air-gap invariant: every Runner implementation MUST invoke Trivy with
// --offline-scan --skip-db-update; a non-conforming impl is a defect (D-22,
// threat T-02-03-02).
package scan

import "context"

// SBOMFormat identifies an SBOM format that Trivy can emit.
type SBOMFormat string

// SBOMFormat constants. Values match the exact string Trivy's --format flag
// expects so callers can pass them through unchanged.
const (
	FormatCycloneDX SBOMFormat = "cyclonedx"
	FormatSPDX      SBOMFormat = "spdx-json"
)

// Runner is the stable façade over Trivy (D-21). All methods are safe to call
// concurrently; implementations serialize or parallelize internally.
type Runner interface {
	// Image scans an OCI layout directory (manifest as index.json + blobs).
	Image(ctx context.Context, ociLayoutDir string) (Result, error)

	// Filesystem scans an extracted directory tree (used for RAW + Phase 3
	// protocols that materialize package contents to disk).
	Filesystem(ctx context.Context, dir string) (Result, error)

	// SBOM writes a CycloneDX or SPDX-JSON SBOM describing dir to outPath.
	// Callers MUST validate outPath is inside the sboms data root (T-02-03-05).
	SBOM(ctx context.Context, dir string, format SBOMFormat, outPath string) error
}

// Result is the parsed Trivy output that flows into scans + vulnerabilities
// rows (Phase 02-09). Summary is always initialized with all 5 severity keys
// present (value 0 when absent) so downstream code never hits map-miss edge
// cases.
type Result struct {
	Summary         map[string]int
	Vulnerabilities []Vuln
	SchemaVersion   int
	TrivyDBVersion  string
	ArtifactName    string
}

// Vuln is a single vulnerability finding parsed from Trivy JSON.
type Vuln struct {
	CVEID            string
	Package          string
	InstalledVersion string
	FixedVersion     string
	Severity         string
	Title            string
	Description      string
}
