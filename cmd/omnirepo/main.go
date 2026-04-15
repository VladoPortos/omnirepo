// Command omnirepo is the OmniRepo server binary entry point.
//
// Subcommands (D-45):
//
//	serve (default)  Start HTTP+HTTPS listeners, bootstrap data root, run the API.
//	version          Print the build version and exit 0.
//	migrate up       Apply schema migrations (stub in Phase 1; plan 01-02 lands the runner).
//	migrate status   Show applied migrations (stub in Phase 1).
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"

	"github.com/dxc-internal/omnirepo/internal/app"
	"github.com/dxc-internal/omnirepo/internal/config"
	"github.com/dxc-internal/omnirepo/internal/httpx"
	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/metadata/migrations"
)

// Version is the build version. Overridden at link time via -ldflags "-X main.Version=...".
var Version = "0.1.0-phase1-skeleton"

func main() {
	if len(os.Args) < 2 {
		// Default subcommand is 'serve'.
		if err := serve(os.Args[1:]); err != nil {
			exit(err)
		}
		return
	}

	switch os.Args[1] {
	case "serve":
		if err := serve(os.Args[2:]); err != nil {
			exit(err)
		}
	case "version":
		fmt.Println(Version)
	case "migrate":
		if err := migrate(os.Args[2:]); err != nil {
			exit(err)
		}
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `omnirepo - self-hosted artifact repository server

Usage:
  omnirepo [serve]           Start the server (default subcommand)
  omnirepo version           Print version
  omnirepo migrate up        Apply schema migrations
  omnirepo migrate status    Show applied migrations

Flags for 'serve':
  --config PATH              Path to YAML config (overrides $OMNIREPO_CONFIG)`)
}

func serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	var cfgPath string
	fs.StringVar(&cfgPath, "config", "", "path to YAML config file")
	_ = fs.Parse(args)

	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("serve: %w", err)
	}

	if err := app.EnsureDirs(cfg.DataRoot); err != nil {
		return fmt.Errorf("serve: ensure data root: %w", err)
	}

	router := httpx.New(httpx.Deps{Config: cfg})

	slog.Info("phase 1 skeleton: HTTP listener only; TLS + DB open land in plan 05/02",
		slog.Int("http_port", cfg.Server.HTTPPort),
		slog.String("data_root", cfg.DataRoot),
	)

	addr := fmt.Sprintf(":%d", cfg.Server.HTTPPort)
	srv := &http.Server{Addr: addr, Handler: router}
	return srv.ListenAndServe()
}

func migrate(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("migrate: subcommand required (up | status)")
	}
	sub := args[0]
	fs := flag.NewFlagSet("migrate "+sub, flag.ExitOnError)
	var cfgPath string
	fs.StringVar(&cfgPath, "config", "", "path to YAML config file")
	_ = fs.Parse(args[1:])

	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	if err := app.EnsureDirs(cfg.DataRoot); err != nil {
		return fmt.Errorf("migrate: ensure data root: %w", err)
	}
	dbPath := filepath.Join(cfg.DataRoot, "db", "omnirepo.sqlite")
	db, err := metadata.Open(dbPath)
	if err != nil {
		return fmt.Errorf("migrate: open db: %w", err)
	}
	defer db.Close()

	ctx := context.Background()
	switch sub {
	case "up":
		applied, err := migrations.Apply(ctx, db.Writer)
		if err != nil {
			return fmt.Errorf("migrate up: %w", err)
		}
		if len(applied) == 0 {
			fmt.Println("no migrations to apply")
			return nil
		}
		for _, stem := range applied {
			fmt.Println("applied:", stem)
		}
		return nil
	case "status":
		a, p, err := migrations.Status(ctx, db.Reader)
		if err != nil {
			return fmt.Errorf("migrate status: %w", err)
		}
		if len(a) == 0 {
			fmt.Println("applied: (none)")
		} else {
			for _, s := range a {
				fmt.Println("applied:", s)
			}
		}
		if len(p) == 0 {
			fmt.Println("pending: (none)")
		} else {
			for _, s := range p {
				fmt.Println("pending:", s)
			}
		}
		return nil
	default:
		return fmt.Errorf("migrate: unknown subcommand %q", sub)
	}
}

func exit(err error) {
	fmt.Fprintln(os.Stderr, err.Error())
	os.Exit(1)
}
