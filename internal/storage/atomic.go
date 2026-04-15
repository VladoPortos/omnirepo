// Package storage owns the shared write-path primitives for OmniRepo: the
// atomic temp+fsync+rename helper (used by every writer), content-addressed
// blob store (CAS), path-addressed file store (PathStore), soft-delete trash,
// and per-repo mutex map. Every primitive survives crashes without leaving
// partial files at their final paths (D-28..D-32).
package storage

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// WriteAndRename writes r to a uniquely named temp file inside tmpDir, fsyncs
// the file, renames it to dstPath, then fsyncs the parent dir of dstPath (so
// the rename survives a power loss on Linux ext4/xfs/btrfs). On any error the
// temp file is unlinked and the error is returned wrapped.
//
// Concurrent callers targeting the same dstPath are safe: last-rename wins.
// On *nix, os.Rename is atomic across a single filesystem.
func WriteAndRename(ctx context.Context, tmpDir, dstPath string, r io.Reader) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if err := os.MkdirAll(tmpDir, 0o750); err != nil {
		return 0, fmt.Errorf("storage: mkdir tmp: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(dstPath), 0o750); err != nil {
		return 0, fmt.Errorf("storage: mkdir dst parent: %w", err)
	}

	f, err := os.CreateTemp(tmpDir, ".omnirepo-*.tmp")
	if err != nil {
		return 0, fmt.Errorf("storage: create temp: %w", err)
	}
	tmpName := f.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()

	n, copyErr := io.Copy(f, r)
	if copyErr != nil {
		_ = f.Close()
		return 0, fmt.Errorf("storage: write temp: %w", copyErr)
	}
	if syncErr := f.Sync(); syncErr != nil {
		_ = f.Close()
		return 0, fmt.Errorf("storage: fsync temp: %w", syncErr)
	}
	if closeErr := f.Close(); closeErr != nil {
		return 0, fmt.Errorf("storage: close temp: %w", closeErr)
	}
	if renameErr := os.Rename(tmpName, dstPath); renameErr != nil {
		return 0, fmt.Errorf("storage: rename %s -> %s: %w", tmpName, dstPath, renameErr)
	}
	cleanup = false

	// Parent dir fsync (Linux) — ensures the rename is durable.
	if pf, err := os.Open(filepath.Dir(dstPath)); err == nil {
		_ = pf.Sync()
		_ = pf.Close()
	}

	return n, nil
}
