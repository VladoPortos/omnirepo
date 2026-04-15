// Package rpm — regen factory. Task 1 ships a no-op factory body so the
// app wiring compiles; Task 3 lands the real regen pipeline.
package rpm

import (
	"context"

	"github.com/dxc-internal/omnirepo/internal/audit"
	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/protocol/regen"
	"github.com/dxc-internal/omnirepo/internal/storage"
)

// RegenDeps bundles the state a RegenFn needs to rebuild
// <repoRoot>/<project>/rpm/<repo>/repodata/* on disk under the per-repo
// mutex (D-09, D-10, D-11). Populated by app.phase3_rpm.wireRPM.
type RegenDeps struct {
	DB          *metadata.DB
	Repos       *metadata.ReposRepo
	Projects    *metadata.ProjectsRepo
	RPMPackages *metadata.RPMPackagesRepo
	SigningKeys *metadata.SigningKeysRepo
	Audit       audit.Logger
	Locks       storage.Locks
	RepoRoot    string
	RepoID      int64
}

// RegenFor returns a regen.RegenFn. Task 3 implements the real body
// (primary/filelists/other.xml.gz + repomd.xml + repomd.xml.asc); the Task 2
// stub is a no-op so the app wiring compiles end-to-end.
func RegenFor(d RegenDeps) regen.RegenFn {
	return func(ctx context.Context) error {
		// Task 3 will fill this in. Return nil so the coalescer doesn't
		// log a spurious failure during Task 2 integration tests.
		return nil
	}
}
