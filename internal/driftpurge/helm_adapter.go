// helmAdapter covers BOTH HTTP-helm and OCI-helm mirror paths per D-14.
// Both write helm_charts rows with the same (name, version) unique key,
// and both produce UpstreamEntry values at the collectFn boundary in
// helm/sync_handler.go — the caller builds []Key identically from the
// already-collected entries slice regardless of source. One adapter,
// both paths.

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

// HelmPathFn resolves the on-disk blob path for a helm_charts row.
type HelmPathFn func(row *metadata.HelmChart) string

// helmAdapter is the DriftAdapter implementation for Helm mirror repos.
// Drift key per D-12: {name, version} (C is unused; helm has only a
// 2-field key — both HTTP index.yaml entries and OCI catalog entries
// project to the same shape).
type helmAdapter struct {
	upstream []Key
	charts   *metadata.HelmChartsRepo
	trash    storage.Trash
	pathFn   HelmPathFn
}

// NewHelmAdapter constructs a DriftAdapter for Helm mirror repos.
// upstreamKeys MUST be built by the caller from the already-collected
// []UpstreamEntry slice in helm/sync_handler.Handle (HTTP and OCI
// produce the same shape per D-14). Caller projection:
// Key{A: ent.Name, B: ent.Version, C: ""}.
func NewHelmAdapter(
	upstreamKeys []Key,
	charts *metadata.HelmChartsRepo,
	trash storage.Trash,
	pathFn HelmPathFn,
) DriftAdapter {
	return &helmAdapter{
		upstream: upstreamKeys,
		charts:   charts,
		trash:    trash,
		pathFn:   pathFn,
	}
}

func (a *helmAdapter) Protocol() string    { return "helm" }
func (a *helmAdapter) TrashKind() string   { return "helm_chart_drift" }
func (a *helmAdapter) UpstreamKeys() []Key { return a.upstream }

func (a *helmAdapter) LocalRows(ctx context.Context, _ *sql.Tx, repoID int64) ([]Row, error) {
	rows, err := a.charts.ListByRepo(ctx, repoID)
	if err != nil {
		return nil, err
	}
	out := make([]Row, len(rows))
	for i := range rows {
		out[i] = &helmRow{inner: &rows[i]}
	}
	return out, nil
}

func (a *helmAdapter) Purge(ctx context.Context, tx *sql.Tx, row Row, actor string) error {
	pr, ok := row.(*helmRow)
	if !ok {
		return fmt.Errorf("helm adapter: unexpected row type %T", row)
	}
	inner := pr.inner

	snap := map[string]any{
		"repo_id":          inner.RepoID,
		"name":             inner.Name,
		"version":          inner.Version,
		"app_version":      inner.AppVersion,
		"description":      inner.Description,
		"keywords_json":    inner.KeywordsJSON,
		"maintainers_json": inner.MaintainersJSON,
		"size_bytes":       inner.SizeBytes,
		"digest":           inner.Digest,
		"filename":         inner.Filename,
	}
	snapBytes, err := json.Marshal(snap)
	if err != nil {
		return fmt.Errorf("helm adapter: marshal snapshot id=%d: %w", inner.ID, err)
	}

	path := a.pathFn(inner)
	if _, err := a.trash.MoveWithSnapshot(ctx, path, "helm_chart_drift", inner.ID, actor, snapBytes); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("helm adapter: trash move id=%d path=%q: %w", inner.ID, path, err)
		}
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM helm_charts WHERE id = ?`, inner.ID); err != nil {
		return fmt.Errorf("helm adapter: delete id=%d: %w", inner.ID, err)
	}
	return nil
}

// helmRow wraps *metadata.HelmChart. Key per D-12 is {name, version, ""}.
type helmRow struct {
	inner *metadata.HelmChart
}

func (r *helmRow) Key() Key {
	return Key{A: r.inner.Name, B: r.inner.Version, C: ""}
}

// SampleFilename uses the stored Filename when set, falling back to
// the canonical helm tarball name.
func (r *helmRow) SampleFilename() string {
	if r.inner.Filename != "" {
		return r.inner.Filename
	}
	return fmt.Sprintf("%s-%s.tgz", r.inner.Name, r.inner.Version)
}
