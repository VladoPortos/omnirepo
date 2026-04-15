// Package metadata owns the SQLite metadata substrate for OmniRepo.
//
// It implements the D-09/D-10 reader/writer split: two *sql.DB handles against
// the same SQLite file, reader pool sized 8, writer pool sized 1. Every write
// transaction is BEGIN IMMEDIATE (via the modernc.org/sqlite `_txlock=immediate`
// DSN extension, documented in internal/metadata/tx.go) so concurrent writers
// serialize cleanly without ever returning SQLITE_BUSY — pinning pitfall P2.
//
// modernc.org/sqlite is imported here (moving it out of the tools.go pin) and
// registered as driver name "sqlite".
package metadata

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	_ "modernc.org/sqlite" // registers driver "sqlite"
)

// DB wraps the reader/writer *sql.DB pair. Callers use DB.Reader for read
// queries and DB.WriteTx(ctx, fn) for any writes.
type DB struct {
	Reader *sql.DB // SetMaxOpenConns(8)
	Writer *sql.DB // SetMaxOpenConns(1), SetMaxIdleConns(1)

	path string
}

// readerPoolSize is D-10's reader-pool default (N=8).
const readerPoolSize = 8

// Open returns a DB handle backed by the SQLite file (or memory DSN) at path.
// It applies the D-09 pragma list on both pools and verifies the sqlite build
// supports FTS5 and JSON1. It also transparently appends ?_txlock=immediate to
// the DSN so modernc.org/sqlite upgrades every sql.Tx to BEGIN IMMEDIATE (see
// tx.go for the documentation of this extension).
func Open(path string) (*DB, error) {
	dsn := ensureImmediateTxlock(path)

	reader, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("metadata: open reader: %w", err)
	}
	reader.SetMaxOpenConns(readerPoolSize)
	reader.SetMaxIdleConns(readerPoolSize)

	writer, err := sql.Open("sqlite", dsn)
	if err != nil {
		_ = reader.Close()
		return nil, fmt.Errorf("metadata: open writer: %w", err)
	}
	writer.SetMaxOpenConns(1)
	writer.SetMaxIdleConns(1)
	writer.SetConnMaxLifetime(0)

	ctx := context.Background()

	// Apply pragmas on one writer conn first so journal_mode=WAL is switched
	// before any other connection reads the journal header.
	if err := applyOnConn(ctx, writer, applyPragmas); err != nil {
		_ = reader.Close()
		_ = writer.Close()
		return nil, err
	}
	if err := applyOnConn(ctx, writer, checkCompileOptions); err != nil {
		_ = reader.Close()
		_ = writer.Close()
		return nil, err
	}
	// Apply pragmas on a reader conn too — foreign_keys, busy_timeout etc
	// are per-connection. modernc.org/sqlite does not share pragma state
	// across connections.
	if err := applyOnConn(ctx, reader, applyPragmas); err != nil {
		_ = reader.Close()
		_ = writer.Close()
		return nil, err
	}

	return &DB{Reader: reader, Writer: writer, path: path}, nil
}

// Close closes both pools. Safe to call on a nil *DB (no-op) so test cleanup
// stays simple.
func (db *DB) Close() error {
	if db == nil {
		return nil
	}
	var first error
	if db.Reader != nil {
		if err := db.Reader.Close(); err != nil && first == nil {
			first = err
		}
	}
	if db.Writer != nil {
		if err := db.Writer.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// Path returns the DSN the DB was opened with (before ensureImmediateTxlock).
func (db *DB) Path() string { return db.path }

// applyOnConn grabs one connection from pool, runs fn, releases.
func applyOnConn(ctx context.Context, pool *sql.DB, fn func(context.Context, *sql.Conn) error) error {
	conn, err := pool.Conn(ctx)
	if err != nil {
		return fmt.Errorf("metadata: conn: %w", err)
	}
	defer conn.Close()
	return fn(ctx, conn)
}

// ensureImmediateTxlock guarantees the DSN carries `_txlock=immediate`, which
// is modernc.org/sqlite's documented extension for forcing every sql.BeginTx
// (and every db.Begin) to open as BEGIN IMMEDIATE instead of the default
// DEFERRED. This is the cornerstone of pitfall P2's mitigation: without it,
// two concurrent writers trying to promote a deferred tx to a reserved lock
// deadlock into SQLITE_BUSY.
//
// Accepts both bare paths ("/tmp/a.db") and DSNs with existing query strings
// ("file:/tmp/a.db?cache=shared"). Idempotent.
func ensureImmediateTxlock(path string) string {
	const key = "_txlock=immediate"
	if strings.Contains(path, key) {
		return path
	}
	// If path already has query string delimiter, append with &.
	if strings.Contains(path, "?") {
		return path + "&" + key
	}
	return path + "?" + key
}
