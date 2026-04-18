package helm

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"

	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/protocol/regen"
	"github.com/dxc-internal/omnirepo/internal/storage"
)

// Mirror writes Helm charts that arrived via OCI push into the traditional
// <dataRoot>/repos/<project>/helm/<repo>/charts/ tree so `helm repo add` +
// `helm pull` (and `helm search repo`) can see them.
//
// Plan 07-04 S-03b: forward-only mirror. The reverse direction
// (traditional PUT → synthesised OCI manifest) is deferred to v1.2.
//
// Called from OCI manifestPut AFTER the OCI writer-tx commits. Failure
// MUST log-and-continue at the call site — the OCI push has already
// succeeded and its 201 MUST NOT regress. See Pitfall 1 in 07-RESEARCH.
type Mirror struct {
	db         *metadata.DB
	helmCharts *metadata.HelmChartsRepo
	repos      *metadata.ReposRepo
	pathStore  storage.PathStore
	coalescer  *regen.Registry
}

// NewMirror constructs a Mirror wired to the same deps the Helm handler uses.
// Every arg must be non-nil except coalescer (which may be nil in tests that
// do not exercise regen kicks; the mirror degrades to a pure write in that
// case).
func NewMirror(
	db *metadata.DB,
	helmCharts *metadata.HelmChartsRepo,
	repos *metadata.ReposRepo,
	pathStore storage.PathStore,
	coalescer *regen.Registry,
) *Mirror {
	return &Mirror{
		db:         db,
		helmCharts: helmCharts,
		repos:      repos,
		pathStore:  pathStore,
		coalescer:  coalescer,
	}
}

// chartFilenameRe defends against path-traversal via a malicious Chart.yaml
// Name or Version. Helm SDK chart loader already validates both, but the
// mirror sits behind an OCI wire surface where the manifest author could, in
// principle, upload a crafted tgz whose Chart.yaml bypasses loader
// validation. Defence in depth: reject anything that does not look like
// "<chart-name>-<version>.tgz" using Helm's own chart-name + SemVer character
// classes.
var chartFilenameRe = regexp.MustCompile(`^[a-z0-9._-]+-[0-9A-Za-z.+-]+\.tgz$`)

