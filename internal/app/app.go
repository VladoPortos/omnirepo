package app

import (
	"context"
	cryptorand "crypto/rand"
	"database/sql"
	stdtls "crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/dxc-internal/omnirepo/internal/api"
	"github.com/dxc-internal/omnirepo/internal/audit"
	"github.com/dxc-internal/omnirepo/internal/config"
	omrcrypto "github.com/dxc-internal/omnirepo/internal/crypto"
	"github.com/dxc-internal/omnirepo/internal/httpx"
	"github.com/dxc-internal/omnirepo/internal/jobs"
	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/metadata/migrations"
	gitpkg "github.com/dxc-internal/omnirepo/internal/protocol/git"
	"github.com/dxc-internal/omnirepo/internal/protocol/oci"
	"github.com/dxc-internal/omnirepo/internal/protocol/raw"
	s3handler "github.com/dxc-internal/omnirepo/internal/protocol/s3"
	s3backend "github.com/dxc-internal/omnirepo/internal/protocol/s3/backend"
	s3keys "github.com/dxc-internal/omnirepo/internal/protocol/s3/keys"
	"github.com/dxc-internal/omnirepo/internal/scan"
	"github.com/dxc-internal/omnirepo/internal/storage"
	omrtls "github.com/dxc-internal/omnirepo/internal/tls"
)

// upstreamCredsAEADKeySetting is the settings-table key under which the
// per-install AES-GCM-256 master key is stored, base64-encoded. Generated
// on first boot; reused forever after.
const upstreamCredsAEADKeySetting = "upstream_creds_aead_key"

// dockerTokenHMACSecretSetting is the settings-table key for the 32-byte
// random HMAC secret used to sign /v2/token JWTs (D-06). Stored
// base64-encoded so the TEXT-valued settings column round-trips safely.
// Generated on first boot; reused forever after. Never logged.
const dockerTokenHMACSecretSetting = "docker_token_hmac_secret"

// dockerTokenHMACSecretSize is the required length of the JWT HMAC secret.
// 32 bytes == 256 bits == HS256 full-strength key.
const dockerTokenHMACSecretSize = 32

// BootEnsureAEADKey materializes the per-install upstream_creds AES-GCM key
// on first boot. Subsequent boots load the existing key. The key bytes never
// appear in logs — only length is logged.
//
// Exported so tests can drive the same boot path without calling the full
// Run orchestrator.
func BootEnsureAEADKey(ctx context.Context, db *metadata.DB, settings *metadata.SettingsRepo) (*omrcrypto.AEAD, error) {
	existing, err := settings.Get(ctx, upstreamCredsAEADKeySetting)
	if err == nil && existing != "" {
		raw, derr := base64.StdEncoding.DecodeString(existing)
		if derr != nil {
			return nil, fmt.Errorf("app: decode existing aead key: %w", derr)
		}
		if len(raw) != omrcrypto.KeySize {
			return nil, fmt.Errorf("app: existing aead key wrong size: got %d bytes", len(raw))
		}
		a, nerr := omrcrypto.New(raw)
		if nerr != nil {
			return nil, fmt.Errorf("app: construct aead from existing key: %w", nerr)
		}
		slog.InfoContext(ctx, "aead.key.loaded", "len", len(raw))
		return a, nil
	}
	if err != nil && !errors.Is(err, metadata.ErrNotFound) {
		return nil, fmt.Errorf("app: load aead key: %w", err)
	}

	key, err := omrcrypto.GenerateKey()
	if err != nil {
		return nil, fmt.Errorf("app: generate aead key: %w", err)
	}
	encoded := base64.StdEncoding.EncodeToString(key)
	if err := settings.Set(ctx, upstreamCredsAEADKeySetting, encoded); err != nil {
		return nil, fmt.Errorf("app: persist aead key: %w", err)
	}
	a, err := omrcrypto.New(key)
	if err != nil {
		return nil, fmt.Errorf("app: construct aead from generated key: %w", err)
	}
	slog.InfoContext(ctx, "aead.key.generated", "len", len(key))
	return a, nil
}

