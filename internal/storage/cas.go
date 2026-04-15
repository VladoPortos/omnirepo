package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// CAS is the content-addressed blob store (D-28). Blobs live at
// <root>/sha256/<first-two-hex>/<full-hex>. Put streams the input, hashes
// inline, and atomically places the result at its content address. Puts of
// already-existing content are idempotent: the temp file is discarded and
// the on-disk blob (inode/mtime) is untouched.
type CAS interface {
	Put(ctx context.Context, r io.Reader) (digest string, size int64, err error)
	Get(ctx context.Context, digest string) (io.ReadCloser, error)
	Stat(ctx context.Context, digest string) (size int64, exists bool, err error)
	Exists(ctx context.Context, digest string) (bool, error)
	Delete(ctx context.Context, digest string) error
}

type cas struct {
	root   string // final blob tree root, e.g. /var/lib/omnirepo/blobs
	tmpDir string // staging area siblings to the tree (<root>/.tmp)
}

// NewCAS returns a CAS rooted at root. Callers typically pass
// /var/lib/omnirepo/blobs.
func NewCAS(root string) CAS {
	return &cas{
		root:   root,
		tmpDir: filepath.Join(root, ".tmp"),
	}
}

// blobPath returns the canonical on-disk path for digest ("sha256:<hex>").
func (c *cas) blobPath(digest string) (string, error) {
	hx := strings.TrimPrefix(digest, "sha256:")
	if len(hx) != 64 {
		return "", fmt.Errorf("cas: invalid digest %q", digest)
	}
	return filepath.Join(c.root, "sha256", hx[:2], hx), nil
}

// Put streams r, hashes it with sha256, and atomically lands the bytes at
// their content address. If an existing blob is already present for the
// computed digest, the staging temp file is discarded and the existing file
// is left in place (idempotent; inode preserved).
func (c *cas) Put(ctx context.Context, r io.Reader) (string, int64, error) {
	if err := ctx.Err(); err != nil {
		return "", 0, err
	}
	if err := os.MkdirAll(c.tmpDir, 0o750); err != nil {
		return "", 0, fmt.Errorf("cas: mkdir tmp: %w", err)
	}

	tmp, err := os.CreateTemp(c.tmpDir, ".omnirepo-cas-*.partial")
	if err != nil {
		return "", 0, fmt.Errorf("cas: create temp: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()

	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(tmp, h), r)
	if copyErr != nil {
		_ = tmp.Close()
		return "", 0, fmt.Errorf("cas: write: %w", copyErr)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return "", 0, fmt.Errorf("cas: fsync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", 0, fmt.Errorf("cas: close temp: %w", err)
	}

	digest := "sha256:" + hex.EncodeToString(h.Sum(nil))
	final, err := c.blobPath(digest)
	if err != nil {
		return "", 0, err
	}

	// Idempotent skip if final already exists.
	if _, statErr := os.Stat(final); statErr == nil {
		// Discard temp; leave existing blob untouched.
		return digest, n, nil
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return "", 0, fmt.Errorf("cas: stat final: %w", statErr)
	}

	if err := os.MkdirAll(filepath.Dir(final), 0o750); err != nil {
		return "", 0, fmt.Errorf("cas: mkdir final dir: %w", err)
	}
	if err := os.Rename(tmpName, final); err != nil {
		return "", 0, fmt.Errorf("cas: rename: %w", err)
	}
	cleanup = false

	// Parent fsync for durability.
	if pf, err := os.Open(filepath.Dir(final)); err == nil {
		_ = pf.Sync()
		_ = pf.Close()
	}
	return digest, n, nil
}

func (c *cas) Get(ctx context.Context, digest string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p, err := c.blobPath(digest)
	if err != nil {
		return nil, err
	}
	return os.Open(p)
}

func (c *cas) Stat(ctx context.Context, digest string) (int64, bool, error) {
	if err := ctx.Err(); err != nil {
		return 0, false, err
	}
	p, err := c.blobPath(digest)
	if err != nil {
		return 0, false, err
	}
	info, err := os.Stat(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, false, nil
		}
		return 0, false, err
	}
	return info.Size(), true, nil
}

func (c *cas) Exists(ctx context.Context, digest string) (bool, error) {
	_, ok, err := c.Stat(ctx, digest)
	return ok, err
}

func (c *cas) Delete(ctx context.Context, digest string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p, err := c.blobPath(digest)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
