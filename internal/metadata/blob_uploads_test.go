package metadata_test

import (
	"context"
	"testing"
	"time"

	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/metadata/sqlitetest"
)

func TestBlobUploadsStartCompletePrune(t *testing.T) {
	db := sqlitetest.New(t)
	r := metadata.NewBlobUploadsRepo(db)
	ctx := context.Background()

	const d1 = "sha256:aaaa"
	if err := r.Start(ctx, d1, time.Hour); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Second Start on same digest is either idempotent or a PK conflict —
	// we pick idempotent (upsert on expires_at).
	if err := r.Start(ctx, d1, 2*time.Hour); err != nil {
		t.Fatalf("Start (second, idempotent): %v", err)
	}

	// Insert an expired row
	const d2 = "sha256:bbbb"
	// Start() uses ttl>0 so we shorten via direct write:
	if err := r.Start(ctx, d2, -1*time.Second); err != nil {
		t.Fatalf("Start expired: %v", err)
	}

	n, err := r.PruneExpired(ctx, time.Now())
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if n != 1 {
		t.Fatalf("pruned %d, want 1", n)
	}

	// Complete d1
	if err := r.Complete(ctx, d1); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	// Row count
	var count int
	if err := db.Reader.QueryRowContext(ctx, "SELECT count(*) FROM blob_uploads").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("remaining rows = %d, want 0", count)
	}
}
