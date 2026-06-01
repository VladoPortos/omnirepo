// Package app hosts process-wide lifecycle helpers: data-root directory
// creation, graceful-shutdown wiring (later plan), and other bits that don't
// belong to a single subsystem.
package app

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// dataRootDirs lists every subdirectory required under the data root.
// Note that blobs/sha256 is deliberately NOT pre-created — the CAS layer
// creates the two-character shard directories lazily on first Put().
var dataRootDirs = []struct {
	rel  string
	mode fs.FileMode
}{
	{"config", 0o700},
	{"certs", 0o750},
	{"certs/uploaded", 0o750},
	{"db", 0o750},
	{"blobs", 0o750},
	{"repos", 0o750},
	{"s3", 0o750},
	{"trash", 0o750},
	{"trivy", 0o750},
	{"trivy/db", 0o750},
	{"sboms", 0o750},
	{"logs", 0o750},
	{"tmp", 0o750},
}

// EnsureDirs creates every subdirectory under root with the correct
// permissions. The call is idempotent: re-running against a populated root
// MkdirAlls the path (no-op) and Chmods the leaf mode.
//
// Errors:
//   - If root exists and is a regular file (not a directory), returns an
//     error naming the path.
//   - If any MkdirAll or Chmod fails, returns a wrapped error naming the
//     offending subdirectory.
func EnsureDirs(root string) error {
	// Ensure root itself exists and is a directory.
	info, err := os.Stat(root)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if mkErr := os.MkdirAll(root, 0o750); mkErr != nil {
			return fmt.Errorf("app: mkdir data-root %q: %w", root, mkErr)
		}
	case err != nil:
		return fmt.Errorf("app: stat data-root %q: %w", root, err)
	case !info.IsDir():
		return fmt.Errorf("app: data-root %q is not a directory (it is a regular file)", root)
	}

	for _, d := range dataRootDirs {
		p := filepath.Join(root, d.rel)
		if err := os.MkdirAll(p, d.mode); err != nil {
			return fmt.Errorf("app: mkdir %q: %w", p, err)
		}
		// MkdirAll honours the umask; re-chmod to the exact mode so
		// sensitive dirs (config at 0700) are tight regardless of umask.
		if err := os.Chmod(p, d.mode); err != nil {
			return fmt.Errorf("app: chmod %q to %#o: %w", p, d.mode, err)
		}
	}
	return nil
}
