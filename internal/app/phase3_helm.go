package app

import (
	"context"
	"path/filepath"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/dxc-internal/omnirepo/internal/audit"
	"github.com/dxc-internal/omnirepo/internal/config"
	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/protocol/helm"
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

// wireHelm constructs the Helm handler + its per-repo regen coalescer
// registry and mounts routes on router. The regen factory is wired to
// helm.RegenFor (Task 2) so uploads trigger index.yaml regeneration.
func (d helmDeps) wireHelm(router chi.Router) *regen.Registry {
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
		Path:         storage.NewPathStore(repoRoot),
		Trash:        storage.NewTrash(filepath.Join(d.cfg.DataRoot, "trash")),
		Audit:        d.auditLogger,
		SeverityGate: d.severity,
		RepoRoot:     repoRoot,
	})
	h.Mount(router)
	return registry
}

// shutdownHelmRegistry drains the coalescer registry returned by wireHelm.
// Called from app.Run during graceful shutdown.
func shutdownHelmRegistry(ctx context.Context, reg *regen.Registry) {
	if reg == nil {
		return
	}
	_ = reg.ShutdownAll(ctx)
}
