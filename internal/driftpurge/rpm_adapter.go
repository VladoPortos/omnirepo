package driftpurge

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/vladoportos/omnirepo/internal/metadata"
	"github.com/vladoportos/omnirepo/internal/storage"
)

// RPMPathFn resolves the on-disk blob path for an RPM package row.
// The caller (rpm/sync_handler.go) binds it from the
// live SyncDeps so the adapter stays free of path-construction.
type RPMPathFn func(row *metadata.RPMPackage) string

// rpmAdapter is the DriftAdapter implementation for RPM mirror repos.
// Drift key: {name, version, arch}. This is intentionally
// coarser than the DB UNIQUE (name, epoch, version, release, arch) —
// upstream repomd.xml does not always spell epoch / release identically
// across rebuilds.
type rpmAdapter struct {
	upstream []Key
	pkgs     *metadata.RPMPackagesRepo
	trash    storage.Trash
	pathFn   RPMPathFn
}

// NewRPMAdapter constructs a DriftAdapter for RPM mirror repos.
// upstreamKeys MUST be built by the caller from the parsed primary.xml
// entries already in memory at the end of rpm/sync_handler.Handle.
// Caller projection: Key{A: name, B: version, C: arch}.
func NewRPMAdapter(
	upstreamKeys []Key,
	pkgs *metadata.RPMPackagesRepo,
	trash storage.Trash,
	pathFn RPMPathFn,
) DriftAdapter {
	return &rpmAdapter{
		upstream: upstreamKeys,
		pkgs:     pkgs,
		trash:    trash,
		pathFn:   pathFn,
	}
}

func (a *rpmAdapter) Protocol() string    { return "rpm" }
func (a *rpmAdapter) TrashKind() string   { return "rpm_package_drift" }
func (a *rpmAdapter) UpstreamKeys() []Key { return a.upstream }

func (a *rpmAdapter) LocalRows(ctx context.Context, _ *sql.Tx, repoID int64) ([]Row, error) {
	rows, err := a.pkgs.ListByRepo(ctx, repoID)
	if err != nil {
		return nil, err
	}
	out := make([]Row, len(rows))
	for i := range rows {
		out[i] = &rpmRow{inner: &rows[i]}
	}
	return out, nil
}

func (a *rpmAdapter) Purge(ctx context.Context, tx *sql.Tx, row Row, actor string) (PendingMove, error) {
	pr, ok := row.(*rpmRow)
	if !ok {
		return PendingMove{}, fmt.Errorf("rpm adapter: unexpected row type %T", row)
	}
	inner := pr.inner

	snap := map[string]any{
		"repo_id":     inner.RepoID,
		"name":        inner.Name,
		"epoch":       inner.Epoch,
		"version":     inner.Version,
		"release":     inner.Release,
		"arch":        inner.Arch,
		"summary":     inner.Summary,
		"description": inner.Description,
		"license":     inner.License,
		"url":         inner.URL,
		"source_rpm":  inner.SourceRPM,
		"size_bytes":  inner.SizeBytes,
		"digest":      inner.Digest,
		"filename":    inner.Filename,
	}
	return purgeRow(ctx, tx, a.trash, "rpm adapter",
		`DELETE FROM rpm_packages WHERE id = ?`, "rpm_package_drift",
		inner.ID, snap, a.pathFn(inner), actor)
}

// rpmRow wraps *metadata.RPMPackage. Key is {name, version, arch}.
type rpmRow struct {
	inner *metadata.RPMPackage
}

func (r *rpmRow) Key() Key {
	return Key{A: r.inner.Name, B: r.inner.Version, C: r.inner.Arch}
}

// SampleFilename uses the canonical filename column when populated, falling
// back to NEVRA for rows seeded without one.
func (r *rpmRow) SampleFilename() string {
	if r.inner.Filename != "" {
		return r.inner.Filename
	}
	return fmt.Sprintf("%s-%s-%s.%s.rpm", r.inner.Name, r.inner.Version, r.inner.Release, r.inner.Arch)
}
