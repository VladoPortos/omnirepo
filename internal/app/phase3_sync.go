// Package app — Phase 03 Plan 06 SYNC-05 wiring.
//
// wireSync constructs the four per-protocol sync handlers (rpm, deb,
// pypi, helm) and registers them on the SyncPool under their respective
// kinds (rpm_sync, apt_sync, pypi_sync, helm_sync). Returns the
// SyncRESTAdapter the api.Mount call uses to expose POST
// /api/v1/projects/{name}/repos/{type}/{repo}/sync.
package app

import (
	"context"
	"net/http"
	"path/filepath"
	"time"

	"github.com/dxc-internal/omnirepo/internal/api"
	"github.com/dxc-internal/omnirepo/internal/audit"
	"github.com/dxc-internal/omnirepo/internal/config"
	"github.com/dxc-internal/omnirepo/internal/httpx"
	"github.com/dxc-internal/omnirepo/internal/jobs"
	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/protocol/deb"
	"github.com/dxc-internal/omnirepo/internal/protocol/helm"
	"github.com/dxc-internal/omnirepo/internal/protocol/helm/ociclient"
	"github.com/dxc-internal/omnirepo/internal/protocol/pypi"
	"github.com/dxc-internal/omnirepo/internal/protocol/regen"
	"github.com/dxc-internal/omnirepo/internal/protocol/rpm"
	"github.com/dxc-internal/omnirepo/internal/storage"
)

// syncDeps bundles the shared cross-protocol prerequisites.
type syncDeps struct {
	cfg          config.Config
	db           *metadata.DB
	auditLogger  audit.Logger
	creds        *metadata.UpstreamCredsRepo
	syncPool     *jobs.Pool
	syncHandlers jobs.Handlers
	rpmRegistry  *regen.Registry
	debRegistry  *regen.Registry
	pypiRegistry *regen.Registry
	helmRegistry *regen.Registry
}

// wireSync constructs and registers the four sync handlers + builds the
// SyncRESTAdapter caller passes into api.Mount(... SyncDeps: ...).
func (d syncDeps) wireSync() *api.SyncRESTAdapter {
	repoRoot := filepath.Join(d.cfg.DataRoot, "repos")
	pathStore := storage.NewPathStore(repoRoot)
	httpClient := &http.Client{Timeout: defaultSyncHTTPTimeout(d.cfg)}

	reposRepo := metadata.NewReposRepo(d.db)
	projectsRepo := metadata.NewProjectsRepo(d.db)
	scansRepo := metadata.NewScansRepo(d.db)

	// Phase 8 Plan 02 (M2.4–M2.7): shared SyncJobs repo threaded into all
	// four handlers for throttled byte-level / step-based progress emit.
	syncJobsRepo := metadata.NewSyncJobsRepo(d.db)

	rpmSync := rpm.NewSyncHandler(rpm.SyncDeps{
		DB: d.db, Path: pathStore,
		RPMPackages: metadata.NewRPMPackagesRepo(d.db),
		Repos:       reposRepo, Projects: projectsRepo,
		Creds: d.creds, Scans: scansRepo, Audit: d.auditLogger,
		Coalescer: d.rpmRegistry, HTTPClient: httpClient,
		RepoRoot: repoRoot, Cfg: d.cfg.Sync,
		SyncJobs: syncJobsRepo,
	})
	debSync := deb.NewSyncHandler(deb.SyncDeps{
		DB: d.db, Path: pathStore,
		DEBPackages: metadata.NewDEBPackagesRepo(d.db),
		AptSuites:   metadata.NewAptSuitesRepo(d.db),
		Repos:       reposRepo, Projects: projectsRepo,
		Creds: d.creds, Scans: scansRepo, Audit: d.auditLogger,
		Coalescer: d.debRegistry, HTTPClient: httpClient,
		RepoRoot: repoRoot, Cfg: d.cfg.Sync,
		SyncJobs: syncJobsRepo,
	})
	pypiSync := pypi.NewSyncHandler(pypi.SyncDeps{
		DB: d.db, Path: pathStore,
		PyPIFiles: metadata.NewPyPIFilesRepo(d.db),
		Repos:     reposRepo, Projects: projectsRepo,
		Creds: d.creds, Scans: scansRepo, Audit: d.auditLogger,
		Coalescer: d.pypiRegistry, HTTPClient: httpClient,
		RepoRoot: repoRoot, Cfg: d.cfg.Sync,
		SyncJobs: syncJobsRepo,
	})
	helmSync := helm.NewSyncHandler(helm.SyncDeps{
		DB: d.db, Path: pathStore,
		HelmCharts: metadata.NewHelmChartsRepo(d.db),
		Repos:      reposRepo, Projects: projectsRepo,
		Creds: d.creds, Scans: scansRepo, Audit: d.auditLogger,
		Coalescer: d.helmRegistry, HTTPClient: httpClient,
		RepoRoot: repoRoot, Cfg: d.cfg.Sync,
		SyncJobs: syncJobsRepo,
		// Plan 11-03: wire the OCI Helm client so fetchAndCommit can
		// branch on UpstreamEntry.Source == EntrySourceOCI. The shared
		// httpClient already carries TLS/proxy/timeout config.
		OCIClient: ociclient.New(httpClient),
		// Plan 11-03 (D-02): tag-rebound handling soft-deletes the prior
		// digest's on-disk file via Trash.Move with kind
		// "oci_tag_rebound" before inserting the replacement row.
		Trash: storage.NewTrash(filepath.Join(d.cfg.DataRoot, "trash")),
	})

	d.syncHandlers[rpm.SyncJobKind] = func(c context.Context, j *jobs.JobView) error {
		return rpmSync.Handle(c, j.Payload, j.ProjectID, j.RepoID, j.ID)
	}
	d.syncHandlers[deb.SyncJobKind] = func(c context.Context, j *jobs.JobView) error {
		return debSync.Handle(c, j.Payload, j.ProjectID, j.RepoID, j.ID)
	}
	d.syncHandlers[pypi.SyncJobKind] = func(c context.Context, j *jobs.JobView) error {
		return pypiSync.Handle(c, j.Payload, j.ProjectID, j.RepoID, j.ID)
	}
	d.syncHandlers[helm.SyncJobKind] = func(c context.Context, j *jobs.JobView) error {
		return helmSync.Handle(c, j.Payload, j.ProjectID, j.RepoID, j.ID)
	}

	return api.NewSyncRESTAdapter(httpx.SyncRESTDeps{
		DB:            d.db,
		Repos:         reposRepo,
		Projects:      projectsRepo,
		Members:       metadata.NewMembersRepo(d.db),
		SyncJobs:      metadata.NewSyncJobsRepo(d.db),
		UpstreamCreds: d.creds,
		Audit:         d.auditLogger,
		Kick:          d.syncPool.Kick,
	})
}

func defaultSyncHTTPTimeout(cfg config.Config) time.Duration {
	if cfg.Sync.UpstreamHTTPTimeout > 0 {
		return cfg.Sync.UpstreamHTTPTimeout
	}
	return 60 * time.Second
}
