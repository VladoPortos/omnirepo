package helm

import (
	"context"

	"github.com/dxc-internal/omnirepo/internal/audit"
	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/protocol/regen"
	"github.com/dxc-internal/omnirepo/internal/storage"
)

// RegenDeps bundles the state a RegenFn needs to rebuild <repoRoot>/index.yaml
// from the .tgz files currently on disk. Populated by app.phase3_helm.wireHelm.
//
// NOTE: the actual regen body (helm SDK IndexDirectory + atomic rename +
// metadata_state transitions + audit) lands in Plan 03-02 Task 2. Task 1
// provides this seam so the coalescer registry can be wired at construction
// time; the stub RegenFn is a no-op so compilation stays clean.
type RegenDeps struct {
	DB       *metadata.DB
	Repos    *metadata.ReposRepo
	Projects *metadata.ProjectsRepo
	Audit    audit.Logger
	Locks    storage.Locks
	RepoRoot string
	RepoID   int64
	// BaseURL is the chart-URL prefix written into index.yaml entries'
	// `urls:` field (e.g. "/<project>/helm/<repo>/charts"). When empty the
	// Task 2 implementation defaults to a relative "charts" prefix.
	BaseURL string
}

// RegenFor returns a regen.RegenFn that rebuilds <repoRoot>/index.yaml from
// disk under the per-repo mutex. The returned closure is suitable for
// passing directly to regen.Registry's factory callback.
//
// Plan 03-02 Task 1 ships this as a no-op stub — the actual implementation
// (helm.sh/helm/v3/pkg/repo.IndexDirectory + atomic rename + metadata_state
// transitions) lands in Plan 03-02 Task 2, along with the integration tests.
// This separation lets Task 1 wire the handler + coalescer registry end-to-end
// without waiting on the regen body.
func RegenFor(d RegenDeps) regen.RegenFn {
	return func(ctx context.Context) error {
		// Task 2 replaces this body with the real IndexDirectory-based rebuild.
		return nil
	}
}
