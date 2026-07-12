package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// ErrInvalidKey is returned when a PathStore key tries to escape the store
// root via ".." segments or an absolute path.
var ErrInvalidKey = errors.New("storage: invalid key (path escapes root or is absolute)")

// PathStore is a path-addressed file store. Writes use the same
// atomic temp+fsync+rename semantics as CAS via WriteAndRename. Keys are
// relative paths rooted at the store root; attempts to escape via "../.." or
// absolute paths are rejected with ErrInvalidKey.
type PathStore interface {
	Put(ctx context.Context, key string, r io.Reader) (size int64, err error)
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	Delete(ctx context.Context, key string) error
	Exists(ctx context.Context, key string) (bool, error)
	// Replace atomically publishes new bytes and invokes commit while holding a
	// per-key lock. If commit fails, the previous file is restored (or the new
	// file is removed when the key did not previously exist).
	Replace(ctx context.Context, key string, r io.Reader, commit func(size int64) error) (size int64, err error)
}

type pathstore struct {
	root    string
	tmpDir  string
	locksMu sync.Mutex
	locks   map[string]*keyLock
}

type keyLock struct {
	mu   sync.Mutex
	refs int
}

func (p *pathstore) lockKey(key string) func() {
	p.locksMu.Lock()
	entry := p.locks[key]
	if entry == nil {
		entry = &keyLock{}
		p.locks[key] = entry
	}
	entry.refs++
	p.locksMu.Unlock()

	entry.mu.Lock()
	return func() {
		entry.mu.Unlock()
		p.locksMu.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(p.locks, key)
		}
		p.locksMu.Unlock()
	}
}

// NewPathStore returns a PathStore rooted at root. Callers typically pass
// /var/lib/omnirepo/repos.
func NewPathStore(root string) PathStore {
	return &pathstore{
		root:   root,
		tmpDir: filepath.Join(root, ".tmp"),
		locks:  make(map[string]*keyLock),
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

func (p *pathstore) Replace(ctx context.Context, key string, r io.Reader, commit func(int64) error) (int64, error) {
	if commit == nil {
		return 0, errors.New("storage: replace commit callback is nil")
	}
	cleaned, err := cleanKey(key)
	if err != nil {
		return 0, err
	}
	dst, err := p.full(cleaned)
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(p.tmpDir, 0o750); err != nil {
		return 0, fmt.Errorf("storage: replace mkdir tmp: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return 0, fmt.Errorf("storage: replace mkdir dst: %w", err)
	}

	staged, err := os.CreateTemp(p.tmpDir, ".omnirepo-replace-*.tmp")
	if err != nil {
		return 0, fmt.Errorf("storage: replace create temp: %w", err)
	}
	stagedPath := staged.Name()
	stagedOwned := true
	defer func() {
		if stagedOwned {
			_ = os.Remove(stagedPath)
		}
	}()
	size, copyErr := io.Copy(staged, r)
	if copyErr != nil {
		_ = staged.Close()
		return 0, fmt.Errorf("storage: replace stage: %w", copyErr)
	}
	if err := staged.Sync(); err != nil {
		_ = staged.Close()
		return 0, fmt.Errorf("storage: replace sync temp: %w", err)
	}
	if err := staged.Close(); err != nil {
		return 0, fmt.Errorf("storage: replace close temp: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	unlock := p.lockKey(cleaned)
	defer unlock()
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	backup := ""
	if _, err := os.Stat(dst); err == nil {
		bf, err := os.CreateTemp(filepath.Dir(dst), ".omnirepo-previous-*.tmp")
		if err != nil {
			return 0, fmt.Errorf("storage: replace reserve backup: %w", err)
		}
		backup = bf.Name()
		if err := bf.Close(); err != nil {
			_ = os.Remove(backup)
			return 0, fmt.Errorf("storage: replace close backup: %w", err)
		}
		_ = os.Remove(backup)
		if err := os.Rename(dst, backup); err != nil {
			return 0, fmt.Errorf("storage: replace backup: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return 0, fmt.Errorf("storage: replace stat dst: %w", err)
	}

	if err := os.Rename(stagedPath, dst); err != nil {
		if backup != "" {
			_ = os.Rename(backup, dst)
		}
		return 0, fmt.Errorf("storage: replace publish: %w", err)
	}
	stagedOwned = false

	if err := commit(size); err != nil {
		removeErr := os.Remove(dst)
		var restoreErr error
		if backup != "" {
			restoreErr = os.Rename(backup, dst)
		}
		if removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return size, fmt.Errorf("storage: replace commit: %w (remove new file: %v)", err, removeErr)
		}
		if restoreErr != nil {
			return size, fmt.Errorf("storage: replace commit: %w (restore previous file: %v)", err, restoreErr)
		}
		return size, err
	}
	if backup != "" {
		if err := os.Remove(backup); err != nil && !errors.Is(err, os.ErrNotExist) {
			// The metadata commit and new file are already live. Reporting a
			// transactional failure here would invite callers to roll back other
			// successfully committed files, so retain consistency and surface the
			// stale backup for operational cleanup instead.
			slog.WarnContext(ctx, "storage.replace.cleanup_failed", "path", backup, "err", err)
		}
	}
	return size, nil
}
