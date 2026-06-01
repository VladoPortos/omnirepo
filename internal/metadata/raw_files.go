package metadata

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// RawFile mirrors one raw_files row.
type RawFile struct {
	RepoID    int64
	Path      string
	SizeBytes int64
	MIME      string
	SHA256    string
	Modified  time.Time
}

// RawFilesRepo owns CRUD for raw_files. Writers are expected to pass a
// *sql.Tx (obtained via DB.WriteTx) so the INSERT rides in the same writer
// transaction as the FTS5 + scans enqueue helpers the RAW handler drives.
// Reads go through the reader pool directly.
type RawFilesRepo struct{ db *DB }

// NewRawFilesRepo constructs a repo bound to db.
func NewRawFilesRepo(db *DB) *RawFilesRepo { return &RawFilesRepo{db: db} }

// Insert upserts the (repo_id, path) row. modified is refreshed to
// CURRENT_TIMESTAMP on every call.
//
// The PRIMARY KEY is composite on (repo_id, path); ON CONFLICT DO UPDATE
// matches that exact conflict target. We refresh size_bytes, mime, sha256,
// and modified — everything that can change when a file is overwritten.
func (r *RawFilesRepo) Insert(ctx context.Context, tx *sql.Tx, repoID int64, path string, size int64, mime, sha256 string) error {
	if path == "" {
		return errors.New("raw_files: path must not be empty")
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO raw_files(repo_id, path, size_bytes, mime, sha256, modified)
		VALUES (?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(repo_id, path) DO UPDATE SET
		  size_bytes = excluded.size_bytes,
		  mime       = excluded.mime,
		  sha256     = excluded.sha256,
		  modified   = CURRENT_TIMESTAMP
	`, repoID, path, size, mime, sha256); err != nil {
		return fmt.Errorf("raw_files: upsert (repo=%d path=%s): %w", repoID, path, err)
	}
	return nil
}

// Delete removes the row. Missing rows are a no-op (handler needs idempotent
// delete so the caller can run it inside the same tx that already verified
// existence).
func (r *RawFilesRepo) Delete(ctx context.Context, tx *sql.Tx, repoID int64, path string) error {
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM raw_files WHERE repo_id=? AND path=?`, repoID, path,
	); err != nil {
		return fmt.Errorf("raw_files: delete (repo=%d path=%s): %w", repoID, path, err)
	}
	return nil
}

// Get fetches one row. Returns (_, false, nil) when the path doesn't exist
// in this repo.
func (r *RawFilesRepo) Get(ctx context.Context, repoID int64, path string) (RawFile, bool, error) {
	var row RawFile
	err := r.db.Reader.QueryRowContext(ctx, `
		SELECT repo_id, path, size_bytes, mime, sha256, modified
		FROM raw_files WHERE repo_id=? AND path=?
	`, repoID, path).Scan(&row.RepoID, &row.Path, &row.SizeBytes, &row.MIME, &row.SHA256, &row.Modified)
	if errors.Is(err, sql.ErrNoRows) {
		return RawFile{}, false, nil
	}
	if err != nil {
		return RawFile{}, false, fmt.Errorf("raw_files: get (repo=%d path=%s): %w", repoID, path, err)
	}
	return row, true, nil
}

// ListDir returns direct children of dirPrefix within repoID. dirPrefix is a
// slash-separated path with no trailing slash; "" means the repo root.
//
// "Direct children" means: path starts with prefix "<dirPrefix>/" (or no
// prefix for root) AND the remainder after that prefix contains no further
// "/". Nested files under subdirectories are excluded. Subdirectories
// themselves do not appear as rows (only files are stored in raw_files); the
// HTTP listing handler is expected to infer subdirectory names from file
// paths that DO contain further slashes via a separate query if it needs
// them.
func (r *RawFilesRepo) ListDir(ctx context.Context, repoID int64, dirPrefix string) ([]RawFile, error) {
	dirPrefix = strings.Trim(dirPrefix, "/")
	var likeMatch, likeExcludeNested string
	if dirPrefix == "" {
		// Root: any path without a "/" separator.
		likeMatch = "%"
		likeExcludeNested = "%/%"
	} else {
		// Under dir: starts with "<dir>/" and remainder has no "/".
		likeMatch = dirPrefix + "/%"
		likeExcludeNested = dirPrefix + "/%/%"
	}
	rows, err := r.db.Reader.QueryContext(ctx, `
		SELECT repo_id, path, size_bytes, mime, sha256, modified
		FROM raw_files
		WHERE repo_id=? AND path LIKE ? AND path NOT LIKE ?
		ORDER BY path
	`, repoID, likeMatch, likeExcludeNested)
	if err != nil {
		return nil, fmt.Errorf("raw_files: listdir (repo=%d dir=%s): %w", repoID, dirPrefix, err)
	}
	defer func() { _ = rows.Close() }()
	var out []RawFile
	for rows.Next() {
		var rf RawFile
		if err := rows.Scan(&rf.RepoID, &rf.Path, &rf.SizeBytes, &rf.MIME, &rf.SHA256, &rf.Modified); err != nil {
			return nil, fmt.Errorf("raw_files: scan: %w", err)
		}
		out = append(out, rf)
	}
	return out, rows.Err()
}
