package metadata_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/metadata/sqlitetest"
)

func TestReposRepo_CreateAndFind(t *testing.T) {
	db := sqlitetest.New(t)
	ctx := context.Background()
	pid := seedProject(t, db, "p")

	r := metadata.NewReposRepo(db)
	id, err := r.Create(ctx, pid, "docker", "r1", "", nil, nil, nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := r.FindByTriple(ctx, pid, "docker", "r1")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != id || got.AutoScan != true || got.BlockOnSeverity != "none" {
		t.Fatalf("defaults mismatch: %+v", got)
	}
}

func TestReposRepo_RejectsInvalidType(t *testing.T) {
	db := sqlitetest.New(t)
	ctx := context.Background()
	pid := seedProject(t, db, "p")
	r := metadata.NewReposRepo(db)
	_, err := r.Create(ctx, pid, "bogus", "r", "", nil, nil, nil)
	if err == nil {
		t.Fatalf("expected CHECK-constraint error")
	}
	if !strings.Contains(err.Error(), "CHECK") && !strings.Contains(err.Error(), "constraint") {
		t.Fatalf("unexpected: %v", err)
	}
}

func TestReposRepo_UniqueWithinProjectType(t *testing.T) {
	db := sqlitetest.New(t)
	ctx := context.Background()
	pid := seedProject(t, db, "p")
	r := metadata.NewReposRepo(db)
	if _, err := r.Create(ctx, pid, "docker", "r", "", nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Create(ctx, pid, "docker", "r", "", nil, nil, nil); err == nil {
		t.Fatalf("expected unique error")
	}
	// Same name in a different type is fine.
	if _, err := r.Create(ctx, pid, "rpm", "r", "", nil, nil, nil); err != nil {
		t.Fatalf("same-name different-type failed: %v", err)
	}
}

func TestReposRepo_SoftDeleteListExcludes(t *testing.T) {
	db := sqlitetest.New(t)
	ctx := context.Background()
	pid := seedProject(t, db, "p")
	r := metadata.NewReposRepo(db)
	id, err := r.Create(ctx, pid, "docker", "r1", "", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.SoftDelete(ctx, id); err != nil {
		t.Fatal(err)
	}
	list, err := r.ListByProject(ctx, pid)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("expected empty, got %+v", list)
	}
	_, err = r.FindByTriple(ctx, pid, "docker", "r1")
	if !errors.Is(err, metadata.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestReposRepo_OptionalArgsHonored(t *testing.T) {
	db := sqlitetest.New(t)
	ctx := context.Background()
	pid := seedProject(t, db, "p")
	r := metadata.NewReposRepo(db)
	autoScan := false
	bos := "high"
	pr := true
	id, err := r.Create(ctx, pid, "helm", "h1", "d", &autoScan, &bos, &pr)
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.FindByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.AutoScan != false || got.BlockOnSeverity != "high" || got.PublicRead != true {
		t.Fatalf("optional args not honored: %+v", got)
	}
}
