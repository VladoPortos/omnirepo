package driftpurge

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/vladoportos/omnirepo/internal/metadata"
	"github.com/vladoportos/omnirepo/internal/storage"
)

// PyPIPathFn resolves the on-disk blob path for a PyPI file row. The
// caller (pypi/sync_handler.go) binds it from the live
// SyncDeps so the adapter can stay free of path-construction knowledge.
type PyPIPathFn func(row *metadata.PyPIFile) string

// pypiAdapter is the DriftAdapter implementation for PyPI mirror repos.
// Drift key: {project_normalized, filename, digest}.
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
	// runs AFTER the partial-sync commits, so the reader sees the
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

func (a *pypiAdapter) Purge(ctx context.Context, tx *sql.Tx, row Row, actor string) (PendingMove, error) {
	pr, ok := row.(*pypiRow)
	if !ok {
		return PendingMove{}, fmt.Errorf("pypi adapter: unexpected row type %T", row)
	}
	inner := pr.inner

	// Verbatim column map (excluding id and uploaded_at — Restore's
	// UPSERT regenerates uploaded_at via strftime).
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
	return purgeRow(ctx, tx, a.trash, "pypi adapter",
		`DELETE FROM pypi_files WHERE id = ?`, "pypi_file_drift",
		inner.ID, snap, a.pathFn(inner), actor)
}

// pypiRow wraps *metadata.PyPIFile with the Row interface required by
// the engine. Key uses {project_normalized, filename, digest}.
type pypiRow struct {
	inner *metadata.PyPIFile
}

func (r *pypiRow) Key() Key {
	return Key{A: r.inner.ProjectNormalized, B: r.inner.Filename, C: r.inner.Digest}
}

func (r *pypiRow) SampleFilename() string { return r.inner.Filename }
