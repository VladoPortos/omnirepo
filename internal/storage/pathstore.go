package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ErrInvalidKey is returned when a PathStore key tries to escape the store
// root via ".." segments or an absolute path.
var ErrInvalidKey = errors.New("storage: invalid key (path escapes root or is absolute)")

// PathStore is a path-addressed file store (D-29). Writes use the same
// atomic temp+fsync+rename semantics as CAS via WriteAndRename. Keys are
// relative paths rooted at the store root; attempts to escape via "../.." or
// absolute paths are rejected with ErrInvalidKey.
type PathStore interface {
	Put(ctx context.Context, key string, r io.Reader) (size int64, err error)
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	Delete(ctx context.Context, key string) error
	Exists(ctx context.Context, key string) (bool, error)
}

type pathstore struct {
	root   string
	tmpDir string
}

// NewPathStore returns a PathStore rooted at root. Callers typically pass
// /var/lib/omnirepo/repos.
func NewPathStore(root string) PathStore {
	return &pathstore{
		root:   root,
		tmpDir: filepath.Join(root, ".tmp"),
	}
}

// cleanKey validates and normalizes key. Any absolute path or any cleaned
// path that contains a leading ".." segment (i.e. escapes root) returns
// ErrInvalidKey.
func cleanKey(key string) (string, error) {
	if key == "" {
		return "", ErrInvalidKey
	}
	if filepath.IsAbs(key) {
		return "", ErrInvalidKey
	}
	cleaned := filepath.Clean(key)
	// After Clean, escape attempts reduce to a leading ".." segment.
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", ErrInvalidKey
	}
	// Defense in depth: reject any ".." seen anywhere in the cleaned path.
	for _, seg := range strings.Split(cleaned, string(filepath.Separator)) {
		if seg == ".." {
			return "", ErrInvalidKey
		}
	}
	return cleaned, nil
}

func (p *pathstore) full(key string) (string, error) {
	c, err := cleanKey(key)
	if err != nil {
		return "", fmt.Errorf("%w: %s", err, key)
	}
	return filepath.Join(p.root, c), nil
}

func (p *pathstore) Put(ctx context.Context, key string, r io.Reader) (int64, error) {
	dst, err := p.full(key)
	if err != nil {
		return 0, err
	}
	return WriteAndRename(ctx, p.tmpDir, dst, r)
}

func (p *pathstore) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dst, err := p.full(key)
	if err != nil {
		return nil, err
	}
	return os.Open(dst)
}

func (p *pathstore) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	dst, err := p.full(key)
	if err != nil {
		return err
	}
	if err := os.Remove(dst); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (p *pathstore) Exists(ctx context.Context, key string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	dst, err := p.full(key)
	if err != nil {
		return false, err
	}
	if _, err := os.Stat(dst); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
