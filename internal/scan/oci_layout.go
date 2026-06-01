// Package scan — OCI layout materialization for Trivy `image --input`.
//
// Trivy's image scanner can read a directory laid out per the OCI Image
// Layout spec instead of pulling from a registry. We materialize the
// manifest body + every referenced blob into <dstDir>/, then call
// runner.Image(ctx, dstDir).
//
// Layout produced:
//
//	dstDir/
//	  oci-layout            -> {"imageLayoutVersion":"1.0.0"}
//	  index.json            -> top-level index with one descriptor
//	  blobs/
//	    sha256/
//	      <hex>             -> manifest body (and each referenced blob)
//
// The manifest itself is also written under blobs/sha256/<hex> so that the
// index.json descriptor resolves the same way Trivy expects.
package scan

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// BlobLookup fetches the bytes for a digest from the CAS. Returns
// io.ErrUnexpectedEOF (or os.ErrNotExist) on a missing blob.
type BlobLookup func(ctx context.Context, digest string) (io.ReadCloser, error)

// MaterializeOCILayout writes an OCI image layout under dstDir containing
// the manifest body + every blob digest in refs.
//
// manifestMediaType is the manifest's content-type (e.g.
// application/vnd.oci.image.manifest.v1+json). It goes into the index.json
// descriptor's mediaType field.
//
// On any IO error the caller is responsible for cleaning up dstDir; the
// scan handler defers os.RemoveAll for both success and failure paths.
func MaterializeOCILayout(
	ctx context.Context,
	dstDir string,
	manifestBody []byte,
	manifestMediaType string,
	refs []string,
	blobLookup BlobLookup,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	blobsDir := filepath.Join(dstDir, "blobs", "sha256")
	if err := os.MkdirAll(blobsDir, 0o750); err != nil {
		return fmt.Errorf("oci_layout: mkdir blobs: %w", err)
	}

	// 1. oci-layout marker file.
	if err := os.WriteFile(
		filepath.Join(dstDir, "oci-layout"),
		[]byte(`{"imageLayoutVersion":"1.0.0"}`),
		0o640,
	); err != nil {
		return fmt.Errorf("oci_layout: write oci-layout: %w", err)
	}

	// 2. Write manifest body to blobs/sha256/<hex>.
	mfDigest := digestOf(manifestBody)
	mfHex := strings.TrimPrefix(mfDigest, "sha256:")
	if err := os.WriteFile(filepath.Join(blobsDir, mfHex), manifestBody, 0o640); err != nil {
		return fmt.Errorf("oci_layout: write manifest body: %w", err)
	}

	// 3. index.json with one descriptor pointing at the manifest digest.
	idx := map[string]any{
		"schemaVersion": 2,
		"mediaType":     "application/vnd.oci.image.index.v1+json",
		"manifests": []map[string]any{
			{
				"mediaType": manifestMediaType,
				"digest":    mfDigest,
				"size":      len(manifestBody),
			},
		},
	}
	idxBytes, err := json.Marshal(idx)
	if err != nil {
		return fmt.Errorf("oci_layout: encode index.json: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dstDir, "index.json"), idxBytes, 0o640); err != nil {
		return fmt.Errorf("oci_layout: write index.json: %w", err)
	}

	// 4. Copy every referenced blob (config + layers) into blobs/sha256/.
	for _, d := range refs {
		if err := ctx.Err(); err != nil {
			return err
		}
		hx := strings.TrimPrefix(d, "sha256:")
		dst := filepath.Join(blobsDir, hx)
		// Skip if already on disk (manifest body case is handled above).
		if _, err := os.Stat(dst); err == nil {
			continue
		}
		rc, err := blobLookup(ctx, d)
		if err != nil {
			return fmt.Errorf("oci_layout: lookup %s: %w", d, err)
		}
		if err := writeBlob(dst, rc); err != nil {
			return fmt.Errorf("oci_layout: write blob %s: %w", d, err)
		}
	}
	return nil
}

// writeBlob streams rc to dst with O_CREATE|O_WRONLY|O_TRUNC, ensuring
// the reader is closed even on error.
func writeBlob(dst string, rc io.ReadCloser) error {
	defer func() { _ = rc.Close() }()
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, rc); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// digestOf returns "sha256:<hex>" of body.
func digestOf(body []byte) string {
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:])
}