// BootEnsureDockerJWTSecret materializes the /v2/token HS256 HMAC secret on
// first boot. Subsequent boots load the existing secret. The secret bytes
// never appear in logs — only length is logged. Mirrors BootEnsureAEADKey
// but uses a separate settings key (D-06): rotating the Docker JWT secret
// must not invalidate encrypted upstream-creds rows, and vice versa.
//
// Exported so tests can drive the same boot path without calling Run.
func BootEnsureDockerJWTSecret(ctx context.Context, settings *metadata.SettingsRepo) ([]byte, error) {
	existing, err := settings.Get(ctx, dockerTokenHMACSecretSetting)
	if err == nil && existing != "" {
		raw, derr := base64.StdEncoding.DecodeString(existing)
		if derr != nil {
			return nil, fmt.Errorf("app: decode existing docker jwt secret: %w", derr)
		}
		if len(raw) != dockerTokenHMACSecretSize {
			return nil, fmt.Errorf("app: existing docker jwt secret wrong size: got %d bytes", len(raw))
		}
		slog.InfoContext(ctx, "docker.jwt.secret.loaded", "len", len(raw))
		return raw, nil
	}
	if err != nil && !errors.Is(err, metadata.ErrNotFound) {
		return nil, fmt.Errorf("app: load docker jwt secret: %w", err)
	}

	secret := make([]byte, dockerTokenHMACSecretSize)
	if _, rerr := cryptorand.Read(secret); rerr != nil {
		return nil, fmt.Errorf("app: generate docker jwt secret: %w", rerr)
	}
	encoded := base64.StdEncoding.EncodeToString(secret)
	if err := settings.Set(ctx, dockerTokenHMACSecretSetting, encoded); err != nil {
		return nil, fmt.Errorf("app: persist docker jwt secret: %w", err)
	}
	slog.InfoContext(ctx, "docker.jwt.secret.generated", "len", len(secret))
	return secret, nil
}

// RunOptions tunes how Run binds its listeners. The zero value uses the
// ports from cfg.Server directly. Tests use HTTPListener/HTTPSListener to
// inject ephemeral net.Listeners so they can bind to :0.
type RunOptions struct {
	// HTTPListener / HTTPSListener, when non-nil, override the default
	// tcp listener(s) the orchestrator would otherwise open. The orchestrator
	// still serves TLS on HTTPSListener using the CertHolder.
	HTTPListener  net.Listener
	HTTPSListener net.Listener

	// Ready, when non-nil, is closed once both listeners are serving. Used
	// by tests to synchronize probe requests with server-up.
	Ready chan<- struct{}
}

