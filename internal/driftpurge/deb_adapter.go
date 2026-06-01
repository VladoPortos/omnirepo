package driftpurge

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/vladoportos/omnirepo/internal/metadata"
	"github.com/vladoportos/omnirepo/internal/storage"
)

// DEBPathFn resolves the on-disk blob path for a DEB package row.
type DEBPathFn func(row *metadata.DEBPackage) string

// debSuiteRef carries the (suite, component) projection a DEB row needs
// to construct its drift key. Architecture lives directly on the
// DEBPackage row (per-suite rows in apt_suites are arch-specific, but
// deb_packages.architecture is authoritative).
type debSuiteRef struct {
	Suite     string
	Component string
}

// debAdapter is the DriftAdapter implementation for APT mirror repos.
// Drift key: {name, version, arch, component, suite} flattened
// into the 3-slot Key as Key{A: name+"|"+component+"|"+suite, B: version,
// C: arch}. The caller (deb/sync_handler.go) MUST project
// upstream entries into the same 3-slot shape.
//
// Pitfall: deb_packages stores suite_id (FK to apt_suites);
// upstream Packages files key on (suite, component, arch). The adapter
// resolves the int suite_id to its (suite, component) string pair once
// per LocalRows call so the engine compares the same key space upstream
// and locally.
type debAdapter struct {
	upstream     []Key
	syncedSuites map[string]struct{}
	pkgs         *metadata.DEBPackagesRepo
	suites       *metadata.AptSuitesRepo
	trash        storage.Trash
	pathFn       DEBPathFn
}

// NewDEBAdapter constructs a DriftAdapter for APT mirror repos.
// The caller (sync_handler.Handle tail) MUST build upstreamKeys with
// the same 3-slot projection: Key{A: name+"|"+component+"|"+suite,
// B: version, C: arch}.
//
// syncedSuites scopes drift detection to ONLY the suites this sync actually
// processed. upstreamKeys is built from those suites alone, so without
// this scope every package in a suite the job did not touch would have no
// matching upstream key and be wrongly classified as drift — silently purging
// unrelated suites. An empty/nil set matches nothing (fail-safe: no purge).
func NewDEBAdapter(
	upstreamKeys []Key,
	syncedSuites []string,
	pkgs *metadata.DEBPackagesRepo,
	suites *metadata.AptSuitesRepo,
	trash storage.Trash,
	pathFn DEBPathFn,
) DriftAdapter {
	scope := make(map[string]struct{}, len(syncedSuites))
	for _, s := range syncedSuites {
		if s != "" {
			scope[s] = struct{}{}
		}
	}
	return &debAdapter{
		upstream:     upstreamKeys,
		syncedSuites: scope,
		pkgs:         pkgs,
		suites:       suites,
		trash:        trash,
		pathFn:       pathFn,
	}
}

func (a *debAdapter) Protocol() string    { return "deb" }
func (a *debAdapter) TrashKind() string   { return "deb_package_drift" }
func (a *debAdapter) UpstreamKeys() []Key { return a.upstream }

func (a *debAdapter) LocalRows(ctx context.Context, _ *sql.Tx, repoID int64) ([]Row, error) {
	suites, err := a.suites.ListByRepo(ctx, repoID)
	if err != nil {
		return nil, fmt.Errorf("deb adapter: list suites: %w", err)
	}
	// apt_suites stores (suite, component, architecture) — per-arch rows.
	// Multiple arches share the same (suite, component) pair; we pick the
	// first observed (suite, component) for each suite_id since the (suite,
	// component) projection drives the drift key, and per-arch divergence
	// is impossible (one apt_suites row -> one suite_id).
	suiteRefByID := make(map[int64]debSuiteRef, len(suites))
	for _, s := range suites {
		suiteRefByID[s.ID] = debSuiteRef{Suite: s.Suite, Component: s.Component}
	}

	rows, err := a.pkgs.ListByRepo(ctx, repoID)
	if err != nil {
		return nil, err
	}
	out := make([]Row, 0, len(rows))
	for i := range rows {
		ref := suiteRefByID[rows[i].SuiteID]
		// Only rows in a suite that was actually synced this run are drift
		// candidates. Rows in other suites were never compared against upstream;
		// treating them as drift would wrongly purge unrelated suites. An empty
		// synced-suite set matches nothing (fail-safe: never purge).
		if _, ok := a.syncedSuites[ref.Suite]; !ok {
			continue
		}
		out = append(out, &debRow{inner: &rows[i], suite: ref.Suite, component: ref.Component})
	}
	return out, nil
}

func (a *debAdapter) Purge(ctx context.Context, tx *sql.Tx, row Row, actor string) error {
	pr, ok := row.(*debRow)
	if !ok {
		return fmt.Errorf("deb adapter: unexpected row type %T", row)
	}
	inner := pr.inner

	snap := map[string]any{
		"repo_id":           inner.RepoID,
		"suite_id":          inner.SuiteID,
		"package":           inner.Package,
		"version":           inner.Version,
		"architecture":      inner.Architecture,
		"maintainer":        inner.Maintainer,
		"section":           inner.Section,
		"priority":          inner.Priority,
		"depends":           inner.Depends,
		"description":       inner.Description,
		"size_bytes":        inner.SizeBytes,
		"digest":            inner.Digest,
		"filename":          inner.Filename,
		"storage_pool_path": inner.StoragePoolPath,
	}
	snapBytes, err := json.Marshal(snap)
	if err != nil {
		return fmt.Errorf("deb adapter: marshal snapshot id=%d: %w", inner.ID, err)
	}

	// DELETE row first (in tx), then move file
	// to trash. See pypi_adapter.go for the rationale.
	if _, err := tx.ExecContext(ctx, `DELETE FROM deb_packages WHERE id = ?`, inner.ID); err != nil {
		return fmt.Errorf("deb adapter: delete id=%d: %w", inner.ID, err)
	}

	path := a.pathFn(inner)
	if _, err := a.trash.MoveWithSnapshot(ctx, path, "deb_package_drift", inner.ID, actor, snapBytes); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("deb adapter: trash move id=%d path=%q: %w", inner.ID, path, err)
		}
	}
	return nil
}

// debRow wraps *metadata.DEBPackage with the resolved (suite, component)
// strings looked up by suite_id once per LocalRows call.
type debRow struct {
	inner     *metadata.DEBPackage
	suite     string
	component string
}

func (r *debRow) Key() Key {
	// 5-field tuple flattened into 3 string slots. Caller projects upstream identically.
	// Empty suite/component (suite_id missing from apt_suites — should
	// not happen but we tolerate it) collapses to "name||" / "name||x"
	// shapes which still compare deterministically against an
	// identically-projected upstream entry.
	a := r.inner.Package + "|" + r.component + "|" + r.suite
	return Key{A: a, B: r.inner.Version, C: r.inner.Architecture}
}

func (r *debRow) SampleFilename() string {
	if r.inner.Filename != "" {
		return r.inner.Filename
	}
	return fmt.Sprintf("%s_%s_%s.deb", r.inner.Package, r.inner.Version, r.inner.Architecture)
}
