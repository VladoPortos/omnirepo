package tls

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/dxc-internal/omnirepo/internal/storage"
)

// ApplyUpload accepts an admin-supplied cert+key pair, validates them,
// archives a timestamped copy under <dataRoot>/certs/uploaded/<ts>/, atomically
// replaces the live files under <dataRoot>/certs/server.{crt,key} via
// storage.WriteAndRename, and finally calls holder.Swap so the next TLS
// handshake presents the new cert.
//
// Order matters:
//  1. Validate pair in a scratch holder first — never touch disk or the live
//     holder until we know the pair is well-formed.
//  2. Write the history copy (source of truth for rollback).
//  3. Atomically replace the live files via temp+rename+fsync.
//  4. Swap the live holder.
//
// If step 3 succeeds but step 4 fails (extremely unlikely — Swap re-parses
// the same bytes the scratch holder already parsed), the function returns
// the error and leaves the on-disk state with the new cert. The caller
// should surface the error to the admin; the next process restart will
// re-parse the live files and recover the intended state.
func ApplyUpload(ctx context.Context, certPEM, keyPEM []byte, dataRoot string, holder *CertHolder) error {
	if holder == nil {
		return fmt.Errorf("tls: apply upload: nil holder")
	}
	// 1. Validate via scratch holder.
	scratch := NewCertHolder()
	if err := scratch.Swap(certPEM, keyPEM); err != nil {
		return err
	}
	// 2. Archive.
	ts := time.Now().UTC().Format("20060102T150405Z")
	histDir := filepath.Join(dataRoot, "certs", "uploaded", ts)
	if err := os.MkdirAll(histDir, 0o750); err != nil {
		return fmt.Errorf("tls: apply upload: mkdir history: %w", err)
	}
	if err := os.WriteFile(filepath.Join(histDir, "server.crt"), certPEM, 0o644); err != nil {
		return fmt.Errorf("tls: apply upload: write history crt: %w", err)
	}
	if err := os.WriteFile(filepath.Join(histDir, "server.key"), keyPEM, 0o600); err != nil {
		return fmt.Errorf("tls: apply upload: write history key: %w", err)
	}
	// 3. Atomic live-file swap.
	live := filepath.Join(dataRoot, "certs")
	tmpDir := filepath.Join(dataRoot, "tmp")
	if _, err := storage.WriteAndRename(ctx, tmpDir, filepath.Join(live, "server.crt"), bytes.NewReader(certPEM)); err != nil {
		return fmt.Errorf("tls: apply upload: replace live crt: %w", err)
	}
	if _, err := storage.WriteAndRename(ctx, tmpDir, filepath.Join(live, "server.key"), bytes.NewReader(keyPEM)); err != nil {
		return fmt.Errorf("tls: apply upload: replace live key: %w", err)
	}
	// Tighten key mode post-rename (WriteAndRename creates files as 0600 by CreateTemp default, but make explicit).
	_ = os.Chmod(filepath.Join(live, "server.key"), 0o600)
	// 4. Swap holder.
	return holder.Swap(certPEM, keyPEM)
}
