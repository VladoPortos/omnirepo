package storage

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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
// directory so Restore can reconstruct the exact pre-delete path. Without
// this, Restore stripped to basename and could collide with or overwrite
// unrelated content.
const trashMetaFile = "omnirepo-trash.json"

// TrashEntry describes one soft-deleted tree under the trash root.
type TrashEntry struct {
	Path         string    // absolute path on disk (the holder dir)
	MovedAt      time.Time // parsed from the directory name unix-ts prefix
	Kind         string    // "repo", "project", "user", "s3-bucket", ...
	OriginalID   int64     // numeric id from the caller
	OriginalPath string    // original on-disk path (pre-move). Empty for
	// legacy entries written before the sidecar was introduced; callers MUST
	// fall back for those.
	DeletedByUser string // users.login of the actor who triggered the
	// soft-delete. Empty for legacy entries and for GC-driven / system-
	// initiated moves.
	Empty bool // true when the soft-delete was metadata-only (the source
	// tree was absent at delete time — e.g. a git mirror that was
	// created but never synced). Restore re-creates the DB row and only
	// mkdir's the parent; there is no content to rename back.

	// RowSnapshot is populated for drift-purge trash entries:
	// the sidecar carries a verbatim column map of the purged DB row
	// so the Restore handler can UPSERT it back. Nil for non-drift
	// entries and legacy entries written before row snapshotting existed.
	RowSnapshot json.RawMessage
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
	// Empty marks trash entries created for a soft-delete whose on-disk
	// tree was absent (e.g. a never-synced git mirror). Restore treats
	// these as metadata-only and only re-creates the DB row + target
	// parent dir; the sync handler will populate content on first success.
	Empty bool `json:"empty,omitempty"`
	// RowSnapshot is populated for drift-purge trash entries:
	// the sidecar carries a verbatim column map of the purged DB row
	// so the Restore handler can UPSERT it back. Nil for non-drift
	// entries and legacy entries written before row snapshotting existed. The
	// omitempty tag preserves wire compatibility with old sidecars
	// (a missing key decodes to nil RawMessage).
	RowSnapshot json.RawMessage `json:"row_snapshot,omitempty"`
}

// Trash is the soft-delete primitive. Move renames a tree into
// <root>/<unix-ts>-<kind>-<id>/<basename>. Restore renames it back. Hard
// delete (7-day retention) is GC's concern.
//
// actor is the users.login of the caller triggering the soft-delete.
// Pass "" for GC/system-initiated moves; the audit trail falls back to
// an empty "deleted_by" which the UI renders as "—".
type Trash interface {
	Move(ctx context.Context, srcPath string, kind string, id int64, actor string) (trashPath string, err error)
	// MoveWithSnapshot soft-deletes srcPath AND stores rowSnapshot as
	// the sidecar's RowSnapshot field. Used by drift-purge adapters
	// (internal/driftpurge/*_adapter.go) so Restore can UPSERT the
	// DB row back alongside the file-tree rename. Callers that don't
	// need row snapshotting should use Move. Passing a nil snapshot
	// is equivalent to Move (the JSON key is omitted from the sidecar
	// via omitempty).
	MoveWithSnapshot(ctx context.Context, srcPath string, kind string, id int64, actor string, rowSnapshot json.RawMessage) (trashPath string, err error)
	Restore(ctx context.Context, trashPath string, dstPath string) error
	List(ctx context.Context) ([]TrashEntry, error)
}

type trashImpl struct {
	root string
}

var writeTrashSidecar = func(path string, data []byte, perm os.FileMode) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
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

// NewTrash returns a Trash rooted at root. Callers typically pass
// /var/lib/omnirepo/trash.
func NewTrash(root string) Trash {
	return &trashImpl{root: root}
}

func (t *trashImpl) Move(ctx context.Context, srcPath, kind string, id int64, actor string) (string, error) {
	return t.moveInternal(ctx, srcPath, kind, id, actor, nil)
}

// MoveWithSnapshot is the drift-purge ingress: identical to Move
// except that rowSnapshot is stamped into the sidecar's RowSnapshot
// field. Passing nil is equivalent to plain Move (the JSON tag is
// omitempty so the key never appears for nil snapshots).
func (t *trashImpl) MoveWithSnapshot(ctx context.Context, srcPath, kind string, id int64, actor string, rowSnapshot json.RawMessage) (string, error) {
	return t.moveInternal(ctx, srcPath, kind, id, actor, rowSnapshot)
}

