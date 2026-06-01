package scan

import (
	"encoding/json"
	"strings"
)

// Skip reasons returned by IsScannableManifest for non-scannable manifests.
// Exposed as constants so callers (handler, enqueue side, tests) agree on
// the string shape that lands in audit details + summary payloads.
const (
	SkipReasonAttestation = "attestation_manifest"
	SkipReasonIndex       = "image_index"
	SkipReasonNoLayers    = "no_layers"
	SkipReasonUnparseable = "manifest_unparseable"
)

// IsScannableManifest reports whether a Docker/OCI manifest body contains
// anything Trivy can actually scan. Non-scannable bodies are handed back
// with a short reason string for logging and audit.
//
// Non-scannable shapes (P-1 root cause is the first two):
//
//  1. Buildx attestation manifests — a single "layer" that is a JSON
//     attestation (in-toto / DSSE). Trivy treats every layer as a rootfs
//     tar, so pointing it at a JSON document triggers
//     "archive/tar: invalid tar header". These manifests are also tagged
//     by buildx with annotation vnd.docker.reference.type=attestation-manifest.
//  2. Image indexes / manifest lists — the top-level descriptor of a
//     multi-arch push. No rootfs of its own; the per-platform child
//     manifests are enqueued separately and scanned individually.
//  3. Manifests with no layers — nothing for Trivy to analyze.
//  4. Bodies that fail to parse as JSON — treated as non-scannable so the
//     caller MarkDone's with a clear skip reason instead of 500-looping.
func IsScannableManifest(body []byte) (scannable bool, reason string) {
	var raw struct {
		Annotations map[string]string `json:"annotations"`
		Manifests   []json.RawMessage `json:"manifests"`
		Layers      []struct {
			MediaType string `json:"mediaType"`
		} `json:"layers"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return false, SkipReasonUnparseable
	}
	// Buildx attestation annotation trumps everything else.
	if raw.Annotations["vnd.docker.reference.type"] == "attestation-manifest" {
		return false, SkipReasonAttestation
	}
	// Image index / manifest list → no layers of its own; children are
	// scanned individually when they're pushed.
	if len(raw.Manifests) > 0 {
		return false, SkipReasonIndex
	}
	if len(raw.Layers) == 0 {
		return false, SkipReasonNoLayers
	}
	// If EVERY layer is a non-filesystem attestation type, Trivy has
	// nothing to untar. Treat the whole manifest as an attestation.
	for _, l := range raw.Layers {
		if !isAttestationLayerMediaType(l.MediaType) {
			return true, ""
		}
	}
	return false, SkipReasonAttestation
}

// isAttestationLayerMediaType matches the two in-use attestation layer
// shapes: in-toto statements (SLSA provenance, SPDX SBOM, etc.) and DSSE
// envelopes. Matched case-insensitively on substring so future subtypes
// ("application/vnd.in-toto.v1+json" etc.) still land in the skip path.
func isAttestationLayerMediaType(mt string) bool {
	lower := strings.ToLower(mt)
	return strings.Contains(lower, "in-toto") ||
		strings.Contains(lower, "dsse.envelope") ||
		strings.Contains(lower, "vnd.dsse")
}
