// Package sqlitetest is the canonical per-test SQLite helper every other
// internal/ package uses (D-41). New(t) returns a fresh, isolated in-memory
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

	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/metadata/migrations"
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
	if _, err := migrations.Apply(context.Background(), db.Writer); err != nil {
		_ = db.Close()
		t.Fatalf("sqlitetest.New: migrate: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
