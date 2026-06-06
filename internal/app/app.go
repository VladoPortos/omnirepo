package app

import (
	"context"
	cryptorand "crypto/rand"
	stdtls "crypto/tls"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/vladoportos/omnirepo/internal/api"
	"github.com/vladoportos/omnirepo/internal/audit"
	"github.com/vladoportos/omnirepo/internal/auth"
	"github.com/vladoportos/omnirepo/internal/config"
	omrcrypto "github.com/vladoportos/omnirepo/internal/crypto"
	"github.com/vladoportos/omnirepo/internal/httpx"
	"github.com/vladoportos/omnirepo/internal/jobs"
	"github.com/vladoportos/omnirepo/internal/metadata"
	"github.com/vladoportos/omnirepo/internal/metadata/migrations"
	"github.com/vladoportos/omnirepo/internal/protocol/deb"
	gitpkg "github.com/vladoportos/omnirepo/internal/protocol/git"
	"github.com/vladoportos/omnirepo/internal/protocol/helm"
	"github.com/vladoportos/omnirepo/internal/protocol/oci"
	"github.com/vladoportos/omnirepo/internal/protocol/pypi"
	"github.com/vladoportos/omnirepo/internal/protocol/raw"
	"github.com/vladoportos/omnirepo/internal/protocol/rpm"
	s3handler "github.com/vladoportos/omnirepo/internal/protocol/s3"
	s3backend "github.com/vladoportos/omnirepo/internal/protocol/s3/backend"
	s3keys "github.com/vladoportos/omnirepo/internal/protocol/s3/keys"
	"github.com/vladoportos/omnirepo/internal/scan"
	"github.com/vladoportos/omnirepo/internal/storage"
	omrtls "github.com/vladoportos/omnirepo/internal/tls"
	"github.com/vladoportos/omnirepo/web"
)

// upstreamCredsAEADKeySetting is the settings-table key under which the
// per-install AES-GCM-256 master key is stored, base64-encoded. Generated
// on first boot; reused forever after.
const upstreamCredsAEADKeySetting = "upstream_creds_aead_key"

