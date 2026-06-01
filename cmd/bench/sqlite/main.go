// Command bench-sqlite is the writer-contention benchmark: N goroutines
// hammer metadata.WriteTx for a duration, and the binary exits non-zero if
// any attempt surfaces SQLITE_BUSY / "database is locked".
//
// Default is 16 workers x 30s. Override with --workers / --duration for
// dev-loop runs.
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vladoportos/omnirepo/internal/metadata"
	"github.com/vladoportos/omnirepo/internal/metadata/migrations"
)

func main() {
	workers := flag.Int("workers", 16, "concurrent WriteTx goroutines")
	duration := flag.Duration("duration", 30*time.Second, "total bench duration")
	dbFlag := flag.String("db", "", "database path (default: temp file, deleted on exit)")
	flag.Parse()

	dbPath := *dbFlag
	cleanup := func() {}
	if dbPath == "" {
		tmp, err := os.MkdirTemp("", "omnirepo-bench-*")
		if err != nil {
			fatal("mktemp: %v", err)
		}
		dbPath = filepath.Join(tmp, "bench.sqlite")
		cleanup = func() { _ = os.RemoveAll(tmp) }
	}
	defer cleanup()

	db, err := metadata.Open(dbPath)
	if err != nil {
		fatal("open: %v", err)
	}
	defer func() { _ = db.Close() }()

	ctx := context.Background()
	if _, err := migrations.Apply(ctx, db.Writer); err != nil {
		fatal("migrate: %v", err)
	}

	// Seed one project so audit_log inserts have a realistic surface.
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "INSERT INTO projects(name) VALUES ('bench')"); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO repos(project_id,type,name) VALUES (1,'raw','bench-repo')"); err != nil {
			return err
		}
		return nil
	}); err != nil {
		fatal("seed: %v", err)
	}

	runCtx, cancel := context.WithTimeout(ctx, *duration)
	defer cancel()

	var (
		wg        sync.WaitGroup
		txCount   uint64
		errs      int64
		busyCount int64
		counter   uint64
	)

	start := time.Now()
	for i := 0; i < *workers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for runCtx.Err() == nil {
				err := db.WriteTx(runCtx, func(tx *sql.Tx) error {
					if _, err := tx.ExecContext(runCtx,
						"INSERT INTO audit_log(event_kind,target_kind,target_id,outcome,details_json) VALUES ('bench.tick','bench','0','ok','{}')",
					); err != nil {
						return err
					}
					n := atomic.AddUint64(&counter, 1)
					if _, err := tx.ExecContext(runCtx,
						"INSERT INTO repos_fts(repo_name, project_name, description, type) VALUES (?,?,?,?)",
						fmt.Sprintf("bench-%d-%d", workerID, n), "bench", "bench tick", "raw",
					); err != nil {
						return err
					}
					return nil
				})
				atomic.AddUint64(&txCount, 1)
				if err != nil {
					// Context cancelled at shutdown is not a real error.
					if runCtx.Err() != nil && strings.Contains(err.Error(), "context") {
						return
					}
					atomic.AddInt64(&errs, 1)
					m := err.Error()
					if strings.Contains(m, "SQLITE_BUSY") || strings.Contains(m, "database is locked") {
						atomic.AddInt64(&busyCount, 1)
					}
				}
			}
		}(i)
	}
	wg.Wait()
	elapsed := time.Since(start)

	txPerSec := float64(txCount) / elapsed.Seconds()
	fmt.Printf("bench-sqlite workers=%d duration=%s tx=%d tx_per_sec=%.1f errors=%d SQLITE_BUSY=%d\n",
		*workers, *duration, txCount, txPerSec, errs, busyCount)

	if busyCount != 0 || errs != 0 {
		os.Exit(1)
	}
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
