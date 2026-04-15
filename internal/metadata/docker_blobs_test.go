package metadata_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/metadata/sqlitetest"
)

func TestDockerBlobs_UpsertIncDecTouch(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	repo := metadata.NewDockerBlobsRepo(db)
	ctx := context.Background()

	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		if err := repo.UpsertZeroRef(ctx, tx, "sha256:a", 10); err != nil {
			return err
		}
		// Idempotent: a second upsert is a no-op.
		if err := repo.UpsertZeroRef(ctx, tx, "sha256:a", 10); err != nil {
			return err
		}
		if err := repo.IncRef(ctx, tx, "sha256:a"); err != nil {
			return err
		}
		if err := repo.IncRef(ctx, tx, "sha256:a"); err != nil {
			return err
		}
		return repo.DecRef(ctx, tx, "sha256:a")
	}); err != nil {
		t.Fatalf("tx: %v", err)
	}

	b, err := repo.Stat(ctx, "sha256:a")
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if b == nil || b.RefCount != 1 || b.SizeBytes != 10 {
		t.Fatalf("unexpected state: %+v", b)
	}
}

func TestDockerBlobs_DecRefUnderflow(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	repo := metadata.NewDockerBlobsRepo(db)
	ctx := context.Background()

	_ = db.WriteTx(ctx, func(tx *sql.Tx) error {
		return repo.UpsertZeroRef(ctx, tx, "sha256:b", 5)
	})

	err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		return repo.DecRef(ctx, tx, "sha256:b")
	})
	if !errors.Is(err, metadata.ErrRefCountUnderflow) {
		t.Fatalf("want ErrRefCountUnderflow, got %v", err)
	}

	// Decref on a missing digest also returns underflow.
	err = db.WriteTx(ctx, func(tx *sql.Tx) error {
		return repo.DecRef(ctx, tx, "sha256:missing")
	})
	if !errors.Is(err, metadata.ErrRefCountUnderflow) {
		t.Fatalf("want ErrRefCountUnderflow on missing, got %v", err)
	}
}

func TestDockerBlobs_IncRefMissingRow(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	repo := metadata.NewDockerBlobsRepo(db)
	ctx := context.Background()
	err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		return repo.IncRef(ctx, tx, "sha256:ghost")
	})
	if err == nil {
		t.Fatalf("expected error on missing row")
	}
}

func TestDockerBlobs_GCCandidates(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	repo := metadata.NewDockerBlobsRepo(db)
	ctx := context.Background()

	// Seed one candidate (ref=0, aged) and one referenced blob.
	_ = db.WriteTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO docker_blobs(digest,size_bytes,ref_count,last_touched_at) VALUES (?,?,0,datetime('now','-2 hours'))`,
			"sha256:old", 1,
		); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO docker_blobs(digest,size_bytes,ref_count,last_touched_at) VALUES (?,?,0,datetime('now','-1 seconds'))`,
			"sha256:fresh", 1,
		); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO docker_blobs(digest,size_bytes,ref_count,last_touched_at) VALUES (?,?,3,datetime('now','-2 hours'))`,
			"sha256:live", 1,
		); err != nil {
			return err
		}
		return nil
	})

	cands, err := repo.GCCandidates(ctx, time.Hour)
	if err != nil {
		t.Fatalf("gc candidates: %v", err)
	}
	if len(cands) != 1 || cands[0].Digest != "sha256:old" {
		t.Fatalf("unexpected candidates: %+v", cands)
	}
}

func TestDockerBlobs_Touch(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	repo := metadata.NewDockerBlobsRepo(db)
	ctx := context.Background()
	_ = db.WriteTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO docker_blobs(digest,size_bytes,ref_count,last_touched_at) VALUES (?,?,0,datetime('now','-2 hours'))`,
			"sha256:t", 1,
		); err != nil {
			return err
		}
		return repo.Touch(ctx, tx, "sha256:t")
	})
	b, _ := repo.Stat(ctx, "sha256:t")
	if b == nil || time.Since(b.LastTouchedAt) > time.Minute {
		t.Fatalf("touch did not bump last_touched_at: %+v", b)
	}
}
