package metadata_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/metadata/sqlitetest"
)

func TestBlobUploadSessions_Lifecycle(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	seedProjectRepo(t, db)
	sessions := metadata.NewBlobUploadSessionsRepo(db)
	ctx := context.Background()

	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		return sessions.Create(ctx, tx, "u1", 1, time.Hour)
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	_ = db.WriteTx(ctx, func(tx *sql.Tx) error {
		return sessions.AppendBytes(ctx, tx, "u1", 512)
	})
	_ = db.WriteTx(ctx, func(tx *sql.Tx) error {
		return sessions.AppendBytes(ctx, tx, "u1", 1024)
	})

	got, err := sessions.Lookup(ctx, "u1")
	if err != nil || got == nil {
		t.Fatalf("lookup: %v %+v", err, got)
	}
	if got.BytesSoFar != 1536 {
		t.Fatalf("bytes_so_far=%d want 1536", got.BytesSoFar)
	}

	// AppendBytes to missing uuid errors.
	err = db.WriteTx(ctx, func(tx *sql.Tx) error {
		return sessions.AppendBytes(ctx, tx, "ghost", 1)
	})
	if err == nil {
		t.Fatal("expected error for missing session")
	}

	// Touch + delete.
	_ = db.WriteTx(ctx, func(tx *sql.Tx) error { return sessions.Touch(ctx, tx, "u1") })
	_ = db.WriteTx(ctx, func(tx *sql.Tx) error { return sessions.Delete(ctx, tx, "u1") })
	gone, _ := sessions.Lookup(ctx, "u1")
	if gone != nil {
		t.Fatalf("expected nil after delete")
	}
}

func TestBlobUploadSessions_PruneExpired(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	seedProjectRepo(t, db)
	sessions := metadata.NewBlobUploadSessionsRepo(db)
	ctx := context.Background()
	// Fresh session (expires in 1h) and stale session (expired 1h ago).
	_ = db.WriteTx(ctx, func(tx *sql.Tx) error {
		return sessions.Create(ctx, tx, "fresh", 1, time.Hour)
	})
	_ = db.WriteTx(ctx, func(tx *sql.Tx) error {
		return sessions.Create(ctx, tx, "stale", 1, -time.Hour)
	})

	var removed int
	_ = db.WriteTx(ctx, func(tx *sql.Tx) error {
		var err error
		removed, err = sessions.PruneExpired(ctx, tx, time.Now())
		return err
	})
	if removed != 1 {
		t.Fatalf("pruned=%d want 1", removed)
	}
	s, _ := sessions.Lookup(ctx, "fresh")
	if s == nil {
		t.Fatal("fresh session should still exist")
	}
}
