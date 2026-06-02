// Package sqlitetest is the canonical per-test SQLite helper every other
// internal/ package uses. New(t) returns a fresh, isolated in-memory
// DB with every migration from internal/metadata/migrations applied.
//
// Each call uses a unique file:<uuid>?mode=memory&cache=shared DSN so
// subtests share no state. Cleanup is registered with t.Cleanup, so callers
// do not need to Close explicitly.
package sqlitetest

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/vladoportos/omnirepo/internal/metadata"
	"github.com/vladoportos/omnirepo/internal/metadata/migrations"
)

// New returns a freshly-migrated in-memory *metadata.DB scoped to t's
// lifetime. Fails t if open or migrate errors.
func New(t testing.TB) *metadata.DB {
	t.Helper()
	dsn := "file:" + uuid.NewString() + "?mode=memory&cache=shared"
	db, err := metadata.Open(dsn)
	if err != nil {
		t.Fatalf("sqlitetest.New: open: %v", err)
	}

	// Pin a sentinel connection for the whole test. A mode=memory&cache=shared
	// SQLite database is destroyed the instant its LAST open connection closes
	// — every table vanishes. Under load the reader/writer pools can
	// momentarily drop to zero live connections (e.g. a context-canceled write
	// during shutdown makes database/sql discard the writer's sole conn while
	// no reader conn happens to be open), silently wiping the DB; a later query
	// then opens a fresh, empty database and fails with "no such table". A
	// single pinned connection guarantees the DB is never reclaimed mid-test.
	// Held on the reader pool (size 8) so the size-1 writer pool stays free for
	// migrations and test writes.
	sentinel, err := db.Reader.Conn(context.Background())
	if err != nil {
		_ = db.Close()
		t.Fatalf("sqlitetest.New: sentinel conn: %v", err)
	}

	if _, err := migrations.Apply(context.Background(), db.Writer); err != nil {
		_ = sentinel.Close()
		_ = db.Close()
		t.Fatalf("sqlitetest.New: migrate: %v", err)
	}
	t.Cleanup(func() {
		_ = sentinel.Close()
		_ = db.Close()
	})
	return db
}
