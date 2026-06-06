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

// CAS is the content-addressed blob store. Blobs live at
// <root>/sha256/<first-two-hex>/<full-hex>. Put streams the input, hashes
// inline, and atomically places the result at its content address. Puts of
// already-existing content are idempotent: the temp file is discarded and
// the on-disk blob (inode/mtime) is untouched.
type CAS interface {
	Put(ctx context.Context, r io.Reader) (digest string, size int64, err error)
	// PutFromPath hashes the file at srcPath, then promotes it into the
	// CAS via a single atomic os.Rename (no second io.Copy). On success
	// srcPath no longer exists: either it was renamed into the CAS, or
	// — when the computed digest was already present — srcPath was
	// removed and the existing on-disk blob was left untouched (inode
	// preserved). Callers MUST NOT unlink srcPath after calling.
	//
	// Use this when the upload path already streamed bytes to a tmp
	// file on the same filesystem as the CAS root (the OCI chunked
	// upload state machine does exactly that). It avoids a second copy
	// through user space and keeps promotion atomic under the
	// single-filesystem guarantee.
	//
	// Errors from a missing srcPath wrap os.ErrNotExist.
	PutFromPath(ctx context.Context, srcPath string) (digest string, size int64, err error)
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

// PutFromPath promotes the file at srcPath into the CAS via atomic rename.
// See CAS.PutFromPath doc for semantics. Consumes srcPath on every success
// path (either via rename-into-CAS or unlink-on-idempotent-skip); leaves it
// untouched on error.
func (c *cas) PutFromPath(ctx context.Context, srcPath string) (string, int64, error) {
	if err := ctx.Err(); err != nil {
		return "", 0, err
	}
	// Open + hash the source in one pass. Explicit fs.ErrNotExist wrapping
	// so callers can distinguish a missing session tmp file from other IO
	// errors.
	f, err := os.Open(srcPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", 0, fmt.Errorf("cas: open %s: %w", srcPath, err)
		}
		return "", 0, fmt.Errorf("cas: open %s: %w", srcPath, err)
	}
	h := sha256.New()
	n, copyErr := io.Copy(h, f)
	closeErr := f.Close()
	if copyErr != nil {
		return "", 0, fmt.Errorf("cas: hash %s: %w", srcPath, copyErr)
	}
	if closeErr != nil {
		return "", 0, fmt.Errorf("cas: close %s: %w", srcPath, closeErr)
	}

	digest := "sha256:" + hex.EncodeToString(h.Sum(nil))
	final, err := c.blobPath(digest)
	if err != nil {
		return "", 0, err
	}

	// Idempotent skip: existing blob wins; drop the source.
	if _, statErr := os.Stat(final); statErr == nil {
		if rmErr := os.Remove(srcPath); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
			return "", 0, fmt.Errorf("cas: remove idempotent src %s: %w", srcPath, rmErr)
		}
		return digest, n, nil
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return "", 0, fmt.Errorf("cas: stat final: %w", statErr)
	}

	// Rename srcPath → final. os.Rename is atomic on a single Linux
	// filesystem. Callers that stream to c.tmpDir (or a sibling path
	// inside the data root) stay on the same FS as c.root and therefore
	// get the atomic guarantee. If srcPath lies on a different FS, the
	// rename returns EXDEV — we surface it as a typed error so the
	// upload handler can fall back to a Put(io.Reader) path if needed.
	if err := os.MkdirAll(filepath.Dir(final), 0o750); err != nil {
		return "", 0, fmt.Errorf("cas: mkdir final dir: %w", err)
	}
	if err := os.Rename(srcPath, final); err != nil {
		return "", 0, fmt.Errorf("cas: rename %s -> %s: %w", srcPath, final, err)
	}

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

// TrimSHA256Prefix returns the hex part of a "sha256:<hex>" digest, or the
// input unchanged when the prefix is absent. Shared by the rpm/pypi regen
// writers (XML/JSON metadata carries the bare hex form).
func TrimSHA256Prefix(d string) string {
	const prefix = "sha256:"
	if len(d) > len(prefix) && d[:len(prefix)] == prefix {
		return d[len(prefix):]
	}
	return d
}
