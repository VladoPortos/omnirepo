package metadata_test

import (
	"context"
	"database/sql"
	"github.com/vladoportos/omnirepo/internal/metadata"
	"github.com/vladoportos/omnirepo/internal/metadata/sqlitetest"
	"testing"
)

func TestDockerBlobOwnershipWipeIsRepoScoped(t *testing.T) {
	db := sqlitetest.New(t)
	ctx := context.Background()
	pid := seedProject(t, db, "ownership")
	repos := metadata.NewReposRepo(db)
	a, err := repos.Create(ctx, pid, "docker", "a", "", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := repos.Create(ctx, pid, "docker", "b", "", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	blobs := metadata.NewDockerBlobsRepo(db)
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		if err := blobs.UpsertZeroRef(ctx, tx, "digest", 1); err != nil {
			return err
		}
		if err := blobs.Link(ctx, tx, a, "digest"); err != nil {
			return err
		}
		return blobs.Link(ctx, tx, b, "digest")
	}); err != nil {
		t.Fatal(err)
	}
	if err := repos.SoftDelete(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err := repos.Restore(ctx, a); err != nil {
		t.Fatal(err)
	}
	if owned, err := blobs.HasInRepo(ctx, a, "digest"); err != nil || !owned {
		t.Fatalf("restore lost ownership: %v %v", owned, err)
	}
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error { _, _, err := repos.WipeDocker(ctx, tx, a); return err }); err != nil {
		t.Fatal(err)
	}
	if owned, err := blobs.HasInRepo(ctx, a, "digest"); err != nil || owned {
		t.Fatalf("wipe left ownership: %v %v", owned, err)
	}
	if owned, err := blobs.HasInRepo(ctx, b, "digest"); err != nil || !owned {
		t.Fatalf("wipe removed other repo ownership: %v %v", owned, err)
	}
}
