package driftpurge

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/storage"
)

// PyPIPathFn resolves the on-disk blob path for a PyPI file row. The
// caller (pypi/sync_handler.go in plan 06-07) binds it from the live
// SyncDeps so the adapter can stay free of path-construction knowledge.
type PyPIPathFn func(row *metadata.PyPIFile) string

// pypiAdapter is the DriftAdapter implementation for PyPI mirror repos.
// Drift key per D-12: {project_normalized, filename, digest}.
type pypiAdapter struct {
	upstream []Key
	files    *metadata.PyPIFilesRepo
	trash    storage.Trash
	pathFn   PyPIPathFn
}

// NewPyPIAdapter constructs a DriftAdapter for PyPI mirror repos.
// upstreamKeys MUST be built by the caller from the parsed upstream
// entries already in memory at the end of pypi/sync_handler.Handle;
// the engine never re-parses. The Key projection is identical on the
// caller side: Key{A: project_normalized, B: filename, C: digest}.
func NewPyPIAdapter(
	upstreamKeys []Key,
	files *metadata.PyPIFilesRepo,
	trash storage.Trash,
	pathFn PyPIPathFn,
) DriftAdapter {
	return &pypiAdapter{
		upstream: upstreamKeys,
		files:    files,
		trash:    trash,
		pathFn:   pathFn,
	}
}

func (a *pypiAdapter) Protocol() string    { return "pypi" }
func (a *pypiAdapter) TrashKind() string   { return "pypi_file_drift" }
func (a *pypiAdapter) UpstreamKeys() []Key { return a.upstream }

func (a *pypiAdapter) LocalRows(ctx context.Context, _ *sql.Tx, repoID int64) ([]Row, error) {
	// ListByRepo reads via the main DB reader (not tx) — drift detection
	// runs AFTER Phase 5's partial-sync commits, so the reader sees the
	// freshly-inserted rows. If tx-scoped read becomes required for a
	// future stronger-isolation variant, add PyPIFilesRepo.ListByRepoTx.
	rows, err := a.files.ListByRepo(ctx, repoID)
	if err != nil {
		return nil, err
	}
	out := make([]Row, len(rows))
	for i := range rows {
		out[i] = &pypiRow{inner: &rows[i]}
	}
	return out, nil
}

func (a *pypiAdapter) Purge(ctx context.Context, tx *sql.Tx, row Row, actor string) error {
	pr, ok := row.(*pypiRow)
	if !ok {
		return fmt.Errorf("pypi adapter: unexpected row type %T", row)
	}
	inner := pr.inner

	// Verbatim column map (excluding id and uploaded_at — Restore's
	// UPSERT regenerates uploaded_at via strftime per PATTERNS.md §3).
	snap := map[string]any{
		"repo_id":            inner.RepoID,
		"project_normalized": inner.ProjectNormalized,
		"version":            inner.Version,
		"filename":           inner.Filename,
		"kind":               inner.Kind,
		"requires_python":    inner.RequiresPython,
		"size_bytes":         inner.SizeBytes,
		"digest":             inner.Digest,
		"core_metadata_json": inner.CoreMetadataJSON,
	}
	snapBytes, err := json.Marshal(snap)
	if err != nil {
		return fmt.Errorf("pypi adapter: marshal snapshot id=%d: %w", inner.ID, err)
	}

	path := a.pathFn(inner)
	if _, err := a.trash.MoveWithSnapshot(ctx, path, "pypi_file_drift", inner.ID, actor, snapBytes); err != nil {
		// Legacy empty marker (source missing): sidecar still landed,
		// row DELETE proceeds. Any other error is fatal — propagate.
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("pypi adapter: trash move id=%d path=%q: %w", inner.ID, path, err)
		}
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM pypi_files WHERE id = ?`, inner.ID); err != nil {
		return fmt.Errorf("pypi adapter: delete id=%d: %w", inner.ID, err)
	}
	return nil
}

// pypiRow wraps *metadata.PyPIFile with the Row interface required by
// the engine. Key uses D-12's {project_normalized, filename, digest}.
type pypiRow struct {
	inner *metadata.PyPIFile
}

func (r *pypiRow) Key() Key {
	return Key{A: r.inner.ProjectNormalized, B: r.inner.Filename, C: r.inner.Digest}
}

func (r *pypiRow) SampleFilename() string { return r.inner.Filename }
