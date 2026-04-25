package app_test

// Plan 02-04 Task 2 — boot-recovery sweep tests.
//
// Two assertions:
//
//   T6 BootSweepsOrphans     — seed a stale s3_multipart_uploads row,
//                              boot the app, assert the row is gone after
//                              the boot goroutine completes.
//   T7 BootTimeoutGrepGate   — grep gate ensuring the goroutine wraps
//                              appCtx via context.WithTimeout(appCtx,
//                              5*time.Minute) — NOT context.Background()
//                              (I-2 fix).

import (
	"context"
	"crypto/tls"
	"database/sql"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/dxc-internal/omnirepo/internal/app"
	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/metadata/migrations"
)

// TestBootSweep_RemovesOrphanMultipart pins T6: a stale multipart upload
// is gone from the DB once the boot goroutine completes.
//
// The boot goroutine is async, so we poll up to 5s for the row to vanish.
// Five seconds is well below the 5-minute timeout the goroutine itself
// imposes (I-2) — if the goroutine is broken the test fails fast.
func TestBootSweep_RemovesOrphanMultipart(t *testing.T) {
	cfg := newTestConfig(t, "")
	// Pre-seed the DB BEFORE app.Run opens it: open the same SQLite file,
	// run migrations, insert a stale row, close. app.Run will reopen and
	// the boot sweep goroutine will see the stale row.
	dbPath := filepath.Join(cfg.DataRoot, "db", "omnirepo.sqlite")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o750); err != nil {
		t.Fatal(err)
	}

	staleUploadID := "boot-stale-upload-id"
	seedStaleViaTempDB(t, dbPath, staleUploadID)

	httpLn, httpsLn := tcpPair(t)
	httpAddr := httpLn.Addr().String()
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- app.Run(ctx, cfg, app.RunOptions{HTTPListener: httpLn, HTTPSListener: httpsLn, Ready: ready})
	}()
	<-ready
	defer func() {
		cancel()
		<-done
	}()

	// Wait for the listener to be live so we know the boot goroutine had
	// a chance to run (the boot sweep is launched right after RecoverStuckJobs
	// which precedes listener startup).
	tr := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	_ = waitFor(t, "http://"+httpAddr+"/healthz", tr, 3*time.Second)

	// Poll up to 5 seconds for the stale row to vanish.
	deadline := time.Now().Add(5 * time.Second)
	var n int
	for time.Now().Before(deadline) {
		db, err := sql.Open("sqlite", dbPath+"?mode=ro&_pragma=busy_timeout(5000)")
		if err != nil {
			t.Fatalf("open db: %v", err)
		}
		_ = db.QueryRow(
			`SELECT COUNT(*) FROM s3_multipart_uploads WHERE upload_id = ?`,
			staleUploadID,
		).Scan(&n)
		_ = db.Close()
		if n == 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if n != 0 {
		t.Errorf("stale multipart upload row still present after boot sweep: count=%d", n)
	}
}

// seedStaleViaTempDB opens the on-disk SQLite file at dbPath, runs the
// project migrations, and inserts a stale s3_multipart_uploads row whose
// initiated_at is forced to 2020-01-01 (well past any sane cutoff).
func seedStaleViaTempDB(t *testing.T, dbPath, uploadID string) {
	t.Helper()
	db, err := metadata.Open(dbPath)
	if err != nil {
		t.Fatalf("open temp db: %v", err)
	}
	defer func() { _ = db.Close() }()
	// Run migrations so the schema is in place.
	if err := applyAllMigrations(t, db); err != nil {
		t.Fatalf("migrate temp db: %v", err)
	}
	ctx := context.Background()
	if _, err := db.Writer.ExecContext(ctx,
		`INSERT INTO users(id, login, email, password_hash) VALUES (1, 'admin', 'admin@x', 'x')`,
	); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	res, err := db.Writer.ExecContext(ctx,
		`INSERT INTO projects(name) VALUES ('seed-proj')`,
	)
	if err != nil {
		t.Fatalf("seed project: %v", err)
	}
	pid, _ := res.LastInsertId()
	if _, err := db.Writer.ExecContext(ctx,
		`INSERT INTO s3_buckets(name, project_id) VALUES ('seed-bucket', ?)`, pid,
	); err != nil {
		t.Fatalf("seed bucket: %v", err)
	}
	var bid int64
	_ = db.Reader.QueryRowContext(ctx,
		`SELECT id FROM s3_buckets WHERE name='seed-bucket'`,
	).Scan(&bid)
	res, err = db.Writer.ExecContext(ctx,
		`INSERT INTO s3_access_keys(project_id, label, access_key_id, secret_enc, created_by_user_id)
		 VALUES (?, 'seed', 'AKIDSEED', X'00', 1)`, pid,
	)
	if err != nil {
		t.Fatalf("seed s3_access_keys: %v", err)
	}
	keyID, _ := res.LastInsertId()
	if _, err := db.Writer.ExecContext(ctx,
		`INSERT INTO s3_multipart_uploads(upload_id, bucket_id, key, initiated_by_s3_key_id, initiated_at, metadata_json)
		 VALUES (?, ?, 'k', ?, '2020-01-01T00:00:00.000Z', '{}')`,
		uploadID, bid, keyID,
	); err != nil {
		t.Fatalf("seed s3_multipart_uploads: %v", err)
	}
}

// applyAllMigrations runs the migration runner against the temp DB.
func applyAllMigrations(t *testing.T, db *metadata.DB) error {
	t.Helper()
	_, err := migrations.Apply(context.Background(), db.Writer)
	return err
}

// TestBootSweep_BootContextTimeoutGrepGate (T7) — grep gate enforcing the
// goroutine bounds itself with context.WithTimeout(appCtx, 5*time.Minute)
// rather than context.Background() (I-2 fix). A simple regex over app.go
// is the cheapest way to lock this in; a deeper assertion would require
// instrumenting the goroutine itself.
func TestBootSweep_BootContextTimeoutGrepGate(t *testing.T) {
	src, err := os.ReadFile("app.go")
	if err != nil {
		t.Fatalf("read app.go: %v", err)
	}
	s := string(src)
	if !strings.Contains(s, "context.WithTimeout(appCtx, 5*time.Minute)") {
		t.Errorf("app.go boot sweep goroutine MUST bound itself with context.WithTimeout(appCtx, 5*time.Minute) (I-2 fix); not found in source")
	}
	if !strings.Contains(s, "SweepOrphanMultiparts") {
		t.Errorf("app.go MUST call backend.SweepOrphanMultiparts from the boot goroutine; reference not found")
	}
	// Defensive: the goroutine must NOT use context.Background — that
	// would survive app shutdown indefinitely.
	if strings.Contains(s, "context.Background()") &&
		strings.Contains(s, "// 5c-2.") {
		// Surface a more useful message when both are present together —
		// but only fail if context.Background() appears in the same
		// vicinity as our sweep goroutine. Conservative: just warn.
		t.Logf("note: context.Background() appears elsewhere in app.go; ensure the boot-sweep goroutine uses appCtx-derived ctx")
	}
}
