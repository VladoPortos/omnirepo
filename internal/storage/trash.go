package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// trashMetaFile is the sidecar JSON name written inside each trash holder
// directory so Restore can reconstruct the exact pre-delete path. Audit
// finding #2: without this, Restore stripped to basename and could collide
// with or overwrite unrelated content.
const trashMetaFile = "omnirepo-trash.json"

// TrashEntry describes one soft-deleted tree under the trash root.
type TrashEntry struct {
	Path          string    // absolute path on disk (the holder dir)
	MovedAt       time.Time // parsed from the directory name unix-ts prefix
	Kind          string    // "repo", "project", "user", "s3-bucket", ...
	OriginalID    int64     // numeric id from the caller
	OriginalPath  string    // original on-disk path (pre-move). Empty for
	// legacy entries written before audit finding #2 fix; callers MUST
	// fall back for those.
	DeletedByUser string // F-15: users.login of the actor who triggered the
	// soft-delete. Empty for legacy entries and for GC-driven / system-
	// initiated moves.
}

// trashMetadata is the on-disk shape of the sidecar written by Move and
// read by List. Keep field names stable — existing trash entries on disk
// are forwards-compatible only so long as this struct does not rename.
// New fields are added with `omitempty` so old readers ignore them.
type trashMetadata struct {
	OriginalPath  string `json:"original_path"`
	Kind          string `json:"kind"`
	OriginalID    int64  `json:"original_id"`
	MovedAtUnix   int64  `json:"moved_at_unix"`
	DeletedByUser string `json:"deleted_by,omitempty"`
}

// Trash is the soft-delete primitive (D-31). Move renames a tree into
// <root>/<unix-ts>-<kind>-<id>/<basename>. Restore renames it back. Hard
// delete (7-day retention) is Phase 2 GC's concern.
//
// actor is the users.login of the caller triggering the soft-delete.
// Pass "" for GC/system-initiated moves; the audit trail falls back to
// an empty "deleted_by" which the UI renders as "—".
type Trash interface {
	Move(ctx context.Context, srcPath string, kind string, id int64, actor string) (trashPath string, err error)
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

func (t *trashImpl) Move(ctx context.Context, srcPath, kind string, id int64, actor string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if kind == "" {
		return "", errors.New("trash: kind must not be empty")
	}
	now := time.Now()
	holder := fmt.Sprintf("%d-%s-%d", now.Unix(), kind, id)
	dstDir := filepath.Join(t.root, holder)
	if err := os.MkdirAll(dstDir, 0o750); err != nil {
		return "", fmt.Errorf("trash: mkdir: %w", err)
	}
	dst := filepath.Join(dstDir, filepath.Base(srcPath))
	if err := os.Rename(srcPath, dst); err != nil {
		return "", fmt.Errorf("trash: rename: %w", err)
	}
	// Sidecar metadata — written after the rename so a failed Move doesn't
	// leave a stale metadata file behind. Best-effort: trash restore falls
	// back to basename-only behavior if the sidecar is missing, which
	// preserves the legacy invariant for callers that might still
	// rely on the old shape (no regressions on read).
	meta := trashMetadata{
		OriginalPath:  srcPath,
		Kind:          kind,
		OriginalID:    id,
		MovedAtUnix:   now.Unix(),
		DeletedByUser: actor,
	}
	if b, err := json.Marshal(meta); err == nil {
		_ = os.WriteFile(filepath.Join(dstDir, trashMetaFile), b, 0o640)
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
	// Remove the sidecar and then the holder dir (both best-effort). The
	// sidecar cleanup must precede os.Remove, which only succeeds on empty
	// dirs.
	holderDir := filepath.Dir(trashPath)
	_ = os.Remove(filepath.Join(holderDir, trashMetaFile))
	_ = os.Remove(holderDir)
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
		// Overlay sidecar metadata when present. Legacy entries (pre-#2)
		// have no sidecar and leave OriginalPath empty.
		if b, rerr := os.ReadFile(filepath.Join(parsed.Path, trashMetaFile)); rerr == nil {
			var m trashMetadata
			if json.Unmarshal(b, &m) == nil {
				parsed.OriginalPath = m.OriginalPath
				parsed.DeletedByUser = m.DeletedByUser
				// Sidecar kind/id override the holder-name parse in case
				// the holder name was constructed before the kind format
				// stabilized.
				if m.Kind != "" {
					parsed.Kind = m.Kind
				}
				if m.OriginalID != 0 {
					parsed.OriginalID = m.OriginalID
				}
			}
		}
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