// moveInternal is the shared body for Move + MoveWithSnapshot.
// rowSnapshot is nil for plain Move; non-nil snapshots are stamped
// into the sidecar's RowSnapshot field for drift-purge restore.
func (t *trashImpl) moveInternal(ctx context.Context, srcPath, kind string, id int64, actor string, rowSnapshot json.RawMessage) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if kind == "" {
		return "", errors.New("trash: kind must not be empty")
	}
	now := time.Now()
	// The (unix-second, kind, id) tuple is NOT unique: for per-file deletes
	// (RAW/rpm/deb/pypi/helm) `id` is the shared repo id, so two files removed
	// from one repo within the same wall-clock second produced an identical
	// holder dir — MkdirAll no-op'd onto the existing dir, the single sidecar
	// was overwritten, List surfaced only one entry, and GC then hard-deleted
	// the unrecoverable remainder (silent data loss). Append a random,
	// guaranteed-non-numeric suffix so every Move gets its own holder + sidecar
	// (and same-basename files can no longer collide on rename either).
	if err := os.MkdirAll(t.root, 0o750); err != nil {
		return "", fmt.Errorf("trash: mkdir root: %w", err)
	}
	// Create a UNIQUE holder with os.Mkdir (not MkdirAll) so a suffix collision
	// — vanishingly unlikely, but possible under concurrency — is detected as
	// os.ErrExist and retried with a fresh suffix, rather than silently reusing
	// an existing holder (which is the data-loss mode this fix prevents).
	var dstDir string
	for attempts := 0; ; attempts++ {
		holder := fmt.Sprintf("%d-%s-%d-%s", now.Unix(), kind, id, trashUniqSuffix())
		dstDir = filepath.Join(t.root, holder)
		mkErr := os.Mkdir(dstDir, 0o750)
		if mkErr == nil {
			break
		}
		if errors.Is(mkErr, os.ErrExist) && attempts < 8 {
			continue
		}
		return "", fmt.Errorf("trash: mkdir: %w", mkErr)
	}
	dst := filepath.Join(dstDir, filepath.Base(srcPath))
	// Tolerate a missing source. Git mirrors that have
	// never synced (InitBare intentionally skipped) have
	// no on-disk dir, and freshly-created non-mirror repos may also be
	// deleted before any data lands. Without an entry the admin trash
	// list can't surface them and /admin/trash/{id}/restore has nothing
	// to target — the DB row sits orphaned-soft-deleted. Create a
	// metadata-only entry so Restore can bring the row back even when
	// there is nothing to rename.
	var missing bool
	if _, statErr := os.Stat(srcPath); errors.Is(statErr, os.ErrNotExist) {
		missing = true
	} else if statErr != nil {
		_ = os.Remove(dstDir)
		return "", fmt.Errorf("trash: stat source: %w", statErr)
	}
	// Persist restore metadata before removing the live source. A successful
	// rename without this sidecar cannot be restored to its exact nested path
	// and may later be purged by retention GC.
	meta := trashMetadata{
		OriginalPath:  srcPath,
		Kind:          kind,
		OriginalID:    id,
		MovedAtUnix:   now.Unix(),
		DeletedByUser: actor,
		Empty:         missing,
		RowSnapshot:   rowSnapshot,
	}
	b, err := json.Marshal(meta)
	if err != nil {
		_ = os.Remove(dstDir)
		return "", fmt.Errorf("trash: marshal sidecar: %w", err)
	}
	if err := writeTrashSidecar(filepath.Join(dstDir, trashMetaFile), b, 0o640); err != nil {
		_ = os.Remove(dstDir)
		return "", fmt.Errorf("trash: write sidecar: %w", err)
	}
	if !missing {
		if err := os.Rename(srcPath, dst); err != nil {
			_ = os.Remove(filepath.Join(dstDir, trashMetaFile))
			_ = os.Remove(dstDir)
			return "", fmt.Errorf("trash: rename: %w", err)
		}
	}
	if missing {
		return "", os.ErrNotExist
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
				parsed.Empty = m.Empty
				parsed.RowSnapshot = m.RowSnapshot
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

// trashUniqSuffix returns a short, random, guaranteed-non-numeric token that
// makes each holder directory unique even when (unix-second, kind, id)
// collide. The leading "u" guarantees parseTrashHolder can distinguish the
// suffix from the numeric id token. A crypto/rand hiccup falls back to a
// nanosecond stamp (still non-numeric via the "u" prefix) so a soft-delete
// never fails on entropy.
func trashUniqSuffix() string {
	b := make([]byte, 5)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("u%x", time.Now().UnixNano())
	}
	return "u" + hex.EncodeToString(b)
}

// parseTrashHolder splits a holder dir name back into its parts. Two formats
// are accepted:
//
//	<ts>-<kind...>-<id>-<uniq>   (current; uniq is non-numeric, "u"-prefixed)
//	<ts>-<kind...>-<id>          (legacy, pre-uniqueness-fix entries on disk)
//
// kind may itself contain hyphens ("s3-bucket"). The format is disambiguated
// by whether the last token parses as a base-10 int: a non-numeric last token
// is the uniq suffix, so the id is the token before it.
func parseTrashHolder(name string) (TrashEntry, bool) {
	parts := strings.Split(name, "-")
	if len(parts) < 3 {
		return TrashEntry{}, false
	}
	ts, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return TrashEntry{}, false
	}
	// Default to the legacy layout (id is the last token); if the last token
	// is the non-numeric uniq suffix, the id is the second-to-last token.
	idIdx := len(parts) - 1
	if _, e := strconv.ParseInt(parts[idIdx], 10, 64); e != nil {
		idIdx = len(parts) - 2
		if idIdx < 2 { // need at least ts + kind + id before the suffix
			return TrashEntry{}, false
		}
	}
	id, err := strconv.ParseInt(parts[idIdx], 10, 64)
	if err != nil {
		return TrashEntry{}, false
	}
	kind := strings.Join(parts[1:idIdx], "-")
	if kind == "" {
		return TrashEntry{}, false
	}
	return TrashEntry{
		MovedAt:    time.Unix(ts, 0),
		Kind:       kind,
		OriginalID: id,
	}, true
}
