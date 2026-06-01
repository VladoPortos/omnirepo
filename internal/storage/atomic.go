// Package storage owns the shared write-path primitives for OmniRepo: the
// atomic temp+fsync+rename helper (used by every writer), content-addressed
// blob store (CAS), path-addressed file store (PathStore), soft-delete trash,
// and per-repo mutex map. Every primitive survives crashes without leaving
// partial files at their final paths.
package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"
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

	// Parent-dir fsync (Linux) — ensures the rename is durable on
	// ext4/xfs/btrfs. Previously an Open failure here was silently swallowed;
	// now we return it so callers know the durability guarantee was not met.
	pf, openErr := os.Open(filepath.Dir(dstPath))
	if openErr != nil {
		return n, fmt.Errorf("storage: open parent dir for fsync: %w", openErr)
	}
	syncErr := pf.Sync()
	closeErr := pf.Close()
	if syncErr != nil {
		return n, fmt.Errorf("storage: fsync parent dir: %w", syncErr)
	}
	if closeErr != nil {
		return n, fmt.Errorf("storage: close parent dir: %w", closeErr)
	}

	return n, nil
}

// SwapDir replaces dstDir with srcDir using a three-rename dance so a failure
// mid-swap never leaves dstDir missing:
//
//  1. If dstDir exists, rename it to a sibling backup path.
//  2. Rename srcDir → dstDir.
//  3. Remove the backup.
//
// If step 2 fails, the backup is renamed back to dstDir before returning the
// error. The caller is responsible for making sure srcDir and dstDir live on
// the same filesystem (otherwise os.Rename falls back to copy-and-errors).
//
// This is the safe replacement for "os.RemoveAll(dst); os.Rename(src, dst)"
// used by destructive swap-in-place code paths.
func SwapDir(srcDir, dstDir string) error {
	if srcDir == "" || dstDir == "" {
		return errors.New("storage: SwapDir: src/dst must be non-empty")
	}
	if err := os.MkdirAll(filepath.Dir(dstDir), 0o750); err != nil {
		return fmt.Errorf("storage: swap dst parent mkdir: %w", err)
	}
	var backup string
	if _, err := os.Stat(dstDir); err == nil {
		backup = dstDir + ".old-" + strconv.FormatInt(time.Now().UnixNano(), 10)
		if err := os.Rename(dstDir, backup); err != nil {
			return fmt.Errorf("storage: swap backup rename: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("storage: swap stat dst: %w", err)
	}
	if err := os.Rename(srcDir, dstDir); err != nil {
		// Best-effort restore the previous dir so the live location never
		// ends up missing on a failed swap.
		if backup != "" {
			_ = os.Rename(backup, dstDir)
		}
		return fmt.Errorf("storage: swap rename: %w", err)
	}
	if backup != "" {
		_ = os.RemoveAll(backup)
	}
	return nil
}
