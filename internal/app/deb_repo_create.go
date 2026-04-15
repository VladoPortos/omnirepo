// Package app — DEB repo-create hook (Phase 3 Plan 05, D-23).
//
// When a `type=deb` repo is created, this hook seeds the default apt_suites
// matrix inside the SAME writer tx as the repos INSERT. Paired with
// CreateRPMRepoHook (which also handles `type=deb` for signing-key
// generation), the composition is atomic: a failure at any step rolls back
// the repos row (T-03-05 atomicity).
//
// Default matrix per D-23: suites=["stable"], components=["main"],
// architectures=["amd64", "arm64", "all"]. Operators mutate via a future
// PATCH /{project}/deb/{repo}/suites endpoint (see handler.go).
package app

import (
	"context"
	"database/sql"
	"errors"

	"github.com/dxc-internal/omnirepo/internal/metadata"
)

// DefaultDEBSuites is the seed matrix inserted by CreateDEBRepoHook. Exposed
// so tests can assert the exact rows land.
var DefaultDEBSuites = []metadata.AptSuite{
	{Suite: "stable", Component: "main", Architecture: "amd64"},
	{Suite: "stable", Component: "main", Architecture: "arm64"},
	{Suite: "stable", Component: "main", Architecture: "all"},
}

// CreateDEBRepoHook inserts the default apt_suites rows for a newly-created
// deb repo. Must run INSIDE the repo-create writer tx so the rows commit
// atomically with the repos INSERT (and with the signing_keys row inserted
// by CreateRPMRepoHook, which composes alongside this hook in app.Run).
//
// repoType outside {"deb"} → no-op return (nil).
func CreateDEBRepoHook(
	ctx context.Context,
	tx *sql.Tx,
	repoID int64,
	repoType string,
	aptSuites *metadata.AptSuitesRepo,
) error {
	if repoType != "deb" {
		return nil
	}
	if aptSuites == nil {
		return errors.New("apt_suites repo not configured")
	}
	rows := make([]metadata.AptSuite, len(DefaultDEBSuites))
	for i, r := range DefaultDEBSuites {
		r.RepoID = repoID
		rows[i] = r
	}
	return aptSuites.InsertBatch(ctx, tx, repoID, rows)
}
