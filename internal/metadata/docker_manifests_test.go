package metadata_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/vladoportos/omnirepo/internal/metadata"
	"github.com/vladoportos/omnirepo/internal/metadata/sqlitetest"
)

func seedProjectRepo(t *testing.T, db *metadata.DB) int64 {
	t.Helper()
	ctx := context.Background()
	if _, err := db.Writer.ExecContext(ctx, `INSERT INTO projects(name) VALUES ('p1')`); err != nil {
		t.Fatalf("project: %v", err)
	}
	if _, err := db.Writer.ExecContext(ctx,
		`INSERT INTO repos(project_id,type,name) VALUES (1,'docker','r1')`,
	); err != nil {
		t.Fatalf("repo: %v", err)
	}
	return 1
}

func TestDockerManifests_InsertGet(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	repoID := seedProjectRepo(t, db)
	mf := metadata.NewDockerManifestsRepo(db)
	ctx := context.Background()

	body := []byte("{\"schemaVersion\":2}\n\r")
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		return mf.Insert(ctx, tx, repoID, "sha256:a", "application/vnd.oci.image.manifest.v1+json", body)
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	got, err := mf.GetByDigest(ctx, repoID, "sha256:a")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got == nil {
		t.Fatal("expected manifest")
	}
	if !bytes.Equal(got.Body, body) {
		t.Fatalf("body round-trip differs: got=%q want=%q", got.Body, body)
	}
	if got.SizeBytes != int64(len(body)) {
		t.Fatalf("size mismatch: %d", got.SizeBytes)
	}
}

func TestDockerManifests_InsertIdempotentSameBody(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	repoID := seedProjectRepo(t, db)
	mf := metadata.NewDockerManifestsRepo(db)
	ctx := context.Background()
	body := []byte("{}")
	_ = db.WriteTx(ctx, func(tx *sql.Tx) error {
		return mf.Insert(ctx, tx, repoID, "sha256:x", "m", body)
	})
	err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		return mf.Insert(ctx, tx, repoID, "sha256:x", "m", body)
	})
	if err != nil {
		t.Fatalf("second insert same body should be no-op, got %v", err)
	}
}

func TestDockerManifests_InsertConflictDifferentBody(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	repoID := seedProjectRepo(t, db)
	mf := metadata.NewDockerManifestsRepo(db)
	ctx := context.Background()
	_ = db.WriteTx(ctx, func(tx *sql.Tx) error {
		return mf.Insert(ctx, tx, repoID, "sha256:x", "m", []byte("a"))
	})
	err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		return mf.Insert(ctx, tx, repoID, "sha256:x", "m", []byte("b"))
	})
	if !errors.Is(err, metadata.ErrManifestDigestConflict) {
		t.Fatalf("want ErrManifestDigestConflict, got %v", err)
	}
}

func TestDockerManifests_IncDecDelete(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	repoID := seedProjectRepo(t, db)
	mf := metadata.NewDockerManifestsRepo(db)
	ctx := context.Background()
	_ = db.WriteTx(ctx, func(tx *sql.Tx) error {
		return mf.Insert(ctx, tx, repoID, "sha256:y", "m", []byte("body"))
	})
	_ = db.WriteTx(ctx, func(tx *sql.Tx) error { return mf.IncRef(ctx, tx, repoID, "sha256:y") })
	_ = db.WriteTx(ctx, func(tx *sql.Tx) error { return mf.DecRef(ctx, tx, repoID, "sha256:y") })
	err := db.WriteTx(ctx, func(tx *sql.Tx) error { return mf.DecRef(ctx, tx, repoID, "sha256:y") })
	if !errors.Is(err, metadata.ErrRefCountUnderflow) {
		t.Fatalf("want underflow, got %v", err)
	}
	_ = db.WriteTx(ctx, func(tx *sql.Tx) error { return mf.Delete(ctx, tx, repoID, "sha256:y") })
	got, _ := mf.GetByDigest(ctx, repoID, "sha256:y")
	if got != nil {
		t.Fatalf("expected nil after delete")
	}
}