// dockerTokenHMACSecretSetting is the settings-table key for the 32-byte
// random HMAC secret used to sign /v2/token JWTs. Stored base64-encoded
// so the TEXT-valued settings column round-trips safely.
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
// but uses a separate settings key: rotating the Docker JWT secret must
// not invalidate encrypted upstream-creds rows, and vice versa.
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
// for bootstrap atomicity.
func Run(ctx context.Context, cfg config.Config, opts RunOptions) error {
	// 1. Data-root layout.
	if err := EnsureDirs(cfg.DataRoot); err != nil {
		return fmt.Errorf("app.Run: ensure dirs: %w", err)
	}

	// 1b. Trivy DB first-boot seed. Copies baked DB from Docker
	// image to data volume on first boot; no-op outside Docker.
	if err := SeedTrivyDB(ctx, cfg.DataRoot, "/opt/trivy-db"); err != nil {
		return fmt.Errorf("app.Run: trivy db seed: %w", err)
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
	//
	// API-created RPM/DEB repos trigger eager signing-key
	// generation via the composedRepoCreateHook further
	// down; bootstrap.json-created repos historically skipped that, so
	// conformance tests seeded via bootstrap.json saw `GET /public-key.asc
	// → 404`. Extend the bootstrap hook to call the same
	// CreateRPMRepoHook / CreateDEBRepoHook helpers so both paths land
	// the same signing-key + apt_suites state. Init the AEAD + signing-
	// keys repo early (before bootstrap) since BootEnsureAEADKey needs
	// its own writer tx — calling it from inside the bootstrap tx would
	// deadlock against modernc/sqlite's single-writer lock.
	if cfg.Bootstrap.Path != "" {
		if _, serr := os.Stat(cfg.Bootstrap.Path); serr == nil {
			bootAead, aerr := BootEnsureAEADKey(ctx, db, metadata.NewSettingsRepo(db))
			if aerr != nil {
				return fmt.Errorf("app.Run: bootstrap aead: %w", aerr)
			}
			bootSigningKeys := metadata.NewSigningKeysRepo(db, bootAead)
			bootAptSuites := metadata.NewAptSuitesRepo(db)
			gitRefsRepoBoot := metadata.NewGitRefsRepo(db)
			bootHook := func(ctx context.Context, tx *sql.Tx, repoID int64, repoType, projectName, repoName string) (map[string]any, error) {
				if _, err := CreateRPMRepoHook(ctx, tx, repoID, repoType, projectName, repoName, bootSigningKeys, cfg.Signing.GPGKeyBits, nil); err != nil {
					return nil, err
				}
				if err := CreateDEBRepoHook(ctx, tx, repoID, repoType, bootAptSuites); err != nil {
					return nil, err
				}
				// Shared git arm — see gitpkg.CreateRepoHook for the mirror
				// skip + orphan-dir cleanup rationale.
				if err := gitpkg.CreateRepoHook(ctx, tx, repoID, repoType, projectName, repoName, cfg.DataRoot, gitRefsRepoBoot); err != nil {
					return nil, err
				}
				return nil, nil
			}
			if _, err := ApplyBootstrapWithHook(ctx, db, cfg, cfg.Bootstrap.Path, bootHook); err != nil {
				return err // preserve *ErrBootstrap
			}
		}
	}

	// 3a. Record baked-in Trivy DB metadata. When the DB dir was
	// just seeded from /opt/trivy-db we have a metadata.json on disk but
	// no trivy_db_meta row — the dashboard widget then renders "Age
	// unknown (baked-in)". Populate the row now so real ages surface.
	if err := RecordBakedTrivyDBMeta(ctx, db.Writer, cfg.DataRoot); err != nil {
		slog.WarnContext(ctx, "trivy.db.meta.record_failed", "err", err)
	}

	// 3b. One-shot FTS backfill for pre-existing DBs. The bootstrap path now
	// indexes repos into repos_fts inline, but DBs seeded before that change
	// (or manually) have empty FTS tables. Rebuild if repos_fts is empty but
	// repos has rows. Idempotent: skipped when either side is already aligned.
	if err := ensureFTSIndexed(ctx, db); err != nil {
		return fmt.Errorf("app.Run: fts backfill: %w", err)
	}

	// 4. TLS: first-boot self-signed if no live cert, otherwise load existing.
	// Audit finding #4: honor cfg.TLS.CertPath / cfg.TLS.KeyPath when set so
	// operator config overrides aren't silently dropped. Fall back to the
	// legacy DataRoot/certs/server.{crt,key} layout when unset.
	certPath := cfg.TLS.CertPath
	if certPath == "" {
		certPath = filepath.Join(cfg.DataRoot, "certs", "server.crt")
	}
	keyPath := cfg.TLS.KeyPath
	if keyPath == "" {
		keyPath = filepath.Join(cfg.DataRoot, "certs", "server.key")
	}
	if err := os.MkdirAll(filepath.Dir(certPath), 0o750); err != nil {
		return fmt.Errorf("app.Run: mkdir cert dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o750); err != nil {
		return fmt.Errorf("app.Run: mkdir key dir: %w", err)
	}
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
		tmpCert := certPath + ".tmp"
		tmpKey := keyPath + ".tmp"
		if err := os.WriteFile(tmpCert, certPEM, 0o644); err != nil {
			return fmt.Errorf("app.Run: write cert tmp: %w", err)
		}
		if err := os.WriteFile(tmpKey, keyPEM, 0o600); err != nil {
			_ = os.Remove(tmpCert)
			return fmt.Errorf("app.Run: write key tmp: %w", err)
		}
		if err := os.Rename(tmpKey, keyPath); err != nil {
			_ = os.Remove(tmpCert)
			_ = os.Remove(tmpKey)
			return fmt.Errorf("app.Run: rename key: %w", err)
		}
		if err := os.Rename(tmpCert, certPath); err != nil {
			return fmt.Errorf("app.Run: rename cert: %w", err)
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

	// 5a. One-shot boot-time integrity_check. This runs exactly once at
	// boot, post-migrations, BEFORE the HTTP listeners come up. Failure is
	// logged + cached; boot continues so the Dashboard card (destructive
	// variant) can surface the failure to operators. The adapter wraps
	// auditLogger
	// so metadata/ stays free of the internal/audit import (cycle-break).
	bootSettings := metadata.NewSettingsRepo(db)
	bootAudit := newBootAuditAdapter(auditLogger)
	if err := metadata.RunBootIntegrityCheck(ctx, db, bootSettings, bootAudit); err != nil {
		slog.WarnContext(ctx, "app.boot.integrity_check.returned_error", "err", err)
	}

	// 5b. Upstream-creds AEAD master key. Auto-generated
	// on first boot; loaded thereafter. The resulting AEAD is passed to the
	// UpstreamCredsRepo so REST CRUD + pull-external both
	// share the same per-install key.
	aead, err := BootEnsureAEADKey(ctx, db, metadata.NewSettingsRepo(db))
	if err != nil {
		return fmt.Errorf("app.Run: aead key: %w", err)
	}
	upstreamCreds := metadata.NewUpstreamCredsRepo(db, aead)

	// 5c. Boot recovery. Re-pend any row
	// stuck in 'running' from a previous boot. MUST run before the pools
	// start so it cannot race with in-flight leases.
	recovery, err := jobs.RecoverStuckJobs(ctx, db)
	if err != nil {
		return fmt.Errorf("app.Run: jobs boot recovery: %w", err)
	}
	slog.InfoContext(ctx, "jobs.boot_recovered",
		"sync", recovery.SyncRecovered, "scans", recovery.ScansRecovered)

	// 5c-2. S3 multipart orphan sweep. Mirrors the helm-retry
	// boot-recovery pattern: one-shot goroutine bounded by
	// context.WithTimeout(appCtx, 5*time.Minute) so a shutdown signal
	// cancels the sweep promptly and a malicious or buggy backend cannot
	// stall startup indefinitely. Errors log at WARN; never blocks boot.
	// Honors the no-in-process-scheduler invariant — POST
	// /api/v1/admin/maintenance/sweep-multipart is the on-demand trigger
	// for external schedulers.
	bootSweepBackend := s3backend.New(cfg.DataRoot, db, storage.NewLocks())
	bootSweepRetention := cfg.S3.MultipartRetention
	if bootSweepRetention <= 0 {
		bootSweepRetention = 24 * time.Hour
	}
	go func(appCtx context.Context, retention time.Duration) {
		sweepCtx, sweepCancel := context.WithTimeout(appCtx, 5*time.Minute)
		defer sweepCancel()
		cutoff := time.Now().Add(-retention)
		swept, cleaned, err := bootSweepBackend.SweepOrphanMultiparts(sweepCtx, cutoff)
		if err != nil {
			slog.WarnContext(sweepCtx, "s3.multipart.boot_sweep.error", "err", err)
			return
		}
		slog.InfoContext(sweepCtx, "s3.multipart.boot_sweep.done",
			"swept", swept, "cleaned_dirs", cleaned)
	}(ctx, bootSweepRetention)

	// 5d. Job pools. The scan pool's handler map is populated below BEFORE
	// the pool's dispatcher goroutine starts, so map-mutation under read
	// concurrency is impossible. The sync pool stays empty until the
	// pull-external and gc wirings register concrete handlers.
	syncHandlers := jobs.Handlers{}
	scanHandlers := jobs.Handlers{}
	syncPool := jobs.NewSyncPool(db, metadata.NewSyncJobsRepo(db), syncHandlers, cfg.Jobs)
	scanPool := jobs.NewScanPool(db, metadata.NewScansRepo(db), scanHandlers, cfg.Jobs)

	// 5e. Scan handler. Wires the FakeRunner-compatible
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
	dispatch := func(c context.Context, j *jobs.JobView) error {
		return scanHandler.Handle(c, &metadata.Scan{
			ID: j.ID, RepoID: j.RepoID, ArtifactKind: j.Kind,
			ArtifactID: j.Payload, Attempts: j.Attempts, LeaseID: j.LeaseID,
		})
	}
	// All artifact kinds share the same dispatcher; the Handle switch
	// materializes + scans per kind.
	scanHandlers["docker"] = dispatch
	scanHandlers["raw"] = dispatch
	scanHandlers["rpm"] = dispatch
	scanHandlers["deb"] = dispatch
	scanHandlers["pypi"] = dispatch
	scanHandlers["helm"] = dispatch

	// NOTE: syncPool.Run / scanPool.Run are started BELOW, after all
	// sync handlers (02-10 pull-external etc.) have been registered.
	// Running the pool before the map is complete would race the
	// dispatcher against handler registration.

	// 6. Router with global middleware + system routes. LoginBoxSeeder
	// seeds an auth.LoginBox on each request so StructuredLogger can
	// record the authenticated login even though auth middlewares mutate
	// ctx inside inner chains that the outer logger never sees directly.
	router := httpx.New(httpx.Deps{
		Config:         cfg,
		Settings:       metadata.NewSettingsRepo(db),
		LoginBoxSeeder: seedLoginBox,
	})

	// 6a. S3 virtual-host rewrite. MUST be registered
	// as global middleware BEFORE any routes so chi's route matching sees
	// the rewritten path (/s3/<bucket>/<key>) for virtual-host requests.
	router.Use(s3handler.VHostRewrite(cfg.Server.ExternalHostnames))

	// 6b. Opt-in dev-only canned error routes for the UI story page.
	// Registered AFTER all Use() calls (chi panics when Use follows a
	// route) and no-ops unless OMNIREPO_DEV=1 at process start —
	// production binaries never expose the /_dev path.
	httpx.MountDevErrorRoutes(router, api.MountDevErrorRoutes)

	router.Get("/healthz", httpx.Healthz())
	router.Get("/readyz", httpx.Readyz(httpx.ReadyzDeps{DB: db, Holder: holder}))

	// 6b. /v2 OCI registry handler. Materialize the HMAC
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

	// Wire the Helm protocol stack (handler + regen
	// coalescer + mirror) BEFORE the OCI handler so we can hand the mirror
	// adapter in through oci.Deps.HelmMirror. Mounting order is independent
	// (chi routes are leaf-scoped), so mounting helm first does not affect
	// /v2 dispatch. sharedLocks is constructed inline here because the
	// other protocol wirings below still want to share the same Locks
	// instance.
	sharedLocks := storage.NewLocks()
	// block_on_severity gate. The signature is
	// identical across protocols but Go's nominal function typing requires
	// a per-protocol adapter. Cache + DB lookups are shared via
	// severityCache (constructed earlier alongside the OCI gate).
	rawGate := raw.NewSeverityGate(
		metadata.NewReposRepo(db),
		metadata.NewScansRepo(db),
		severityCache,
		auditLogger,
	)
	helmGate := helm.SeverityGateFn(rawGate)
	pypiGate := pypi.SeverityGateFn(rawGate)
	rpmGate := rpm.SeverityGateFn(rawGate)
	debGate := deb.SeverityGateFn(rawGate)

	helmRegistry, helmMirror, helmHandler := helmDeps{
		cfg: cfg, db: db, auditLogger: auditLogger, locks: sharedLocks,
		severity: helmGate,
	}.wireHelm(router)
	defer shutdownHelmRegistry(context.Background(), helmRegistry)
	ociHelmMirrorHook := wireHelmMirror(ociCAS, helmMirror)

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

		// Admin-tunable caps. 0 → handler falls back to built-in
		// defaults (512 MiB chunk / 10 GiB session).
		ChunkMaxBytes:   cfg.Docker.ChunkMaxBytes,
		SessionMaxBytes: cfg.Docker.SessionMaxBytes,

		// manifests + tags + auto-scan enqueue.
		Manifests: metadata.NewDockerManifestsRepo(db),
		Tags:      metadata.NewDockerTagsRepo(db),
		Scans:     metadata.NewScansRepo(db),
		ScanKick:  scanPool.Kick,
		// block_on_severity gate.
		SeverityGate: oci.NewSeverityGate(
			metadata.NewReposRepo(db),
			metadata.NewScansRepo(db),
			severityCache,
			auditLogger,
		),
		// trust-pinned host for WWW-Authenticate realm.
		ExternalHostnames: cfg.Server.ExternalHostnames,

		// forward-only Helm OCI→traditional mirror hook.
		// nil when helmMirror or ociCAS were not wired (unit tests); the
		// /v2 manifestPut detection branch is a no-op in that case.
		HelmMirror: ociHelmMirrorHook,
	})
	ociHandler.Mount(router)
	ociHandler.MountCosign(router)

	// 6b.2. pull-external + promote.
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
		// sync-jobs repo for throttled progress writes.
		SyncJobs: metadata.NewSyncJobsRepo(db),
		OCI:      ociHandler,
	})
	syncHandlers[oci.PullExternalJobKind] = func(c context.Context, j *jobs.JobView) error {
		return pullExternalJob.Handle(c, j.Payload, j.ProjectID, j.RepoID, j.ID)
	}
	pullExternalREST := oci.NewPullExternalREST(ociHandler, upstreamCreds,
		metadata.NewSyncJobsRepo(db), syncPool.Kick)
	promoteREST := oci.NewPromoteREST(ociHandler)
	deleteTagREST := oci.NewDeleteTagREST(ociHandler)

	// 6b.3. super-admin GC handler. Registers on the sync
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
	gitRefsRepo := metadata.NewGitRefsRepo(db)
	// Composed repo-create hook: RPM (signing key for type ∈ {rpm, deb}) +
	// DEB (default apt_suites matrix for type = deb) + Git (bare-repo init
	// + HEAD seed for type = git). All run inside the same writer tx as the
	// repos INSERT so a failure in any rolls back the repo row.
	composedRepoCreateHook := func(ctx context.Context, tx *sql.Tx, repoID int64, repoType, projectName, repoName string) (map[string]any, error) {
		fp, err := CreateRPMRepoHook(ctx, tx, repoID, repoType, projectName, repoName, signingKeysRepo, cfg.Signing.GPGKeyBits, nil)
		if err != nil {
			return nil, err
		}
		if err := CreateDEBRepoHook(ctx, tx, repoID, repoType, aptSuitesRepo); err != nil {
			return nil, err
		}
		// Git bare-repo lifecycle — shared arm, see gitpkg.CreateRepoHook
		// for the mirror skip + orphan-dir cleanup (audit finding #7)
		// rationale. Same implementation serves the bootstrap hook above.
		if err := gitpkg.CreateRepoHook(ctx, tx, repoID, repoType, projectName, repoName, cfg.DataRoot, gitRefsRepo); err != nil {
			return nil, err
		}
		if fp == "" {
			return nil, nil
		}
		return map[string]any{"fingerprint": fp}, nil
	}

	// Sync handlers + REST. Construct registries
	// for each protocol BEFORE api.Mount so wireSync can attach a
	// coalescer.Get(repoID).Kick() factory at the end of every batch.
	//
	// NOTE: helmRegistry + helmMirror are wired earlier (above the OCI
	// handler) so the mirror adapter can feed oci.Deps.HelmMirror. The
	// other protocol registries do not need that coupling.
	pypiRegistry, pypiHandler := pypiDeps{
		cfg: cfg, db: db, auditLogger: auditLogger, locks: sharedLocks,
		severity: pypiGate,
	}.wirePyPI(router)
	defer shutdownPyPIRegistry(context.Background(), pypiRegistry)

	rpmRegistry, rpmHandler := rpmDeps{
		cfg: cfg, db: db, auditLogger: auditLogger,
		signingKeys: signingKeysRepo, locks: sharedLocks,
		severity: rpmGate,
	}.wireRPM(router)
	defer shutdownRPMRegistry(context.Background(), rpmRegistry)

	debRegistry, debHandler := debDeps{
		cfg: cfg, db: db, auditLogger: auditLogger,
		signingKeys: signingKeysRepo, locks: sharedLocks,
		severity: debGate,
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

	// S3 backend is constructed here (ahead of the 6e mount site below) so
	// api.Deps can expose it for the REST bucket-provision endpoint.
	s3Be := s3backend.New(cfg.DataRoot, db, sharedLocks)

	// One shared FTSReindexer drives the
	// per-repo FTS5 prune+reindex cascade in Repos.Restore + Projects.Restore.
	// Bound to the same canonical typed repos used elsewhere — base tables are
	// the source of truth on Restore.
	ftsReindexer := metadata.NewFTSReindexer(db,
		metadata.NewRPMPackagesRepo(db),
		metadata.NewDEBPackagesRepo(db),
		metadata.NewPyPIFilesRepo(db),
		metadata.NewHelmChartsRepo(db),
	)

	api.Mount(router, api.Deps{
		DB:            db,
		Users:         metadata.NewUsersRepo(db),
		Sessions:      metadata.NewSessionsRepo(db),
		APIKeys:       metadata.NewAPIKeysRepo(db),
		Projects:      metadata.NewProjectsRepo(db).WithReindexer(ftsReindexer),
		Members:       metadata.NewMembersRepo(db),
		Repos:         metadata.NewReposRepo(db).WithReindexer(ftsReindexer),
		Settings:      metadata.NewSettingsRepo(db),
		UpstreamCreds: upstreamCreds,
		// S3 access-key CRUD. Reuses the same per-install
		// AEAD master key as upstream_creds.
		S3Keys:        metadata.NewS3KeysRepo(db),
		S3AEAD:        aead,
		S3Backend:     s3Be,
		S3ObjectsRepo: metadata.NewS3ObjectsRepo(db),
		// per-protocol row repos
		// consumed by handleRestoreTrash for the <proto>_drift kinds.
		PyPIFiles:   metadata.NewPyPIFilesRepo(db),
		RPMPackages: metadata.NewRPMPackagesRepo(db),
		DEBPackages: metadata.NewDEBPackagesRepo(db),
		HelmCharts:  metadata.NewHelmChartsRepo(db),
		Holder:      holder,
		DataRoot:    cfg.DataRoot,
		TrivyDBDir:  cfg.Trivy.DBPath,
		// align the admin DB-pull binary lookup with the
		// scan runner so cfg.Trivy.BinaryPath overrides apply both
		// places. Without this the pull endpoint relied on $PATH while
		// the scan runner used the configured absolute path — same
		// install, two different binaries when not on PATH.
		TrivyBinary:          cfg.Trivy.BinaryPath,
		TLSCertPath:          cfg.TLS.CertPath,
		TLSKeyPath:           cfg.TLS.KeyPath,
		Audit:                auditLogger,
		Trash:                storage.NewTrash(filepath.Join(cfg.DataRoot, "trash")),
		Locks:                storage.NewLocks(),
		SessionTTL:           cfg.Auth.SessionTTL,
		S3MultipartRetention: cfg.S3.MultipartRetention,

		RepoCreateHook: composedRepoCreateHook,
		// scan REST endpoints.
		ScanDeps: &api.ScansDeps{
			Scans:    metadata.NewScansRepo(db),
			Vulns:    metadata.NewVulnerabilitiesRepo(db),
			ScanKick: scanPool.Kick,
			SBOMRoot: filepath.Join(cfg.DataRoot, "sboms"),
		},
		// OCI pull-external + promote + tag delete.
		OCIActions: &api.OCIActionsDeps{
			PullExternal: pullExternalREST,
			Promote:      promoteREST,
			DeleteTag:    deleteTagREST,
		},
		// session-authed row-delete shims.
		// The four protocol handlers already implement DELETE; these
		// re-expose them under /api/v1 where SessionOrAPIKey is active
		// so the browser's session cookie can drive row-level deletes.
		ProtocolDeletes: &api.ProtocolDeletesDeps{
			RPM:  rpmHandler,
			DEB:  debHandler,
			PyPI: pypiHandler,
			Helm: helmHandler,
		},
		// super-admin GC trigger.
		GCDeps: &api.GCDeps{
			SyncJobs: metadata.NewSyncJobsRepo(db),
			SyncKick: syncPool.Kick,
		},
		// sync REST endpoint.
		SyncDeps: syncAdapter,
	})

	// Start pool dispatchers AFTER all handlers are registered (02-09 scan,
	// 02-10 pull-external). Map mutation under read concurrency is
	// impossible because Run reads handlers only inside its dispatcher
	// goroutine, which hasn't started yet.
	go syncPool.Run(ctx)
	go scanPool.Run(ctx)

	// 6c. RAW pass-through handler. Mounted on
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
		// block_on_severity gate.
		SeverityGate: raw.NewSeverityGate(
			metadata.NewReposRepo(db),
			metadata.NewScansRepo(db),
			severityCache,
			auditLogger,
		),
	})
	rawHandler.Mount(router)

	// Protocol handlers + sync wiring already constructed above
	// (moved before api.Mount so the SyncDeps closure can reference each
	// per-repo regen registry).

	// 6d. Git Smart-HTTP handler. Backend selection is
	// config-driven (server.git_backend = "gogit"|"gitkit").
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
		DB:       db,
		Refs:     gitRefsRepo,
	})
	gitHandler.Mount(router)

	// 6e. S3-compatible handler. SigV4 → auth.Can → gofakes3.
	// Uses the same per-install AEAD key as upstream_creds for S3 access-key
	// secret decryption, and the shared locks for per-bucket serialization.
	// s3Be is constructed above so the REST bucket-provision endpoint in
	// api.Deps can share the same backend instance.
	s3Service := s3keys.NewService(metadata.NewS3KeysRepo(db), aead)
	s3Deps := &s3handler.Deps{
		Service:   s3Service,
		Backend:   s3Be,
		Skew:      cfg.Auth.SigV4Skew,
		Hostnames: cfg.Server.ExternalHostnames,
	}
	s3Deps.Mount(router)

	// 6f. SPA / dev-proxy NotFound handler. Mounted AFTER
	// all API and protocol routes so unknown paths serve the SPA shell.
	if httpx.IsDevMode() {
		router.NotFound(httpx.DevProxy().ServeHTTP)
		slog.InfoContext(ctx, "spa.mode", "handler", "dev-proxy", "target", "http://localhost:5173")
	} else {
		router.NotFound(httpx.SPAHandler(web.DistFS))
		slog.InfoContext(ctx, "spa.mode", "handler", "embedded")
	}

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

	// Defensive HTTP timeouts (audit finding #5 / gosec G112).
	// ReadHeaderTimeout is the slowloris defense; IdleTimeout reaps leaked
	// keep-alive sockets. Read/Write are deliberately taken from config and
	// default to 0 (unlimited) because large artifact pushes (OCI blobs,
	// RPM/deb/pypi/raw uploads, git packs, S3 PUT) routinely run minutes.
	httpSrv := &http.Server{
		Handler:           router,
		ReadHeaderTimeout: cfg.Server.Timeouts.ReadHeader,
		ReadTimeout:       cfg.Server.Timeouts.Read,
		WriteTimeout:      cfg.Server.Timeouts.Write,
		IdleTimeout:       cfg.Server.Timeouts.Idle,
	}
	httpsSrv := &http.Server{
		Handler:           router,
		ReadHeaderTimeout: cfg.Server.Timeouts.ReadHeader,
		ReadTimeout:       cfg.Server.Timeouts.Read,
		WriteTimeout:      cfg.Server.Timeouts.Write,
		IdleTimeout:       cfg.Server.Timeouts.Idle,
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

	slog.InfoContext(ctx, "http.listen", "addr", httpLn.Addr().String())
	slog.InfoContext(ctx, "https.listen", "addr", httpsLn.Addr().String())

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

	// Graceful shutdown. Drain pools in PARALLEL with their own
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

// SeedTrivyDB copies the baked Trivy DB from bakedDir to dataRoot/trivy/db/
// on first boot. Subsequent boots skip the copy when the target directory
// already contains files. Outside Docker (no bakedDir) this is a silent no-op.
//
// Exported so tests can exercise the seeding logic with a custom bakedDir.
func SeedTrivyDB(ctx context.Context, dataRoot, bakedDir string) error {
	dbDir := filepath.Join(dataRoot, "trivy", "db")

	// Not in Docker — skip silently.
	if _, err := os.Stat(bakedDir); os.IsNotExist(err) {
		return nil
	}

	// Already seeded — skip.
	entries, err := os.ReadDir(dbDir)
	if err == nil && len(entries) > 0 {
		slog.InfoContext(ctx, "trivy.db.seed.skipped", "reason", "already_present", "dir", dbDir)
		return nil
	}

	// Ensure target dir exists.
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		return fmt.Errorf("trivy db seed: mkdir %s: %w", dbDir, err)
	}

	// Copy all files from baked to target.
	srcEntries, err := os.ReadDir(bakedDir)
	if err != nil {
		return fmt.Errorf("trivy db seed: read %s: %w", bakedDir, err)
	}
	for _, e := range srcEntries {
		if e.IsDir() {
			continue
		}
		src := filepath.Join(bakedDir, e.Name())
		dst := filepath.Join(dbDir, e.Name())
		data, err := os.ReadFile(src)
		if err != nil {
			return fmt.Errorf("trivy db seed: read %s: %w", src, err)
		}
		if err := os.WriteFile(dst, data, 0644); err != nil {
			return fmt.Errorf("trivy db seed: write %s: %w", dst, err)
		}
	}
	slog.InfoContext(ctx, "trivy.db.seeded", "from", bakedDir, "to", dbDir, "files", len(srcEntries))
	return nil
}

