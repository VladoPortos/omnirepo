package scan_test

import (
	"testing"

	"github.com/dxc-internal/omnirepo/internal/scan"
)

// IsScannableManifest classifies a Docker/OCI manifest body for scan
// suitability. It MUST:
//   - return true for a normal image manifest with a rootfs layer;
//   - return false with a reason for buildx attestation manifests
//     (layer mediaType == application/vnd.in-toto+json);
//   - return false with a reason for attestation-annotated manifests
//     (vnd.docker.reference.type = attestation-manifest);
//   - return false with a reason for image indexes (manifest lists);
//   - return false with a reason when there are no layers;
//   - return false when the body fails to parse.
//
// P-1: without this classifier, the scan handler hands an attestation
// layer to Trivy which tries to tar-extract a JSON document and dies
// with "archive/tar: invalid tar header".

func TestIsScannableManifest_ImageManifestIsScannable(t *testing.T) {
	body := []byte(`{
		"schemaVersion": 2,
		"mediaType": "application/vnd.oci.image.manifest.v1+json",
		"config": {"digest": "sha256:aaaa", "size": 10},
		"layers": [
			{"mediaType":"application/vnd.oci.image.layer.v1.tar+gzip","digest":"sha256:bbbb","size":100}
		]
	}`)
	ok, reason := scan.IsScannableManifest(body)
	if !ok {
		t.Fatalf("want scannable=true for rootfs layer manifest, got false (reason=%q)", reason)
	}
}

func TestIsScannableManifest_AttestationByLayerMediaType(t *testing.T) {
	body := []byte(`{
		"schemaVersion": 2,
		"mediaType": "application/vnd.oci.image.manifest.v1+json",
		"config": {"digest": "sha256:aa"},
		"layers": [
			{"mediaType":"application/vnd.in-toto+json","digest":"sha256:bb","size":5000}
		]
	}`)
	ok, reason := scan.IsScannableManifest(body)
	if ok {
		t.Fatalf("attestation manifest (in-toto layer) must not be scannable")
	}
	if reason == "" {
		t.Fatalf("reason must be non-empty")
	}
}

func TestIsScannableManifest_AttestationByAnnotation(t *testing.T) {
	body := []byte(`{
		"schemaVersion": 2,
		"mediaType": "application/vnd.oci.image.manifest.v1+json",
		"config": {"digest":"sha256:aa"},
		"layers": [
			{"mediaType":"application/vnd.oci.image.layer.v1.tar+gzip","digest":"sha256:bb","size":100}
		],
		"annotations": {"vnd.docker.reference.type":"attestation-manifest"}
	}`)
	ok, reason := scan.IsScannableManifest(body)
	if ok {
		t.Fatalf("manifest annotated as attestation-manifest must not be scannable")
	}
	if reason == "" {
		t.Fatalf("reason must be non-empty")
	}
}

func TestIsScannableManifest_IndexIsNot(t *testing.T) {
	body := []byte(`{
		"schemaVersion": 2,
		"mediaType": "application/vnd.oci.image.index.v1+json",
		"manifests": [
			{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"sha256:aa","size":1}
		]
	}`)
	ok, reason := scan.IsScannableManifest(body)
	if ok {
		t.Fatalf("image index must not be scannable (callers scan the child manifests)")
	}
	if reason == "" {
		t.Fatalf("reason must be non-empty")
	}
}

func TestIsScannableManifest_EmptyLayersIsNot(t *testing.T) {
	body := []byte(`{
		"schemaVersion": 2,
		"mediaType": "application/vnd.oci.image.manifest.v1+json",
		"config": {"digest":"sha256:aa"},
		"layers": []
	}`)
	ok, reason := scan.IsScannableManifest(body)
	if ok {
		t.Fatalf("manifest with no layers has nothing for Trivy to scan")
	}
	if reason == "" {
		t.Fatalf("reason must be non-empty")
	}
}

func TestIsScannableManifest_MalformedBodyIsNot(t *testing.T) {
	ok, reason := scan.IsScannableManifest([]byte("not-json"))
	if ok {
		t.Fatalf("malformed body must not be scannable")
	}
	if reason == "" {
		t.Fatalf("reason must be non-empty")
	}
}

// DSSE envelopes are another non-scannable attestation shape; pin them
// to the same skip path so future attestation formats don't slip past.
func TestIsScannableManifest_DSSELayerIsNot(t *testing.T) {
	body := []byte(`{
		"schemaVersion": 2,
		"mediaType": "application/vnd.oci.image.manifest.v1+json",
		"config": {"digest":"sha256:aa"},
		"layers": [
			{"mediaType":"application/vnd.dsse.envelope.v1+json","digest":"sha256:bb","size":1}
		]
	}`)
	ok, reason := scan.IsScannableManifest(body)
	if ok {
		t.Fatalf("DSSE envelope layer must not be scannable")
	}
	if reason == "" {
		t.Fatalf("reason must be non-empty")
	}
}