// MirrorToTraditional mirrors a Helm chart .tgz into the traditional
// PathStore layout and kicks the regen coalescer.
//
// Flow (mirrors internal/protocol/helm/put.go:putChart, minus the HTTP
// specifics):
//
//  1. Buffer the tgz reader into an in-memory buffer + sha256 hash + a
//     tmp file (helm.Parse requires a file path).
//  2. Call helm.Parse(tmpPath) to extract Chart.yaml (name, version, etc.).
//  3. Derive filename `<name>-<version>.tgz`; validate via chartFilenameRe.
//  4. Resolve (project, "helm", repo) → repo row.
//  5. pathStore.Put into storageKeyFor(project, repo, filename).
//  6. Writer tx: helm_charts upsert + FTS index refresh + metadata_state=dirty.
//  7. On tx failure: pathStore.Delete (HI-02 rollback pattern).
//  8. coalescer.Get(repoID).Kick() so index.yaml regenerates.
//
// The returned error is for the caller (OCI manifestPut post-tx hook) to
// log; this function NEVER writes to an HTTP response.
func (m *Mirror) MirrorToTraditional(ctx context.Context, projectName, repoName string, chartTgz io.Reader) error {
	if m == nil {
		return errors.New("helm mirror: nil receiver")
	}
	if chartTgz == nil {
		return errors.New("helm mirror: nil chartTgz reader")
	}

	// 1. Buffer + hash + tmp file. Charts are small (<5 MiB typical) so the
	// in-memory buffer is fine. The tmp file exists only because Parse
	// takes a path (Helm SDK chart loader opens the archive via
	// os.File).
	tmpDir, err := os.MkdirTemp("", "helm-mirror-*")
	if err != nil {
		return fmt.Errorf("helm mirror: mkdir tmp: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()
	tmpPath := filepath.Join(tmpDir, "chart.tgz")

	var buf bytes.Buffer
	hasher := sha256.New()
	size, err := io.Copy(io.MultiWriter(&buf, hasher), chartTgz)
	if err != nil {
		return fmt.Errorf("helm mirror: read tgz: %w", err)
	}
	if err := os.WriteFile(tmpPath, buf.Bytes(), 0o600); err != nil {
		return fmt.Errorf("helm mirror: write tmp: %w", err)
	}
	digest := "sha256:" + hex.EncodeToString(hasher.Sum(nil))

	// 2. Parse Chart.yaml via the existing helm.Parse helper.
	chartMeta, perr := Parse(tmpPath)
	if perr != nil {
		return fmt.Errorf("helm mirror: parse Chart.yaml: %w", perr)
	}

	// 3. Derive filename + validate shape.
	filename := fmt.Sprintf("%s-%s.tgz", chartMeta.Name, chartMeta.Version)
	if !chartFilenameRe.MatchString(filename) {
		return fmt.Errorf("helm mirror: unsafe filename %q", filename)
	}

	// 4. Resolve repo row.
	proj, err := m.lookupProjectID(ctx, projectName)
	if err != nil {
		return fmt.Errorf("helm mirror: resolve project %q: %w", projectName, err)
	}
	repo, err := m.repos.FindByTriple(ctx, proj, "helm", repoName)
	if err != nil {
		return fmt.Errorf("helm mirror: resolve repo %q/%q: %w", projectName, repoName, err)
	}
	if repo == nil {
		return fmt.Errorf("helm mirror: repo %q/%q not found", projectName, repoName)
	}

	// 5. PathStore.Put at the canonical storageKey.
	storageKey := storageKeyFor(projectName, repoName, filename)
	if _, err := m.pathStore.Put(ctx, storageKey, bytes.NewReader(buf.Bytes())); err != nil {
		return fmt.Errorf("helm mirror: path store put: %w", err)
	}

	// 6. Writer tx: helm_charts upsert + FTS refresh + metadata_state=dirty.
	if err := m.db.WriteTx(ctx, func(tx *sql.Tx) error {
		if _, err := m.helmCharts.Insert(ctx, tx, &metadata.HelmChart{
			RepoID:          repo.ID,
			Name:            chartMeta.Name,
			Version:         chartMeta.Version,
			AppVersion:      chartMeta.AppVersion,
			Description:     chartMeta.Description,
			KeywordsJSON:    chartMeta.KeywordsJSON(),
			MaintainersJSON: chartMeta.MaintainersJSON(),
			SizeBytes:       size,
			Digest:          digest,
			Filename:        filename,
		}); err != nil {
			return err
		}
		if err := metadata.IndexHelmDelete(ctx, tx, repo.ID,
			chartMeta.Name, chartMeta.Version, chartMeta.AppVersion); err != nil {
			return err
		}
		if err := metadata.IndexHelm(ctx, tx, repo.ID,
			chartMeta.Name, chartMeta.Version, chartMeta.AppVersion, chartMeta.Description); err != nil {
			return err
		}
		return m.repos.SetMetadataState(ctx, tx, repo.ID, metadata.MetadataStateDirty)
	}); err != nil {
		// 7. HI-02 rollback: tx failed AFTER pathStore.Put succeeded, so
		// remove the on-disk file to keep FS + DB consistent.
		_ = m.pathStore.Delete(ctx, storageKey)
		return fmt.Errorf("helm mirror: write tx: %w", err)
	}

	// 8. Kick the coalescer so index.yaml regenerates.
	if m.coalescer != nil {
		m.coalescer.Get(repo.ID).Kick()
	}
	slog.InfoContext(ctx, "helm.mirror.success",
		slog.String("project", projectName),
		slog.String("repo", repoName),
		slog.String("chart", chartMeta.Name),
		slog.String("version", chartMeta.Version),
		slog.String("digest", digest),
		slog.Int64("size", size),
	)
	return nil
}

// lookupProjectID resolves a project name to its id. A dedicated helper (vs.
// inlining) keeps the hot path readable and lets tests stub a narrower
// surface if needed.
func (m *Mirror) lookupProjectID(ctx context.Context, name string) (int64, error) {
	// The helm handler (and every protocol) resolves project lookups via
	// metadata.ProjectsRepo. The mirror does not have a direct handle on
	// it — pass (project, repo) names through FindByTriple's argument list
	// by looking the project id up ourselves. We use a single parameterised
	// query against the reader pool (same path ReposRepo.FindByTriple uses
	// under the hood) to avoid growing the Mirror constructor footprint.
	var id int64
	row := m.db.Reader.QueryRowContext(ctx,
		`SELECT id FROM projects WHERE name=? AND deleted_at IS NULL`, name)
	if err := row.Scan(&id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, fmt.Errorf("project %q not found", name)
		}
		return 0, err
	}
	return id, nil
}
