package tls

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// UploadLayout carries the filesystem paths ApplyUploadAt writes to. All
// fields must be set (absolute paths). Audit finding #4 — made explicit so
// admin-uploaded certs always land where cfg.TLS.* says they should.
type UploadLayout struct {
	CertPath   string // final live cert destination
	KeyPath    string // final live key destination
	HistoryDir string // parent directory for timestamped rollback archives
}

// ApplyUpload is a thin compatibility wrapper that derives the historical
// <dataRoot>/certs/{server.crt,server.key,uploaded} layout and delegates to
// ApplyUploadAt. New code should call ApplyUploadAt directly with explicit
// paths so cfg.TLS.{cert_path,key_path} are honored.
func ApplyUpload(ctx context.Context, certPEM, keyPEM []byte, dataRoot string, holder *CertHolder) error {
	return ApplyUploadAt(ctx, certPEM, keyPEM, UploadLayout{
		CertPath:   filepath.Join(dataRoot, "certs", "server.crt"),
		KeyPath:    filepath.Join(dataRoot, "certs", "server.key"),
		HistoryDir: filepath.Join(dataRoot, "certs", "uploaded"),
	}, holder)
}

// ApplyUploadAt accepts an admin-supplied cert+key pair, validates them,
// archives a timestamped copy under layout.HistoryDir/<ts>/, atomically
// replaces the live files at layout.CertPath / layout.KeyPath, and finally
// calls holder.Swap so the next TLS handshake presents the new cert.
//
// Atomicity (WR-03): both live files are written to `.new` staging paths
// FIRST (with fsync), then renamed into place. If EITHER staging write fails
// the function aborts BEFORE any rename so the live pair on disk remains the
// previous matched set. Even if a crash interrupts the two renames between
// them, next-boot recovery reads the existing cert+key pair (which was the
// OLD matched pair until rename #1 lands) — the only small window where
// disk state could be mismatched is the microsecond between the two
// os.Rename calls. That is tight enough that the CertHolder in-memory will
// have already been swapped for the current process; a crashed restart sees
// a mismatched pair on disk and the startup Swap will fail loudly, but no
// earlier partially-written state can be observed.
func ApplyUploadAt(ctx context.Context, certPEM, keyPEM []byte, layout UploadLayout, holder *CertHolder) error {
	if holder == nil {
		return fmt.Errorf("tls: apply upload: nil holder")
	}
	if layout.CertPath == "" || layout.KeyPath == "" || layout.HistoryDir == "" {
		return fmt.Errorf("tls: apply upload: empty path in layout")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// 1. Validate via scratch holder.
	scratch := NewCertHolder()
	if err := scratch.Swap(certPEM, keyPEM); err != nil {
		return err
	}

	crtFinal := layout.CertPath
	keyFinal := layout.KeyPath
	if err := os.MkdirAll(filepath.Dir(crtFinal), 0o750); err != nil {
		return fmt.Errorf("tls: apply upload: mkdir cert parent: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(keyFinal), 0o750); err != nil {
		return fmt.Errorf("tls: apply upload: mkdir key parent: %w", err)
	}
	crtStage := crtFinal + ".new"
	keyStage := keyFinal + ".new"

	// Pre-clean any leftover staging files from a previous crash.
	_ = os.Remove(crtStage)
	_ = os.Remove(keyStage)

	// 2. Stage both with fsync. On any failure, clean up both stage files.
	stageFail := func(err error) error {
		_ = os.Remove(crtStage)
		_ = os.Remove(keyStage)
		return err
	}
	if err := writeSyncFile(crtStage, certPEM, 0o644); err != nil {
		return stageFail(fmt.Errorf("tls: apply upload: stage crt: %w", err))
	}
	if err := writeSyncFile(keyStage, keyPEM, 0o600); err != nil {
		return stageFail(fmt.Errorf("tls: apply upload: stage key: %w", err))
	}

	// 3. Archive a timestamped rollback copy.
	ts := time.Now().UTC().Format("20060102T150405Z")
	histDir := filepath.Join(layout.HistoryDir, ts)
	if err := os.MkdirAll(histDir, 0o750); err != nil {
		return stageFail(fmt.Errorf("tls: apply upload: mkdir history: %w", err))
	}
	if err := os.WriteFile(filepath.Join(histDir, "server.crt"), certPEM, 0o644); err != nil {
		return stageFail(fmt.Errorf("tls: apply upload: write history crt: %w", err))
	}
	if err := os.WriteFile(filepath.Join(histDir, "server.key"), keyPEM, 0o600); err != nil {
		return stageFail(fmt.Errorf("tls: apply upload: write history key: %w", err))
	}

	// 4. Promote staged files. If the cert rename fails, we haven't touched
	// the live pair — nothing to roll back; just clean up the key stage. If
	// the cert rename succeeds but the key rename fails, the live pair is
	// temporarily mismatched on disk; try to restore from the archive.
	if err := os.Rename(crtStage, crtFinal); err != nil {
		return stageFail(fmt.Errorf("tls: apply upload: rename crt: %w", err))
	}
	if err := os.Rename(keyStage, keyFinal); err != nil {
		// Attempt rollback of the cert to the archived history copy of the
		// PREVIOUS valid pair. We don't have it handy, but the current
		// live key (the OLD key) is still intact — re-copy the NEW key
		// from the history we just wrote; if that fails the operator must
		// intervene. (This narrow window is documented in the comment.)
		_ = os.Remove(keyStage)
		if reErr := copyFile(filepath.Join(histDir, "server.key"), keyFinal, 0o600); reErr != nil {
			return fmt.Errorf("tls: apply upload: rename key: %w (recovery also failed: %v)", err, reErr)
		}
		return fmt.Errorf("tls: apply upload: rename key: %w", err)
	}
	// Parent-dir fsync for rename durability (ext4/xfs/btrfs on Linux).
	if pf, err := os.Open(filepath.Dir(crtFinal)); err == nil {
		_ = pf.Sync()
		_ = pf.Close()
	}

	// 5. Swap holder.
	return holder.Swap(certPEM, keyPEM)
}

// writeSyncFile writes data to path with the given mode, fsyncs the file,
// and closes it. Unlike os.WriteFile it does NOT use atomic temp+rename
// (caller's job — this helper is used for the staging step which is
// explicitly non-final).
func writeSyncFile(path string, data []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// copyFile copies src to dst with the given mode. Used for rollback only.
func copyFile(src, dst string, mode os.FileMode) error {
	sf, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = sf.Close() }()
	df, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(df, sf); err != nil {
		_ = df.Close()
		return err
	}
	if err := df.Sync(); err != nil {
		_ = df.Close()
		return err
	}
	return df.Close()
}