// trivyDBMetadata mirrors the subset of Trivy's metadata.json we care about.
// Trivy writes UpdatedAt (when the upstream DB snapshot was built),
// DownloadedAt (when we fetched/baked it), and Version.
type trivyDBMetadata struct {
	Version      int       `json:"Version"`
	UpdatedAt    time.Time `json:"UpdatedAt"`
	DownloadedAt time.Time `json:"DownloadedAt"`
}

// RecordBakedTrivyDBMeta inserts a trivy_db_meta row for a baked-in DB that
// was seeded to disk without a corresponding DB row. The dashboard
// widget falls back to "Age unknown (baked-in)" whenever no row exists, even
// though Trivy's metadata.json carries the upstream UpdatedAt we can surface
// as a real age.
//
// Idempotent: skipped when any trivy_db_meta row already exists, OR when
// there's no metadata.json to read from.
func RecordBakedTrivyDBMeta(ctx context.Context, db *sql.DB, dataRoot string) error {
	var existing int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM trivy_db_meta`).Scan(&existing); err != nil {
		return fmt.Errorf("count trivy_db_meta: %w", err)
	}
	if existing > 0 {
		return nil
	}

	dbDir := filepath.Join(dataRoot, "trivy", "db")
	metaPath := filepath.Join(dbDir, "metadata.json")
	raw, err := os.ReadFile(metaPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read %s: %w", metaPath, err)
	}
	var m trivyDBMetadata
	if err := json.Unmarshal(raw, &m); err != nil {
		return fmt.Errorf("parse %s: %w", metaPath, err)
	}

	// applied_at = when this instance picked up the DB. Prefer Trivy's
	// DownloadedAt (set when the DB tarball was baked into the image).
	// Fall back to now if Trivy zero-valued it.
	appliedAt := m.DownloadedAt
	if appliedAt.IsZero() {
		appliedAt = time.Now().UTC()
	}
	version := ""
	if !m.UpdatedAt.IsZero() {
		version = "baked-" + m.UpdatedAt.UTC().Format("20060102")
	}
	size := dirSizeBytes(dbDir)

	_, err = db.ExecContext(ctx, `
		INSERT INTO trivy_db_meta(version, source, size_bytes, applied_at)
		VALUES (?, 'baked-in', ?, ?)
	`, version, size, appliedAt.UTC().Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("insert trivy_db_meta: %w", err)
	}
	slog.InfoContext(ctx, "trivy.db.meta.seeded",
		"version", version,
		"applied_at", appliedAt,
		"size_bytes", size,
	)
	return nil
}

// seedLoginBox is the httpx.LoginBoxSeeder adapter: it allocates a fresh
// auth.LoginBox for each request and stashes it on the ctx so downstream
// auth middlewares (auth.WithActor) populate it. StructuredLogger reads
// box.GetLogin() at request exit to fill the actor_id slog attribute.
func seedLoginBox(ctx context.Context) (context.Context, httpx.LoginBox) {
	box := &authLoginBox{inner: &auth.LoginBox{}}
	return auth.WithLoginBox(ctx, box.inner), box
}

// authLoginBox is a thin adapter from *auth.LoginBox (concrete type) to
// the httpx.LoginBox interface — httpx cannot import auth without a
// cycle, so the interface is defined in httpx and implemented here.
type authLoginBox struct{ inner *auth.LoginBox }

func (b *authLoginBox) GetLogin() string { return b.inner.Login }

// dirSizeBytes walks dir and sums regular-file sizes. Errors and non-regular
// entries are skipped silently — this is a best-effort metric.
func dirSizeBytes(dir string) int64 {
	var total int64
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		total += info.Size()
	}
	return total
}
