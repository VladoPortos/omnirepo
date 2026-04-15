package storage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// TrashEntry describes one soft-deleted tree under the trash root.
type TrashEntry struct {
	Path       string    // absolute path on disk
	MovedAt    time.Time // parsed from the directory name unix-ts prefix
	Kind       string    // "repo", "project", "user", "s3-bucket", ...
	OriginalID int64     // numeric id from the caller
}

// Trash is the soft-delete primitive (D-31). Move renames a tree into
// <root>/<unix-ts>-<kind>-<id>/<basename>. Restore renames it back. Hard
// delete (7-day retention) is Phase 2 GC's concern.
type Trash interface {
	Move(ctx context.Context, srcPath string, kind string, id int64) (trashPath string, err error)
	Restore(ctx context.Context, trashPath string, dstPath string) error
	List(ctx context.Context) ([]TrashEntry, error)
}

type trashImpl struct {
	root string
}

// NewTrash returns a Trash rooted at root. Callers typically pass
// /var/lib/omnirepo/trash.
func NewTrash(root string) Trash {
	return &trashImpl{root: root}
}

func (t *trashImpl) Move(ctx context.Context, srcPath, kind string, id int64) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if kind == "" {
		return "", errors.New("trash: kind must not be empty")
	}
	holder := fmt.Sprintf("%d-%s-%d", time.Now().Unix(), kind, id)
	dstDir := filepath.Join(t.root, holder)
	if err := os.MkdirAll(dstDir, 0o750); err != nil {
		return "", fmt.Errorf("trash: mkdir: %w", err)
	}
	dst := filepath.Join(dstDir, filepath.Base(srcPath))
	if err := os.Rename(srcPath, dst); err != nil {
		return "", fmt.Errorf("trash: rename: %w", err)
	}
	return dst, nil
}

func (t *trashImpl) Restore(ctx context.Context, trashPath, dstPath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dstPath), 0o750); err != nil {
		return fmt.Errorf("trash: restore mkdir: %w", err)
	}
	if err := os.Rename(trashPath, dstPath); err != nil {
		return fmt.Errorf("trash: restore rename: %w", err)
	}
	// Remove now-empty holder directory (best-effort).
	_ = os.Remove(filepath.Dir(trashPath))
	return nil
}

func (t *trashImpl) List(ctx context.Context) ([]TrashEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(t.root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]TrashEntry, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		parsed, ok := parseTrashHolder(e.Name())
		if !ok {
			continue
		}
		parsed.Path = filepath.Join(t.root, e.Name())
		out = append(out, parsed)
	}
	return out, nil
}

// parseTrashHolder splits "<ts>-<kind>-<id>" back into its parts.
// kind may itself contain hyphens ("s3-bucket") — the first and last tokens
// are ts and id; everything in between is kind.
func parseTrashHolder(name string) (TrashEntry, bool) {
	parts := strings.Split(name, "-")
	if len(parts) < 3 {
		return TrashEntry{}, false
	}
	ts, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return TrashEntry{}, false
	}
	id, err := strconv.ParseInt(parts[len(parts)-1], 10, 64)
	if err != nil {
		return TrashEntry{}, false
	}
	kind := strings.Join(parts[1:len(parts)-1], "-")
	if kind == "" {
		return TrashEntry{}, false
	}
	return TrashEntry{
		MovedAt:    time.Unix(ts, 0),
		Kind:       kind,
		OriginalID: id,
	}, true
}
