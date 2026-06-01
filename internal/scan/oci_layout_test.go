package scan_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vladoportos/omnirepo/internal/scan"
)

// fakeBlob returns canned bytes for digests in the map.
func fakeLookup(blobs map[string][]byte) scan.BlobLookup {
	return func(ctx context.Context, d string) (io.ReadCloser, error) {
		b, ok := blobs[d]
		if !ok {
			return nil, errors.New("blob not found: " + d)
		}
		return io.NopCloser(strings.NewReader(string(b))), nil
	}
}

func TestMaterializeOCILayout_WritesIndexAndBlobs(t *testing.T) {
	dst := t.TempDir()
	mfBody := []byte(`{"schemaVersion":2,"config":{"digest":"sha256:aaaa"},"layers":[{"digest":"sha256:bbbb"}]}`)
	blobs := map[string][]byte{
		"sha256:aaaa": []byte("config bytes"),
		"sha256:bbbb": []byte("layer bytes"),
	}
	if err := scan.MaterializeOCILayout(context.Background(), dst, mfBody,
		"application/vnd.oci.image.manifest.v1+json",
		[]string{"sha256:aaaa", "sha256:bbbb"},
		fakeLookup(blobs),
	); err != nil {
		t.Fatalf("Materialize: %v", err)
	}

	// oci-layout marker
	if b, err := os.ReadFile(filepath.Join(dst, "oci-layout")); err != nil {
		t.Fatalf("read oci-layout: %v", err)
	} else if !strings.Contains(string(b), "imageLayoutVersion") {
		t.Fatalf("oci-layout missing version: %s", b)
	}

	// index.json
	idxBytes, err := os.ReadFile(filepath.Join(dst, "index.json"))
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	var idx map[string]any
	if err := json.Unmarshal(idxBytes, &idx); err != nil {
		t.Fatalf("parse index: %v", err)
	}
	mfs, ok := idx["manifests"].([]any)
	if !ok || len(mfs) != 1 {
		t.Fatalf("index manifests = %v", idx["manifests"])
	}

	// blobs/sha256/aaaa
	if b, err := os.ReadFile(filepath.Join(dst, "blobs", "sha256", "aaaa")); err != nil || string(b) != "config bytes" {
		t.Fatalf("blob aaaa = %q err=%v", b, err)
	}
	if b, err := os.ReadFile(filepath.Join(dst, "blobs", "sha256", "bbbb")); err != nil || string(b) != "layer bytes" {
		t.Fatalf("blob bbbb = %q err=%v", b, err)
	}

	// Manifest body itself stored under blobs/sha256/<sha256(body)>
	matches, _ := filepath.Glob(filepath.Join(dst, "blobs", "sha256", "*"))
	if len(matches) < 3 {
		t.Fatalf("expected ≥3 blob files, got %d", len(matches))
	}
}

func TestMaterializeOCILayout_LookupErrorPropagates(t *testing.T) {
	dst := t.TempDir()
	mfBody := []byte(`{"config":{"digest":"sha256:dead"}}`)
	err := scan.MaterializeOCILayout(context.Background(), dst, mfBody,
		"application/vnd.oci.image.manifest.v1+json",
		[]string{"sha256:dead"},
		fakeLookup(nil), // empty -> not found
	)
	if err == nil || !strings.Contains(err.Error(), "blob not found") {
		t.Fatalf("expected blob-not-found error, got %v", err)
	}
}
