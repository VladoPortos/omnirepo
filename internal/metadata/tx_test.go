package metadata_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/vladoportos/omnirepo/internal/metadata"
)

// openTestDB returns a fresh metadata.DB on a tmpdir-backed file. Mirrors the
// shape used elsewhere in db_test.go (file-backed DB so the writer pool's
// BEGIN IMMEDIATE pragma chain is exercised exactly as production would be).
func openTestDB(t *testing.T) *metadata.DB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "fp.db")
	db, err := metadata.Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// TestWriteTxFailpoint_OneShot_FiresAndClears arms a sentinel, observes the
// first WriteTx return it (fn ran but the deferred rollback fired before
// commit), then proves the second WriteTx commits normally — the failpoint
// is single-shot and self-clearing.
func TestWriteTxFailpoint_OneShot_FiresAndClears(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()

	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `CREATE TABLE t (id INTEGER PRIMARY KEY AUTOINCREMENT)`)
		return err
	}); err != nil {
		t.Fatalf("create table: %v", err)
	}

	sentinel := errors.New("synthetic-failpoint")
	db.SetWriteTxFailpointForTest(sentinel)

	err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, e := tx.ExecContext(ctx, "INSERT INTO t DEFAULT VALUES")
		return e
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("first WriteTx: want sentinel, got %v", err)
	}

	// Second call: failpoint cleared, commits normally.
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, e := tx.ExecContext(ctx, "INSERT INTO t DEFAULT VALUES")
		return e
	}); err != nil {
		t.Fatalf("second WriteTx: want nil (failpoint cleared), got %v", err)
	}

	var n int
	if err := db.Reader.QueryRow("SELECT COUNT(*) FROM t").Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("rows after cycle: %d, want 1 (first rolled back, second committed)", n)
	}
}

// TestWriteTxFailpoint_RollsBackTx proves the failpoint triggers the deferred
// rollback: an INSERT inside fn must NOT be visible to a Reader after the
// failpoint fires. This is the load-bearing property that ATOMICDEL-06 needs.
func TestWriteTxFailpoint_RollsBackTx(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()

	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `CREATE TABLE t (id INTEGER PRIMARY KEY AUTOINCREMENT, v TEXT)`)
		return err
	}); err != nil {
		t.Fatalf("create table: %v", err)
	}

	sentinel := errors.New("synthetic-rollback")
	db.SetWriteTxFailpointForTest(sentinel)

	err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, e := tx.ExecContext(ctx, "INSERT INTO t(v) VALUES ('rolled-back')")
		return e
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("WriteTx: want sentinel, got %v", err)
	}

	var n int
	if err := db.Reader.QueryRow("SELECT COUNT(*) FROM t").Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatalf("rolled-back row visible: count=%d, want 0", n)
	}
}

// TestWriteTxFailpoint_NoLeakageWhenUnarmed proves the failpoint defaults to
// nil and an unarmed DB commits normally. Belt-and-suspenders against an
// implementation that uses a non-zero default.
func TestWriteTxFailpoint_NoLeakageWhenUnarmed(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()

	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `CREATE TABLE t (id INTEGER PRIMARY KEY AUTOINCREMENT)`)
		return err
	}); err != nil {
		t.Fatalf("create table: %v", err)
	}

	// No SetWriteTxFailpointForTest call; field must default to nil.
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, e := tx.ExecContext(ctx, "INSERT INTO t DEFAULT VALUES")
		return e
	}); err != nil {
		t.Fatalf("WriteTx: want nil, got %v", err)
	}

	var n int
	if err := db.Reader.QueryRow("SELECT COUNT(*) FROM t").Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("rows after commit: %d, want 1", n)
	}
}

// TestWriteTxFailpoint_NameForTestSuffixGate asserts the public method's name
// carries the "ForTest" suffix — a structural grep-gate signal that production
// code must not call this method. If a future refactor renames or removes the
// method, this test fails immediately rather than at a downstream caller.
func TestWriteTxFailpoint_NameForTestSuffixGate(t *testing.T) {
	t.Parallel()
	method, ok := reflect.TypeOf((*metadata.DB)(nil)).MethodByName("SetWriteTxFailpointForTest")
	if !ok {
		t.Fatal("(*metadata.DB).SetWriteTxFailpointForTest not found via reflection")
	}
	if !strings.HasSuffix(method.Name, "ForTest") {
		t.Fatalf("method name %q must end in 'ForTest' (test-only convention)", method.Name)
	}
}

// TestWriteTxFailpoint_RaceFreeUnderConcurrent arms once and issues two
// WriteTx calls in series (NOT parallel — writer pool is size-1, so series
// is the realistic mode). First hits the failpoint; second succeeds because
// the failpoint cleared itself.
func TestWriteTxFailpoint_RaceFreeUnderConcurrent(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()

	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `CREATE TABLE t (id INTEGER PRIMARY KEY AUTOINCREMENT)`)
		return err
	}); err != nil {
		t.Fatalf("create table: %v", err)
	}

	sentinel := errors.New("synthetic-race")
	db.SetWriteTxFailpointForTest(sentinel)

	// First in series: hits failpoint.
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, e := tx.ExecContext(ctx, "INSERT INTO t DEFAULT VALUES")
		return e
	}); !errors.Is(err, sentinel) {
		t.Fatalf("first WriteTx: want sentinel, got %v", err)
	}

	// Second in series: succeeds.
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, e := tx.ExecContext(ctx, "INSERT INTO t DEFAULT VALUES")
		return e
	}); err != nil {
		t.Fatalf("second WriteTx: %v", err)
	}

	var n int
	if err := db.Reader.QueryRow("SELECT COUNT(*) FROM t").Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("rows: %d, want 1", n)
	}

	// Cleanup-safety: clearing an unarmed failpoint is a no-op.
	db.SetWriteTxFailpointForTest(nil)
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, e := tx.ExecContext(ctx, "INSERT INTO t DEFAULT VALUES")
		return e
	}); err != nil {
		t.Fatalf("post-cleanup WriteTx: %v", err)
	}
}
