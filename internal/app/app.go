package app

import (
	"context"
	stdtls "crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/dxc-internal/omnirepo/internal/api"
	"github.com/dxc-internal/omnirepo/internal/audit"
	"github.com/dxc-internal/omnirepo/internal/config"
	"github.com/dxc-internal/omnirepo/internal/httpx"
	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/metadata/migrations"
	"github.com/dxc-internal/omnirepo/internal/storage"
	omrtls "github.com/dxc-internal/omnirepo/internal/tls"
)

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

	// 6. Router with global middleware + system routes + API.
	router := httpx.New(httpx.Deps{Config: cfg})
	router.Get("/healthz", httpx.Healthz())
	router.Get("/readyz", httpx.Readyz(httpx.ReadyzDeps{DB: db, Holder: holder}))
	api.Mount(router, api.Deps{
		DB:         db,
		Users:      metadata.NewUsersRepo(db),
		Sessions:   metadata.NewSessionsRepo(db),
		APIKeys:    metadata.NewAPIKeysRepo(db),
		Projects:   metadata.NewProjectsRepo(db),
		Members:    metadata.NewMembersRepo(db),
		Repos:      metadata.NewReposRepo(db),
		Settings:   metadata.NewSettingsRepo(db),
		Holder:     holder,
		DataRoot:   cfg.DataRoot,
		Audit:      auditLogger,
		Trash:      storage.NewTrash(filepath.Join(cfg.DataRoot, "trash")),
		Locks:      storage.NewLocks(),
		SessionTTL: cfg.Auth.SessionTTL,
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
