package metadata_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/vladoportos/omnirepo/internal/metadata"
	"github.com/vladoportos/omnirepo/internal/metadata/sqlitetest"
)

func TestDockerTags_UpsertResolveListDelete(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	seedProjectRepo(t, db)
	tags := metadata.NewDockerTagsRepo(db)
	ctx := context.Background()

	// First upsert: no prior.
	var prior string
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		var err error
		prior, err = tags.Upsert(ctx, tx, 1, "", "latest", "sha256:A")
		return err
	}); err != nil {
		t.Fatalf("upsert1: %v", err)
	}
	if prior != "" {
		t.Fatalf("expected empty prior, got %q", prior)
	}

	// Second upsert: returns prior digest.
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		var err error
		prior, err = tags.Upsert(ctx, tx, 1, "", "latest", "sha256:B")
		return err
	}); err != nil {
		t.Fatalf("upsert2: %v", err)
	}
	if prior != "sha256:A" {
		t.Fatalf("expected prior=sha256:A, got %q", prior)
	}

	// Same-digest upsert returns "".
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		var err error
		prior, err = tags.Upsert(ctx, tx, 1, "", "latest", "sha256:B")
		return err
	}); err != nil {
		t.Fatalf("upsert3: %v", err)
	}
	if prior != "" {
		t.Fatalf("expected '' when digest unchanged, got %q", prior)
	}

	// Another tag.
	_ = db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tags.Upsert(ctx, tx, 1, "", "v1", "sha256:C")
		return err
	})

	d, err := tags.Resolve(ctx, 1, "", "latest")
	if err != nil || d != "sha256:B" {
		t.Fatalf("resolve latest=%q err=%v", d, err)
	}
	missing, _ := tags.Resolve(ctx, 1, "", "missing")
	if missing != "" {
		t.Fatalf("expected empty for missing tag")
	}

	list, _ := tags.List(ctx, 1, "")
	if len(list) != 2 || list[0] != "latest" || list[1] != "v1" {
		t.Fatalf("unexpected list: %v", list)
	}

	var gone string
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		var err error
		gone, err = tags.Delete(ctx, tx, 1, "", "latest")
		return err
	}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if gone != "sha256:B" {
		t.Fatalf("expected deleted digest=sha256:B, got %q", gone)
	}

	// Delete missing = ""
	var gone2 string
	_ = db.WriteTx(ctx, func(tx *sql.Tx) error {
		var err error
		gone2, err = tags.Delete(ctx, tx, 1, "", "nope")
		return err
	})
	if gone2 != "" {
		t.Fatalf("expected '' for missing delete, got %q", gone2)
	}
}
