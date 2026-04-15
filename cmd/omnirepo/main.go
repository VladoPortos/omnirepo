// Command omnirepo is the OmniRepo server binary entry point.
//
// Subcommands (D-45):
//
//	serve (default)  Start HTTP+HTTPS listeners, bootstrap data root, run the API.
//	version          Print the build version and exit 0.
//	migrate up       Apply schema migrations.
//	migrate status   Show applied migrations.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/dxc-internal/omnirepo/internal/app"
	"github.com/dxc-internal/omnirepo/internal/config"
	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/metadata/migrations"
)

// Version is the build version. Overridden at link time via -ldflags "-X main.Version=...".
var Version = "0.1.0-phase1"

// osExit is indirected so tests can inject a panicking shim without actually
// terminating the test binary. Use runAndExit for the end-to-end wrapper.
var osExit = os.Exit

func main() {
	runAndExit(os.Args[1:])
}

// runAndExit dispatches a single invocation of the binary. On error it
// converts the error into an exit code via osExit: *app.ErrBootstrap → 2,
// everything else → 1. A nil error is a no-op (the subcommand either
// returned cleanly or blocked until it returned cleanly).
func runAndExit(args []string) {
	if len(args) == 0 {
		if err := serve(nil); err != nil {
			exitForError(err)
		}
		return
	}
	switch args[0] {
	case "serve":
		if err := serve(args[1:]); err != nil {
			exitForError(err)
		}
	case "version":
		fmt.Println(Version)
	case "migrate":
		if err := migrate(args[1:]); err != nil {
			exitForError(err)
		}
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n\n", args[0])
		usage()
		osExit(2)
	}
}

func exitForError(err error) {
	fmt.Fprintln(os.Stderr, err.Error())
	var be *app.ErrBootstrap
	if errors.As(err, &be) {
		osExit(2)
		return
	}
	osExit(1)
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

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	return app.Run(ctx, cfg, app.RunOptions{})
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
	defer func() { _ = db.Close() }()

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
