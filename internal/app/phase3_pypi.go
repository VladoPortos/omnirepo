package app

import (
	"context"
	"path/filepath"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/dxc-internal/omnirepo/internal/audit"
	"github.com/dxc-internal/omnirepo/internal/config"
	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/protocol/pypi"
	"github.com/dxc-internal/omnirepo/internal/protocol/regen"
	"github.com/dxc-internal/omnirepo/internal/storage"
)

// pypiDeps bundles the bits app.Run already materialized that the PyPI
// handler + regen factory need.
type pypiDeps struct {
	cfg         config.Config
	db          *metadata.DB
	auditLogger audit.Logger
	severity    pypi.SeverityGateFn
	locks       storage.Locks
}

// wirePyPI constructs the PyPI handler + its per-repo regen coalescer
// registry and mounts /<project>/pypi/<repo>/... routes on router. Returns
// the registry so app.Run can drain it during graceful shutdown.
func (d pypiDeps) wirePyPI(router chi.Router) *regen.Registry {
	repoRoot := filepath.Join(d.cfg.DataRoot, "repos")

	reposRepo := metadata.NewReposRepo(d.db)
	pypiFiles := metadata.NewPyPIFilesRepo(d.db)
	projectsRepo := metadata.NewProjectsRepo(d.db)

	debounce := time.Duration(d.cfg.Regen.DebounceMs) * time.Millisecond
	maxWait := time.Duration(d.cfg.Regen.MaxWaitMs) * time.Millisecond

	registry := regen.NewRegistry(debounce, maxWait, func(repoID int64) regen.RegenFn {
		return pypi.RegenFor(pypi.RegenDeps{
			DB:        d.db,
			Repos:     reposRepo,
			Projects:  projectsRepo,
			PyPIFiles: pypiFiles,
			Audit:     d.auditLogger,
			Locks:     d.locks,
			RepoRoot:  repoRoot,
			RepoID:    repoID,
		})
	})

	h := pypi.New(pypi.Deps{
		DB:           d.db,
		Users:        metadata.NewUsersRepo(d.db),
		APIKeys:      metadata.NewAPIKeysRepo(d.db),
		Sessions:     metadata.NewSessionsRepo(d.db),
		Repos:        reposRepo,
		Projects:     projectsRepo,
		Members:      metadata.NewMembersRepo(d.db),
		PyPIFiles:    pypiFiles,
		Scans:        metadata.NewScansRepo(d.db),
		Coalescer:    registry,
		PEP694:       pypi.NewPEP694Sessions(time.Hour),
		Path:         storage.NewPathStore(repoRoot),
		Trash:        storage.NewTrash(filepath.Join(d.cfg.DataRoot, "trash")),
		Audit:        d.auditLogger,
		SeverityGate: d.severity,
		RepoRoot:     repoRoot,
	})
	h.Mount(router)
	return registry
}

// shutdownPyPIRegistry drains the coalescer registry returned by wirePyPI.
func shutdownPyPIRegistry(ctx context.Context, reg *regen.Registry) {
	if reg == nil {
		return
	}
	_ = reg.ShutdownAll(ctx)
}
