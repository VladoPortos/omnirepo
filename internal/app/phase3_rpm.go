// Package app — Phase 3 Plan 04 RPM wiring. Constructs the RPM handler +
// per-repo regen coalescer registry and mounts /<project>/rpm/<repo>/...
// routes onto router.
package app

import (
	"context"
	"path/filepath"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/dxc-internal/omnirepo/internal/audit"
	"github.com/dxc-internal/omnirepo/internal/config"
	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/protocol/regen"
	"github.com/dxc-internal/omnirepo/internal/protocol/rpm"
	"github.com/dxc-internal/omnirepo/internal/storage"
)

// rpmDeps bundles the prerequisites the RPM handler + regen factory share.
type rpmDeps struct {
	cfg         config.Config
	db          *metadata.DB
	auditLogger audit.Logger
	signingKeys *metadata.SigningKeysRepo
	locks       storage.Locks
}

// wireRPM constructs the RPM handler + its per-repo regen coalescer registry
// and mounts the routes on router. Returns the registry so app.Run can drain
// it on shutdown.
func (d rpmDeps) wireRPM(router chi.Router) *regen.Registry {
	repoRoot := filepath.Join(d.cfg.DataRoot, "repos")

	reposRepo := metadata.NewReposRepo(d.db)
	rpmPackages := metadata.NewRPMPackagesRepo(d.db)
	projectsRepo := metadata.NewProjectsRepo(d.db)

	debounce := time.Duration(d.cfg.Regen.DebounceMs) * time.Millisecond
	maxWait := time.Duration(d.cfg.Regen.MaxWaitMs) * time.Millisecond

	registry := regen.NewRegistry(debounce, maxWait, func(repoID int64) regen.RegenFn {
		return rpm.RegenFor(rpm.RegenDeps{
			DB:          d.db,
			Repos:       reposRepo,
			Projects:    projectsRepo,
			RPMPackages: rpmPackages,
			SigningKeys: d.signingKeys,
			Audit:       d.auditLogger,
			Locks:       d.locks,
			RepoRoot:    repoRoot,
			RepoID:      repoID,
		})
	})

	pubKeyCache := rpm.NewPublicKeyCache(d.signingKeys)

	h := rpm.New(rpm.Deps{
		DB:             d.db,
		Users:          metadata.NewUsersRepo(d.db),
		APIKeys:        metadata.NewAPIKeysRepo(d.db),
		Sessions:       metadata.NewSessionsRepo(d.db),
		Repos:          reposRepo,
		Projects:       projectsRepo,
		Members:        metadata.NewMembersRepo(d.db),
		RPMPackages:    rpmPackages,
		SigningKeys:    d.signingKeys,
		Scans:          metadata.NewScansRepo(d.db),
		Coalescer:      registry,
		PublicKeyCache: pubKeyCache,
		Path:           storage.NewPathStore(repoRoot),
		Trash:          storage.NewTrash(filepath.Join(d.cfg.DataRoot, "trash")),
		Audit:          d.auditLogger,
		RepoRoot:       repoRoot,
	})
	h.Mount(router)
	return registry
}

// shutdownRPMRegistry drains the registry returned by wireRPM.
func shutdownRPMRegistry(ctx context.Context, reg *regen.Registry) {
	if reg == nil {
		return
	}
	_ = reg.ShutdownAll(ctx)
}
