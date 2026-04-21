// Package app — Phase 3 Plan 05 DEB wiring. Constructs the DEB handler +
// per-repo regen coalescer registry and mounts /<project>/deb/<repo>/... routes.
package app

import (
	"context"
	"path/filepath"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/dxc-internal/omnirepo/internal/audit"
	"github.com/dxc-internal/omnirepo/internal/config"
	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/protocol/deb"
	"github.com/dxc-internal/omnirepo/internal/protocol/regen"
	"github.com/dxc-internal/omnirepo/internal/storage"
)

// debDeps bundles the prerequisites the DEB handler + regen factory share.
type debDeps struct {
	cfg         config.Config
	db          *metadata.DB
	auditLogger audit.Logger
	signingKeys *metadata.SigningKeysRepo
	severity    deb.SeverityGateFn
	locks       storage.Locks
}

// wireDEB constructs the DEB handler + its per-repo regen coalescer registry
// and mounts the routes on router. Returns the registry so app.Run can drain
// it on shutdown.
func (d debDeps) wireDEB(router chi.Router) *regen.Registry {
	repoRoot := filepath.Join(d.cfg.DataRoot, "repos")

	reposRepo := metadata.NewReposRepo(d.db)
	debPackages := metadata.NewDEBPackagesRepo(d.db)
	aptSuites := metadata.NewAptSuitesRepo(d.db)
	projectsRepo := metadata.NewProjectsRepo(d.db)

	debounce := time.Duration(d.cfg.Regen.DebounceMs) * time.Millisecond
	maxWait := time.Duration(d.cfg.Regen.MaxWaitMs) * time.Millisecond

	registry := regen.NewRegistry(debounce, maxWait, func(repoID int64) regen.RegenFn {
		return deb.RegenFor(deb.RegenDeps{
			DB:          d.db,
			Repos:       reposRepo,
			Projects:    projectsRepo,
			DEBPackages: debPackages,
			AptSuites:   aptSuites,
			SigningKeys: d.signingKeys,
			Audit:       d.auditLogger,
			Locks:       d.locks,
			RepoRoot:    repoRoot,
			RepoID:      repoID,
		})
	})

	pubKeyCache := deb.NewPublicKeyCache(d.signingKeys)

	h := deb.New(deb.Deps{
		DB:             d.db,
		Users:          metadata.NewUsersRepo(d.db),
		APIKeys:        metadata.NewAPIKeysRepo(d.db),
		Sessions:       metadata.NewSessionsRepo(d.db),
		Repos:          reposRepo,
		Projects:       projectsRepo,
		Members:        metadata.NewMembersRepo(d.db),
		DEBPackages:    debPackages,
		AptSuites:      aptSuites,
		SigningKeys:    d.signingKeys,
		Scans:          metadata.NewScansRepo(d.db),
		Coalescer:      registry,
		PublicKeyCache: pubKeyCache,
		Path:           storage.NewPathStore(repoRoot),
		Trash:          storage.NewTrash(filepath.Join(d.cfg.DataRoot, "trash")),
		Audit:          d.auditLogger,
		SeverityGate:   d.severity,
		RepoRoot:       repoRoot,
	})
	h.Mount(router)
	return registry
}

// shutdownDEBRegistry drains the registry returned by wireDEB.
func shutdownDEBRegistry(ctx context.Context, reg *regen.Registry) {
	if reg == nil {
		return
	}
	_ = reg.ShutdownAll(ctx)
}
