package metadata_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/metadata/sqlitetest"
)

func TestRPMPackagesRoundTrip(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	repoID := seedRPMRepo(t, db)
	r := metadata.NewRPMPackagesRepo(db)
	ctx := context.Background()

	p := &metadata.RPMPackage{
		RepoID: repoID, Name: "nginx", Epoch: 0,
		Version: "1.25.0", Release: "1.el9", Arch: "x86_64",
		Summary: "web server", Description: "HTTP server",
		License: "BSD", URL: "https://nginx.org",
		SourceRPM: "nginx-1.25.0-1.el9.src.rpm",
		SizeBytes: 1234, Digest: "sha256:abc",
		Filename: "nginx-1.25.0-1.el9.x86_64.rpm",
	}
	var id int64
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		v, err := r.Insert(ctx, tx, p)
		id = v
		return err
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	got, err := r.FindByNEVRA(ctx, repoID, "nginx", 0, "1.25.0", "1.el9", "x86_64")
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if got.ID != id || got.Digest != "sha256:abc" || got.Summary != "web server" {
		t.Fatalf("round-trip mismatch: %+v", got)
	}

	// Upsert keeps the id stable and updates digest.
	p.Digest = "sha256:def"
	var id2 int64
	_ = db.WriteTx(ctx, func(tx *sql.Tx) error {
		v, err := r.Insert(ctx, tx, p)
		id2 = v
		return err
	})
	if id2 != id {
		t.Fatalf("upsert changed id: %d -> %d", id, id2)
	}
	got2, _ := r.FindByDigest(ctx, repoID, "sha256:def")
	if got2 == nil || got2.ID != id {
		t.Fatalf("FindByDigest after upsert: %+v", got2)
	}
}

func TestRPMPackagesNEVRAMiss(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	r := metadata.NewRPMPackagesRepo(db)
	_, err := r.FindByNEVRA(context.Background(), 99, "x", 0, "1", "1", "noarch")
	if !errors.Is(err, metadata.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestRPMPackagesListByRepo(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	repoID := seedRPMRepo(t, db)
	r := metadata.NewRPMPackagesRepo(db)
	ctx := context.Background()

	seed := []metadata.RPMPackage{
		{RepoID: repoID, Name: "a", Version: "1", Release: "1", Arch: "x86_64", Digest: "d1", Filename: "a.rpm"},
		{RepoID: repoID, Name: "a", Version: "2", Release: "1", Arch: "x86_64", Digest: "d2", Filename: "a2.rpm"},
		{RepoID: repoID, Name: "b", Version: "1", Release: "1", Arch: "noarch", Digest: "d3", Filename: "b.rpm"},
	}
	_ = db.WriteTx(ctx, func(tx *sql.Tx) error {
		for i := range seed {
			if _, err := r.Insert(ctx, tx, &seed[i]); err != nil {
				return err
			}
		}
		return nil
	})
	list, err := r.ListByRepo(ctx, repoID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("list len=%d", len(list))
	}
	// First row must be "a" version 2 (DESC) before version 1.
	if list[0].Name != "a" || list[0].Version != "2" {
		t.Fatalf("ordering broken: first=%+v", list[0])
	}
}

func TestRPMPackagesDelete(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	repoID := seedRPMRepo(t, db)
	r := metadata.NewRPMPackagesRepo(db)
	ctx := context.Background()
	var id int64
	_ = db.WriteTx(ctx, func(tx *sql.Tx) error {
		v, err := r.Insert(ctx, tx, &metadata.RPMPackage{
			RepoID: repoID, Name: "zsh", Version: "5.9", Release: "1", Arch: "x86_64",
			Digest: "sha256:zz", Filename: "zsh.rpm",
		})
		id = v
		return err
	})
	_ = db.WriteTx(ctx, func(tx *sql.Tx) error { return r.Delete(ctx, tx, id) })
	if _, err := r.FindByDigest(ctx, repoID, "sha256:zz"); !errors.Is(err, metadata.ErrNotFound) {
		t.Fatalf("after delete: want ErrNotFound, got %v", err)
	}
}
