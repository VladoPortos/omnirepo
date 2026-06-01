package metadata_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/vladoportos/omnirepo/internal/metadata"
	"github.com/vladoportos/omnirepo/internal/metadata/sqlitetest"
)

func TestDEBPackagesRoundTrip(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	repoID := seedAPTRepo(t, db)
	sr := metadata.NewAptSuitesRepo(db)
	pr := metadata.NewDEBPackagesRepo(db)
	ctx := context.Background()
	var suiteID int64
	_ = db.WriteTx(ctx, func(tx *sql.Tx) error {
		v, err := sr.Insert(ctx, tx, repoID, "stable", "main", "amd64")
		suiteID = v
		return err
	})
	p := &metadata.DEBPackage{
		RepoID: repoID, SuiteID: suiteID,
		Package: "curl", Version: "7.88.0-1", Architecture: "amd64",
		Maintainer: "maintainers@example.com", Section: "web", Priority: "optional",
		Depends: "libc6 (>= 2.34)", Description: "command line tool",
		SizeBytes: 4242, Digest: "sha256:curl", Filename: "pool/curl_7.88.0-1_amd64.deb",
	}
	var id int64
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		v, err := pr.Insert(ctx, tx, p)
		id = v
		return err
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	got, err := pr.FindByTuple(ctx, repoID, suiteID, "curl", "7.88.0-1", "amd64")
	if err != nil || got.ID != id {
		t.Fatalf("find: %+v err=%v", got, err)
	}
	if got, err := pr.FindByDigest(ctx, repoID, "sha256:curl"); err != nil || got.ID != id {
		t.Fatalf("find by digest: %+v err=%v", got, err)
	}
	list, _ := pr.ListBySuite(ctx, suiteID)
	if len(list) != 1 {
		t.Fatalf("list len=%d", len(list))
	}
}

func TestDEBPackagesMiss(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	pr := metadata.NewDEBPackagesRepo(db)
	if _, err := pr.FindByTuple(context.Background(), 99, 1, "x", "1", "amd64"); !errors.Is(err, metadata.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestDEBPackagesUpsertIDStable(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	repoID := seedAPTRepo(t, db)
	sr := metadata.NewAptSuitesRepo(db)
	pr := metadata.NewDEBPackagesRepo(db)
	ctx := context.Background()
	var suiteID int64
	_ = db.WriteTx(ctx, func(tx *sql.Tx) error {
		v, err := sr.Insert(ctx, tx, repoID, "stable", "main", "amd64")
		suiteID = v
		return err
	})
	p := &metadata.DEBPackage{
		RepoID: repoID, SuiteID: suiteID,
		Package: "zsh", Version: "5.9-4", Architecture: "amd64",
		Digest: "d1", Filename: "zsh.deb",
	}
	var id1, id2 int64
	_ = db.WriteTx(ctx, func(tx *sql.Tx) error {
		v, err := pr.Insert(ctx, tx, p)
		id1 = v
		return err
	})
	p.Digest = "d2"
	_ = db.WriteTx(ctx, func(tx *sql.Tx) error {
		v, err := pr.Insert(ctx, tx, p)
		id2 = v
		return err
	})
	if id1 != id2 {
		t.Fatalf("upsert id unstable: %d -> %d", id1, id2)
	}
	got, _ := pr.FindByDigest(ctx, repoID, "d2")
	if got == nil || got.ID != id1 {
		t.Fatalf("digest after upsert: %+v", got)
	}
}