// Run composes every subsystem and serves until ctx is canceled. On return,
// both listeners are closed and the DB is flushed/closed.
//
// Errors of type *ErrBootstrap indicate a bad bootstrap file — callers (the
// `omnirepo serve` subcommand in particular) convert these to exit code 2
// per RESEARCH §Pitfall Mitigations "Bootstrap atomicity".
func Run(ctx context.Context, cfg config.Config, opts RunOptions) error {
	// 1. Data-root layout.
	if err := EnsureDirs(cfg.DataRoot); err != nil {
		return fmt.Errorf("app.Run: ensure dirs: %w", err)
	}

	// 2. Metadata DB + migrations.
	dbPath := filepath.Join(cfg.DataRoot, "db", "omnirepo.sqlite")
	db, err := metadata.Open(dbPath)
	if err != nil {
		return fmt.Errorf("app.Run: open db: %w", err)
	}
	defer func() { _ = db.Close() }()
	if _, err := migrations.Apply(ctx, db.Writer); err != nil {
		return fmt.Errorf("app.Run: migrate: %w", err)
	}

	// 3. Bootstrap (if path configured and file exists).
	if cfg.Bootstrap.Path != "" {
		if _, serr := os.Stat(cfg.Bootstrap.Path); serr == nil {
			if _, err := ApplyBootstrap(ctx, db, cfg, cfg.Bootstrap.Path); err != nil {
				return err // preserve *ErrBootstrap
			}
		}
	}

	// 4. TLS: first-boot self-signed if no live cert, otherwise load existing.
	certPath := filepath.Join(cfg.DataRoot, "certs", "server.crt")
	keyPath := filepath.Join(cfg.DataRoot, "certs", "server.key")
	holder := omrtls.NewCertHolder()
	if _, err := os.Stat(certPath); errors.Is(err, os.ErrNotExist) {
		hosts := append([]string{omrtls.Hostname()}, cfg.Server.ExternalHostnames...)
		if cfg.Server.Hostname != "" {
			hosts = append(hosts, cfg.Server.Hostname)
		}
		certPEM, keyPEM, err := omrtls.GenerateSelfSigned(hosts, 2*365*24*time.Hour, 4096)
		if err != nil {
			return fmt.Errorf("app.Run: first-boot self-signed: %w", err)
		}
		if err := os.WriteFile(certPath, certPEM, 0o644); err != nil {
			return fmt.Errorf("app.Run: write cert: %w", err)
		}
		if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
			return fmt.Errorf("app.Run: write key: %w", err)
		}
		if err := holder.Swap(certPEM, keyPEM); err != nil {
			return fmt.Errorf("app.Run: initial holder swap: %w", err)
		}
	} else {
		certPEM, err := os.ReadFile(certPath)
		if err != nil {
			return fmt.Errorf("app.Run: read cert: %w", err)
		}
		keyPEM, err := os.ReadFile(keyPath)
		if err != nil {
			return fmt.Errorf("app.Run: read key: %w", err)
		}
		if err := holder.Swap(certPEM, keyPEM); err != nil {
			return fmt.Errorf("app.Run: load existing cert: %w", err)
		}
	}

	// 5. Audit logger.
	auditPath := filepath.Join(cfg.DataRoot, "logs", "audit.log")
	auditLogger, err := audit.New(db, auditPath, cfg.Log.AuditMaxSizeMiB, cfg.Log.AuditKeep)
	if err != nil {
		return fmt.Errorf("app.Run: audit: %w", err)
	}

	// 5b. Upstream-creds AEAD master key (Phase 02-02 D-10). Auto-generated
	// on first boot; loaded thereafter. The resulting AEAD is passed to the
	// UpstreamCredsRepo so REST CRUD + pull-external (Phase 02-10) both
	// share the same per-install key.
	aead, err := BootEnsureAEADKey(ctx, db, metadata.NewSettingsRepo(db))
	if err != nil {
		return fmt.Errorf("app.Run: aead key: %w", err)
	}
	upstreamCreds := metadata.NewUpstreamCredsRepo(db, aead)

	// 5c. Boot recovery (Phase 02-04 D-19, SYNC-03). Re-pend any row
	// stuck in 'running' from a previous boot. MUST run before the pools
	// start so it cannot race with in-flight leases.
	recovery, err := jobs.RecoverStuckJobs(ctx, db)
	if err != nil {
		return fmt.Errorf("app.Run: jobs boot recovery: %w", err)
	}
	slog.InfoContext(ctx, "jobs.boot_recovered",
		"sync", recovery.SyncRecovered, "scans", recovery.ScansRecovered)

	// 5d. Job pools (D-14, D-16). The scan pool's handler map is populated
	// below (Plan 02-09 wiring) BEFORE the pool's dispatcher goroutine
	// starts, so map-mutation under read concurrency is impossible. The
	// sync pool stays empty until 02-10 (pull-external) / 02-12 (gc) wire
	// concrete handlers.
	syncHandlers := jobs.Handlers{}
	scanHandlers := jobs.Handlers{}
	syncPool := jobs.NewSyncPool(db, metadata.NewSyncJobsRepo(db), syncHandlers, cfg.Jobs)
	scanPool := jobs.NewScanPool(db, metadata.NewScansRepo(db), scanHandlers, cfg.Jobs)

	// 5e. Scan handler (Plan 02-09). Wires the FakeRunner-compatible
	// scan.Runner (production = trivyRunner via cfg.Trivy) to the
	// scan pool so docker + raw artifacts get scanned + SBOM'd + the
	// severity cache invalidated post-commit.
	blobRoot := filepath.Join(cfg.DataRoot, "blobs")
	ociCAS := storage.NewCAS(blobRoot)
	severityCache := scan.NewSeverityCache(cfg.Scan.SeverityCacheTTL)
	scanRunner := scan.NewTrivyRunner(cfg.Trivy)
	scanHandler, err := scan.NewHandler(scan.HandlerDeps{
		DB:        db,
		Runner:    scanRunner,
		Scans:     metadata.NewScansRepo(db),
		Vulns:     metadata.NewVulnerabilitiesRepo(db),
		Manifests: metadata.NewDockerManifestsRepo(db),
		RawFiles:  metadata.NewRawFilesRepo(db),
		Repos:     metadata.NewReposRepo(db),
		Projects:  metadata.NewProjectsRepo(db),
		CAS:       ociCAS,
		Audit:     auditLogger,
		Cache:     severityCache,
		DataRoot:  cfg.DataRoot,
	})
	if err != nil {
		return fmt.Errorf("app.Run: scan handler: %w", err)
	}
	// One handler covers both kinds; Pool dispatches by JobView.Kind which
	// for scans rows is ArtifactKind.
	scanHandlers["docker"] = func(c context.Context, j *jobs.JobView) error {
		return scanHandler.Handle(c, &metadata.Scan{
			ID: j.ID, RepoID: j.RepoID, ArtifactKind: j.Kind,
			ArtifactID: j.Payload, Attempts: j.Attempts, LeaseID: j.LeaseID,
		})
	}
	scanHandlers["raw"] = scanHandlers["docker"]

	// NOTE: syncPool.Run / scanPool.Run are started BELOW, after all
	// sync handlers (02-10 pull-external etc.) have been registered.
	// Running the pool before the map is complete would race the
	// dispatcher against handler registration.

	// 6. Router with global middleware + system routes.
	router := httpx.New(httpx.Deps{Config: cfg})

	// 6a. S3 virtual-host rewrite (Phase 04-07, D-23). MUST be registered
	// as global middleware BEFORE any routes so chi's route matching sees
	// the rewritten path (/s3/<bucket>/<key>) for virtual-host requests.
	router.Use(s3handler.VHostRewrite(cfg.Server.ExternalHostnames))

	router.Get("/healthz", httpx.Healthz())
	router.Get("/readyz", httpx.Readyz(httpx.ReadyzDeps{DB: db, Holder: holder}))

	// 6b. /v2 OCI registry handler (Phase 02-05). Materialize the HMAC
	// secret on first boot; subsequent boots reuse the existing secret.
	// JWT TTL is taken from cfg.Docker.JWTTTLSeconds (default 3600s).
	dockerJWTSecret, err := BootEnsureDockerJWTSecret(ctx, metadata.NewSettingsRepo(db))
	if err != nil {
		return fmt.Errorf("app.Run: docker jwt secret: %w", err)
	}
	jwtTTL := time.Duration(cfg.Docker.JWTTTLSeconds) * time.Second
	if jwtTTL <= 0 {
		jwtTTL = time.Hour
	}
	// blobRoot + ociCAS already constructed in step 5e (scan handler wiring).
	ociHandler := oci.New(oci.Deps{
		DB:          db,
		Users:       metadata.NewUsersRepo(db),
		APIKeys:     metadata.NewAPIKeysRepo(db),
		Repos:       metadata.NewReposRepo(db),
		Projects:    metadata.NewProjectsRepo(db),
		Sessions:    metadata.NewSessionsRepo(db),
		Members:     metadata.NewMembersRepo(db),
		CAS:         ociCAS,
		Blobs:       metadata.NewDockerBlobsRepo(db),
		BlobUploads: metadata.NewBlobUploadsRepo(db),
		Sess:        metadata.NewBlobUploadSessionsRepo(db),
		Audit:       auditLogger,
		DataRoot:    cfg.DataRoot,
		HMACSecret:  dockerJWTSecret,
		JWTTTL:      jwtTTL,

		// Plan 02-07 wiring: manifests + tags + auto-scan enqueue.
		Manifests: metadata.NewDockerManifestsRepo(db),
		Tags:      metadata.NewDockerTagsRepo(db),
		Scans:     metadata.NewScansRepo(db),
		ScanKick:  scanPool.Kick,
		// Plan 02-09: block_on_severity gate (D-26, SCAN-07).
		SeverityGate: oci.NewSeverityGate(
			metadata.NewReposRepo(db),
			metadata.NewScansRepo(db),
			severityCache,
			auditLogger,
		),
	})
	ociHandler.Mount(router)
	ociHandler.MountCosign(router)

	// 6b.2. Plan 02-10: pull-external + promote.
	// Register the sync-pool handler for kind="pull_external"; the REST
	// endpoints are mounted via api.Mount below (they live on /api/v1).
	pullExternalJob := oci.NewPullExternalHandler(oci.PullExternalDeps{
		DB:        db,
		CAS:       ociCAS,
		Blobs:     metadata.NewDockerBlobsRepo(db),
		Manifests: metadata.NewDockerManifestsRepo(db),
		Tags:      metadata.NewDockerTagsRepo(db),
		Scans:     metadata.NewScansRepo(db),
		ScanKick:  scanPool.Kick,
		Repos:     metadata.NewReposRepo(db),
		Projects:  metadata.NewProjectsRepo(db),
		Creds:     upstreamCreds,
		Audit:     auditLogger,
		OCI:       ociHandler,
	})
	syncHandlers[oci.PullExternalJobKind] = func(c context.Context, j *jobs.JobView) error {
		return pullExternalJob.Handle(c, j.Payload, j.ProjectID, j.RepoID)
	}
	pullExternalREST := oci.NewPullExternalREST(ociHandler, upstreamCreds,
		metadata.NewSyncJobsRepo(db), syncPool.Kick)
	promoteREST := oci.NewPromoteREST(ociHandler)

	// 6b.3. Plan 02-12: super-admin GC handler. Registers on the sync
	// pool under jobs.GCJobKind. The corresponding REST endpoint (POST
	// /api/v1/admin/gc) is mounted via api.Mount below by passing GCDeps.
	gcQuiescence := time.Duration(cfg.GC.BlobQuiescenceSeconds) * time.Second
	if gcQuiescence <= 0 {
		gcQuiescence = time.Hour
	}
	gcRetention := time.Duration(cfg.GC.TrashRetentionDays) * 24 * time.Hour
	if gcRetention <= 0 {
		gcRetention = 7 * 24 * time.Hour
	}
	gcHandler := jobs.NewGCHandler(jobs.GCHandler{
		DB:             db,
		Blobs:          metadata.NewDockerBlobsRepo(db),
		BlobUploads:    metadata.NewBlobUploadsRepo(db),
		Sessions:       metadata.NewBlobUploadSessionsRepo(db),
		CAS:            ociCAS,
		Trash:          storage.NewTrash(filepath.Join(cfg.DataRoot, "trash")),
		Audit:          auditLogger,
		DataRoot:       cfg.DataRoot,
		Quiescence:     gcQuiescence,
		TrashRetention: gcRetention,
	})
	syncHandlers[jobs.GCJobKind] = func(c context.Context, j *jobs.JobView) error {
		return gcHandler.Handle(c, j.ID)
	}

	// 6c. Admin + user REST at /api/v1. Mounted AFTER ociHandler so the
	// OCIActions bundle can reference handlers that depend on it.
	signingKeysRepo := metadata.NewSigningKeysRepo(db, aead)
	aptSuitesRepo := metadata.NewAptSuitesRepo(db)
	// Composed repo-create hook: RPM (signing key for type ∈ {rpm, deb}) +
	// DEB (default apt_suites matrix for type = deb). Both run inside the
	// same writer tx as the repos INSERT so a failure in either rolls back
	// the repo row (T-03-05 atomicity).
	rpmRepoCreateHook := func(ctx context.Context, tx *sql.Tx, repoID int64, repoType, projectName, repoName string) (map[string]any, error) {
		fp, err := CreateRPMRepoHook(ctx, tx, repoID, repoType, projectName, repoName, signingKeysRepo, cfg.Signing.GPGKeyBits, nil)
		if err != nil {
			return nil, err
		}
		if err := CreateDEBRepoHook(ctx, tx, repoID, repoType, aptSuitesRepo); err != nil {
			return nil, err
		}
		if fp == "" {
			return nil, nil
		}
		return map[string]any{"fingerprint": fp}, nil
	}

	// Phase 03 Plan 06: SYNC-05 sync handlers + REST. Construct registries
	// for each protocol BEFORE api.Mount so wireSync can attach a
	// coalescer.Get(repoID).Kick() factory at the end of every batch.
	sharedLocks := storage.NewLocks()
	helmRegistry := helmDeps{
		cfg: cfg, db: db, auditLogger: auditLogger, locks: sharedLocks,
	}.wireHelm(router)
	defer shutdownHelmRegistry(context.Background(), helmRegistry)

	pypiRegistry := pypiDeps{
		cfg: cfg, db: db, auditLogger: auditLogger, locks: sharedLocks,
	}.wirePyPI(router)
	defer shutdownPyPIRegistry(context.Background(), pypiRegistry)

	rpmRegistry := rpmDeps{
		cfg: cfg, db: db, auditLogger: auditLogger,
		signingKeys: signingKeysRepo, locks: sharedLocks,
	}.wireRPM(router)
	defer shutdownRPMRegistry(context.Background(), rpmRegistry)

	debRegistry := debDeps{
		cfg: cfg, db: db, auditLogger: auditLogger,
		signingKeys: signingKeysRepo, locks: sharedLocks,
	}.wireDEB(router)
	defer shutdownDEBRegistry(context.Background(), debRegistry)

	syncAdapter := syncDeps{
		cfg:          cfg,
		db:           db,
		auditLogger:  auditLogger,
		creds:        upstreamCreds,
		syncPool:     syncPool,
		syncHandlers: syncHandlers,
		rpmRegistry:  rpmRegistry,
		debRegistry:  debRegistry,
		pypiRegistry: pypiRegistry,
		helmRegistry: helmRegistry,
	}.wireSync()

	api.Mount(router, api.Deps{
		DB:            db,
		Users:         metadata.NewUsersRepo(db),
		Sessions:      metadata.NewSessionsRepo(db),
		APIKeys:       metadata.NewAPIKeysRepo(db),
		Projects:      metadata.NewProjectsRepo(db),
		Members:       metadata.NewMembersRepo(db),
		Repos:         metadata.NewReposRepo(db),
		Settings:      metadata.NewSettingsRepo(db),
		UpstreamCreds: upstreamCreds,
		// Phase 04-05: S3 access-key CRUD. Reuses the same per-install
		// AEAD master key as upstream_creds.
		S3Keys: metadata.NewS3KeysRepo(db),
		S3AEAD: aead,
		Holder:        holder,
		DataRoot:      cfg.DataRoot,
		Audit:         auditLogger,
		Trash:         storage.NewTrash(filepath.Join(cfg.DataRoot, "trash")),
		Locks:         storage.NewLocks(),
		SessionTTL:    cfg.Auth.SessionTTL,

		RepoCreateHook: rpmRepoCreateHook,
		// Plan 02-09: scan REST endpoints.
		ScanDeps: &api.ScansDeps{
			Scans:    metadata.NewScansRepo(db),
			Vulns:    metadata.NewVulnerabilitiesRepo(db),
			ScanKick: scanPool.Kick,
			SBOMRoot: filepath.Join(cfg.DataRoot, "sboms"),
		},
		// Plan 02-10: OCI pull-external + promote.
		OCIActions: &api.OCIActionsDeps{
			PullExternal: pullExternalREST,
			Promote:      promoteREST,
		},
		// Plan 02-12: super-admin GC trigger.
		GCDeps: &api.GCDeps{
			SyncJobs: metadata.NewSyncJobsRepo(db),
			SyncKick: syncPool.Kick,
		},
		// Plan 03-06: SYNC-05 REST endpoint.
		SyncDeps: syncAdapter,
	})

	// Start pool dispatchers AFTER all handlers are registered (02-09 scan,
	// 02-10 pull-external). Map mutation under read concurrency is
	// impossible because Run reads handlers only inside its dispatcher
	// goroutine, which hasn't started yet.
	go syncPool.Run(ctx)
	go scanPool.Run(ctx)

	// 6c. RAW pass-through handler (Phase 02-08, D-27..D-31). Mounted on
	// the root router because the URL path includes the project slug —
	// no /api/v1 prefix.
	repoRoot := filepath.Join(cfg.DataRoot, "repos")
	rawHandler := raw.New(raw.Deps{
		DB:       db,
		Users:    metadata.NewUsersRepo(db),
		APIKeys:  metadata.NewAPIKeysRepo(db),
		Sessions: metadata.NewSessionsRepo(db),
		Repos:    metadata.NewReposRepo(db),
		Projects: metadata.NewProjectsRepo(db),
		Files:    metadata.NewRawFilesRepo(db),
		Scans:    metadata.NewScansRepo(db),
		Members:  metadata.NewMembersRepo(db),
		Path:     storage.NewPathStore(repoRoot),
		Trash:    storage.NewTrash(filepath.Join(cfg.DataRoot, "trash")),
		Audit:    auditLogger,
		RepoRoot: repoRoot,
		// Plan 02-09: block_on_severity gate (D-26, SCAN-07).
		SeverityGate: raw.NewSeverityGate(
			metadata.NewReposRepo(db),
			metadata.NewScansRepo(db),
			severityCache,
			auditLogger,
		),
	})
	rawHandler.Mount(router)

	// Phase 03 protocol handlers + sync wiring already constructed above
	// (moved before api.Mount so the SyncDeps closure can reference each
	// per-repo regen registry).

	// 6d. Git Smart-HTTP handler (Phase 04-09). Backend selection is
	// config-driven (server.git_backend = "gogit"|"gitkit"; D-26).
	gitBackend := gitpkg.SelectBackend(cfg)
	slog.InfoContext(ctx, "git.backend.selected", "name", gitBackend.BackendName())
	gitHandler := gitpkg.New(gitpkg.Deps{
		Backend:  gitBackend,
		Config:   cfg,
		Locks:    sharedLocks,
		Repos:    metadata.NewReposRepo(db),
		Projects: metadata.NewProjectsRepo(db),
		Members:  metadata.NewMembersRepo(db),
		Audit:    auditLogger,
		DataRoot: cfg.DataRoot,
		Users:    metadata.NewUsersRepo(db),
		Sessions: metadata.NewSessionsRepo(db),
		APIKeys:  metadata.NewAPIKeysRepo(db),
	})
	gitHandler.Mount(router)

	// 6e. S3-compatible handler (Phase 04-07). SigV4 → auth.Can → gofakes3.
	// Uses the same per-install AEAD key as upstream_creds for S3 access-key
	// secret decryption, and the shared locks for per-bucket serialization.
	s3Service := s3keys.NewService(metadata.NewS3KeysRepo(db), aead)
	s3Be := s3backend.New(cfg.DataRoot, db, sharedLocks)
	s3Deps := &s3handler.Deps{
		Service:   s3Service,
		Backend:   s3Be,
		Skew:      cfg.Auth.SigV4Skew,
		Hostnames: cfg.Server.ExternalHostnames,
	}
	s3Deps.Mount(router)

	// 7. Listeners.
	httpLn := opts.HTTPListener
	if httpLn == nil {
		ln, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.Server.HTTPPort))
		if err != nil {
			return fmt.Errorf("app.Run: listen http: %w", err)
		}
		httpLn = ln
	}
	httpsLn := opts.HTTPSListener
	if httpsLn == nil {
		ln, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.Server.HTTPSPort))
		if err != nil {
			_ = httpLn.Close()
			return fmt.Errorf("app.Run: listen https: %w", err)
		}
		httpsLn = ln
	}

	httpSrv := &http.Server{Handler: router}
	httpsSrv := &http.Server{
		Handler: router,
		TLSConfig: &stdtls.Config{
			GetCertificate: holder.Get,
			MinVersion:     stdtls.VersionTLS12,
		},
	}

	errs := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if err := httpSrv.Serve(httpLn); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errs <- fmt.Errorf("http serve: %w", err)
		}
	}()
	go func() {
		defer wg.Done()
		tlsLn := stdtls.NewListener(httpsLn, httpsSrv.TLSConfig)
		if err := httpsSrv.Serve(tlsLn); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errs <- fmt.Errorf("https serve: %w", err)
		}
	}()

	if opts.Ready != nil {
		close(opts.Ready)
	}

	// Block until ctx cancellation or a Serve error.
	select {
	case <-ctx.Done():
	case err := <-errs:
		_ = httpSrv.Close()
		_ = httpsSrv.Close()
		wg.Wait()
		return err
	}

	// Graceful shutdown (D-20). Drain pools in PARALLEL with their own
	// grace deadline (cfg.Jobs.ShutdownGraceSeconds, default 30s) so
	// running sync + scan handlers get a full grace window each rather
	// than fighting over a shared timer. Run pool drain before HTTP
	// shutdown so in-flight enqueue requests don't outlast their pool.
	poolGrace := time.Duration(cfg.Jobs.ShutdownGraceSeconds) * time.Second
	if poolGrace <= 0 {
		poolGrace = 30 * time.Second
	}
	shutdownCtx := context.Background()
	var poolWG sync.WaitGroup
	poolWG.Add(2)
	go func() { defer poolWG.Done(); syncPool.Shutdown(shutdownCtx, poolGrace) }()
	go func() { defer poolWG.Done(); scanPool.Shutdown(shutdownCtx, poolGrace) }()
	poolWG.Wait()

	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutCtx)
	_ = httpsSrv.Shutdown(shutCtx)
	wg.Wait()
	return nil
}
