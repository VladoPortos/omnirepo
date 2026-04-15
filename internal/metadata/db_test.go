package metadata_test

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dxc-internal/omnirepo/internal/metadata"
)

// TestOpenAppliesPragmas verifies every pragma from D-09 is applied on a
// fresh file-backed DB.
func TestOpenAppliesPragmas(t *testing.T) {
	t.Parallel()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := metadata.Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = db.Close() }()

	checks := []struct {
		pragma string
		want   string
	}{
		{"journal_mode", "wal"},
		{"foreign_keys", "1"},
		{"synchronous", "1"}, // NORMAL
		{"busy_timeout", "5000"},
		{"cache_size", "-65536"},
		{"temp_store", "2"}, // MEMORY
	}
	for _, c := range checks {
		var got string
		row := db.Reader.QueryRow("PRAGMA " + c.pragma)
		if err := row.Scan(&got); err != nil {
			t.Fatalf("pragma %s: %v", c.pragma, err)
		}
		if !strings.EqualFold(got, c.want) {
			t.Errorf("pragma %s: got %q want %q", c.pragma, got, c.want)
		}
	}
}

func TestWriterPoolIsOne(t *testing.T) {
	t.Parallel()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := metadata.Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = db.Close() }()
	if got := db.Writer.Stats().MaxOpenConnections; got != 1 {
		t.Errorf("Writer.MaxOpenConnections = %d, want 1", got)
	}
}

func TestReaderPoolIsEight(t *testing.T) {
	t.Parallel()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := metadata.Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = db.Close() }()
	if got := db.Reader.Stats().MaxOpenConnections; got != 8 {
		t.Errorf("Reader.MaxOpenConnections = %d, want 8", got)
	}
}

// TestWriteTxSerializesConcurrentWriters is the pitfall P2 gate: two goroutines
// hammering WriteTx must never produce SQLITE_BUSY / "database is locked".
func TestWriteTxSerializesConcurrentWriters(t *testing.T) {
	t.Parallel()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := metadata.Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = db.Close() }()

	ctx := context.Background()
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `CREATE TABLE t (id INTEGER PRIMARY KEY AUTOINCREMENT, v TEXT)`)
		return err
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	var (
		wg       sync.WaitGroup
		busy     int64
		errCount int64
	)
	const iters = 100
	for g := 0; g < 2; g++ {
		wg.Add(1)
		go func(gid int) {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				err := db.WriteTx(ctx, func(tx *sql.Tx) error {
					_, err := tx.ExecContext(ctx, "INSERT INTO t(v) VALUES (?)", fmt.Sprintf("g%d-%d", gid, i))
					return err
				})
				if err != nil {
					atomic.AddInt64(&errCount, 1)
					m := err.Error()
					if strings.Contains(m, "SQLITE_BUSY") || strings.Contains(m, "database is locked") {
						atomic.AddInt64(&busy, 1)
					}
				}
			}
		}(g)
	}
	wg.Wait()
	if busy != 0 {
		t.Fatalf("SQLITE_BUSY / database is locked count = %d, want 0", busy)
	}
	if errCount != 0 {
		t.Fatalf("error count = %d, want 0", errCount)
	}
}

// TestWriteTxUsesImmediate proves BEGIN IMMEDIATE semantics: while WriteTx A is
// held, a second WriteTx must block (writer pool = 1 serializes, immediate
// transactions hold write lock).
func TestWriteTxUsesImmediate(t *testing.T) {
	t.Parallel()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := metadata.Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = db.Close() }()

	ctx := context.Background()
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `CREATE TABLE t (id INTEGER PRIMARY KEY AUTOINCREMENT)`)
		return err
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	hold := make(chan struct{})
	released := make(chan struct{})
	done := make(chan error, 1)

	go func() {
		done <- db.WriteTx(ctx, func(tx *sql.Tx) error {
			close(hold)
			<-released
			_, err := tx.ExecContext(ctx, "INSERT INTO t DEFAULT VALUES")
			return err
		})
	}()

	<-hold

	second := make(chan error, 1)
	go func() {
		second <- db.WriteTx(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, "INSERT INTO t DEFAULT VALUES")
			return err
		})
	}()

	select {
	case err := <-second:
		t.Fatalf("second WriteTx should have blocked; returned early: %v", err)
	case <-time.After(250 * time.Millisecond):
		// good — blocked as expected
	}

	close(released)

	if err := <-done; err != nil {
		t.Fatalf("first tx: %v", err)
	}
	if err := <-second; err != nil {
		t.Fatalf("second tx: %v", err)
	}
}

// TestWriteTxRollbackOnError ensures that an error from fn rolls back the tx
// (data not committed) and the writer connection is released (next WriteTx
// works normally).
func TestWriteTxRollbackOnError(t *testing.T) {
	t.Parallel()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := metadata.Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = db.Close() }()

	ctx := context.Background()
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `CREATE TABLE t (id INTEGER PRIMARY KEY AUTOINCREMENT, v TEXT)`)
		return err
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	sentinel := fmt.Errorf("boom")
	err = db.WriteTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "INSERT INTO t(v) VALUES ('will-rollback')"); err != nil {
			return err
		}
		return sentinel
	})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("want sentinel error, got %v", err)
	}

	var n int
	if err := db.Reader.QueryRow("SELECT COUNT(*) FROM t").Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatalf("after rollback, row count = %d, want 0", n)
	}

	// Next WriteTx must work — writer conn was released.
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO t(v) VALUES ('ok')")
		return err
	}); err != nil {
		t.Fatalf("post-rollback tx: %v", err)
	}
}
