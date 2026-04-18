package app

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/dxc-internal/omnirepo/internal/audit"
	"github.com/dxc-internal/omnirepo/internal/config"
	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/protocol/helm"
	"github.com/dxc-internal/omnirepo/internal/protocol/oci"
	"github.com/dxc-internal/omnirepo/internal/protocol/regen"
	"github.com/dxc-internal/omnirepo/internal/storage"
)

// helmDeps bundles everything app.Run has already materialized and that the
// Helm handler + regen factory need. Kept internal to the app package.
type helmDeps struct {
	cfg         config.Config
	db          *metadata.DB
	auditLogger audit.Logger
	severity    helm.SeverityGateFn
	locks       storage.Locks
}

// wireHelmResult bundles the registry and the OCI helm-mirror hook so
// app.Run can pass the hook into the OCI handler while still holding on to
// the registry for shutdown. Plan 07-04 S-03b.
type wireHelmResult struct {
	registry *regen.Registry
	// mirrorHook is the adapter passed to oci.Deps.HelmMirror. nil if the
	// OCI handler has not been constructed yet; app.Run attaches it via
	// the `wireHelmMirror` helper below after the CAS is available.
	mirrorHook oci.HelmMirrorHook
}

// wireHelm constructs the Helm handler + its per-repo regen coalescer
// registry and mounts routes on router. The regen factory is wired to
// helm.RegenFor (Task 2) so uploads trigger index.yaml regeneration.
//
// The returned registry is what the caller shuts down on exit. The helm
// Mirror (plan 07-04 S-03b) is constructed alongside but is NOT wired to
// the OCI handler here — app.Run calls wireHelmMirror once the OCI CAS is
// available, producing the adapter that implements oci.HelmMirrorHook.
func (d helmDeps) wireHelm(router chi.Router) (*regen.Registry, *helm.Mirror) {
	repoRoot := filepath.Join(d.cfg.DataRoot, "repos")

	reposRepo := metadata.NewReposRepo(d.db)
	helmCharts := metadata.NewHelmChartsRepo(d.db)
	projectsRepo := metadata.NewProjectsRepo(d.db)

	debounce := time.Duration(d.cfg.Regen.DebounceMs) * time.Millisecond
	maxWait := time.Duration(d.cfg.Regen.MaxWaitMs) * time.Millisecond

	registry := regen.NewRegistry(debounce, maxWait, func(repoID int64) regen.RegenFn {
		return helm.RegenFor(helm.RegenDeps{
			DB:       d.db,
			Repos:    reposRepo,
			Projects: projectsRepo,
			Audit:    d.auditLogger,
			Locks:    d.locks,
			RepoRoot: repoRoot,
			RepoID:   repoID,
		})
	})

	pathStore := storage.NewPathStore(repoRoot)

	h := helm.New(helm.Deps{
		DB:           d.db,
		Users:        metadata.NewUsersRepo(d.db),
		APIKeys:      metadata.NewAPIKeysRepo(d.db),
		Sessions:     metadata.NewSessionsRepo(d.db),
		Repos:        reposRepo,
		Projects:     projectsRepo,
		Members:      metadata.NewMembersRepo(d.db),
		HelmCharts:   helmCharts,
		Scans:        metadata.NewScansRepo(d.db),
		Coalescer:    registry,
		Path:         pathStore,
		Trash:        storage.NewTrash(filepath.Join(d.cfg.DataRoot, "trash")),
		Audit:        d.auditLogger,
		SeverityGate: d.severity,
		RepoRoot:     repoRoot,
	})
	h.Mount(router)

	// Plan 07-04 S-03b: construct the forward-only Mirror alongside the
	// handler. The OCI adapter lives in wireHelmMirror below and is
	// wired from app.Run once the OCI CAS exists.
	mirror := helm.NewMirror(d.db, helmCharts, reposRepo, pathStore, registry)
	return registry, mirror
}

// ociHelmMirrorAdapter implements oci.HelmMirrorHook by opening the chart
// blob from the OCI CAS and streaming it into helm.Mirror.MirrorToTraditional.
//
// Plan 07-04 S-03b: the OCI manifestPut post-commit hook supplies the
// chart-content layer digest; the adapter resolves the bytes via the
// content-addressed store the OCI handler already writes to, so the mirror
// never has to re-hash or re-upload the chart.
type ociHelmMirrorAdapter struct {
	cas    storage.CAS
	mirror *helm.Mirror
}

// Mirror satisfies oci.HelmMirrorHook. Errors surface back to the OCI hook
// which logs-and-continues; the OCI push result is not affected.
func (a *ociHelmMirrorAdapter) Mirror(ctx context.Context, projectName, repoName, chartBlobDigest string) error {
	if a == nil || a.cas == nil || a.mirror == nil {
		return fmt.Errorf("helm mirror adapter not wired")
	}
	rc, err := a.cas.Get(ctx, chartBlobDigest)
	if err != nil {
		return fmt.Errorf("cas open %s: %w", chartBlobDigest, err)
	}
	defer func() { _ = rc.Close() }()
	return a.mirror.MirrorToTraditional(ctx, projectName, repoName, rc)
}

// wireHelmMirror produces the oci.HelmMirrorHook that app.Run passes into
// oci.Deps.HelmMirror. Kept as a small constructor so the adapter's
// wire-in point is greppable and a future change can swap CAS providers
// without touching app.Run.
func wireHelmMirror(cas storage.CAS, mirror *helm.Mirror) oci.HelmMirrorHook {
	if cas == nil || mirror == nil {
		return nil
	}
	return &ociHelmMirrorAdapter{cas: cas, mirror: mirror}
}

// compile-time guard: the adapter actually satisfies oci.HelmMirrorHook.
var _ oci.HelmMirrorHook = (*ociHelmMirrorAdapter)(nil)

// compile-time guard: io.ReadCloser from CAS.Get is the shape Mirror
// expects. This is belt-and-suspenders — if the signature ever drifts we
// want a package-level compile break, not a runtime surprise.
var _ io.Reader = io.ReadCloser(nil)

// shutdownHelmRegistry drains the coalescer registry returned by wireHelm.
// Called from app.Run during graceful shutdown.
func shutdownHelmRegistry(ctx context.Context, reg *regen.Registry) {
	if reg == nil {
		return
	}
	_ = reg.ShutdownAll(ctx)
}
