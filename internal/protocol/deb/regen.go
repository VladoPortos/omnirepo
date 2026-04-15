// Package deb — regen factory stub. Task 3 in Plan 03-05 fills in the real
// staging-dir swap pipeline.
package deb

import (
	"context"

	"github.com/dxc-internal/omnirepo/internal/audit"
	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/protocol/regen"
	"github.com/dxc-internal/omnirepo/internal/storage"
)

// RegenDeps bundles the state a RegenFn needs to rebuild the dists/ tree on
// disk. Populated by app.phase3_deb.wireDEB.
type RegenDeps struct {
	DB          *metadata.DB
	Repos       *metadata.ReposRepo
	Projects    *metadata.ProjectsRepo
	DEBPackages *metadata.DEBPackagesRepo
	AptSuites   *metadata.AptSuitesRepo
	SigningKeys *metadata.SigningKeysRepo
	Audit       audit.Logger
	Locks       storage.Locks
	RepoRoot    string
	RepoID      int64
}

// RegenFor returns a regen.RegenFn for the given deps.
//
// Task 2 stub: no-op (returns nil). Task 3 replaces this with the real
// staging-dir swap pipeline (Packages, Release, InRelease, Release.gpg).
func RegenFor(d RegenDeps) regen.RegenFn {
	return func(ctx context.Context) error { return nil }
}
