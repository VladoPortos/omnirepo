package app

import (
	"context"
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
	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/metadata/migrations"
	"github.com/dxc-internal/omnirepo/internal/storage"
	omrtls "github.com/dxc-internal/omnirepo/internal/tls"
)

// upstreamCredsAEADKeySetting is the settings-table key under which the
// per-install AES-GCM-256 master key is stored, base64-encoded. Generated
// on first boot; reused forever after.
const upstreamCredsAEADKeySetting = "upstream_creds_aead_key"

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

	// 6. Router with global middleware + system routes + API.
	router := httpx.New(httpx.Deps{Config: cfg})
	router.Get("/healthz", httpx.Healthz())
	router.Get("/readyz", httpx.Readyz(httpx.ReadyzDeps{DB: db, Holder: holder}))
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
		Holder:        holder,
		DataRoot:      cfg.DataRoot,
		Audit:         auditLogger,
		Trash:         storage.NewTrash(filepath.Join(cfg.DataRoot, "trash")),
		Locks:         storage.NewLocks(),
		SessionTTL:    cfg.Auth.SessionTTL,
	})

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

	// Graceful shutdown with a short deadline.
	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutCtx)
	_ = httpsSrv.Shutdown(shutCtx)
	wg.Wait()
	return nil
}
