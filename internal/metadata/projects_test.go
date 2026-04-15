package metadata_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/metadata/sqlitetest"
)

func TestProjectsRepo_CreateAndFind(t *testing.T) {
	db := sqlitetest.New(t)
	r := metadata.NewProjectsRepo(db)
	ctx := context.Background()

	id, err := r.Create(ctx, "dxc", "desc")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if id <= 0 {
		t.Fatalf("expected id>0, got %d", id)
	}
	got, err := r.FindByName(ctx, "dxc")
	if err != nil {
		t.Fatalf("find by name: %v", err)
	}
	if got.Name != "dxc" || got.DescriptionMD != "desc" {
		t.Fatalf("unexpected: %+v", got)
	}
}

func TestProjectsRepo_UniqueName(t *testing.T) {
	db := sqlitetest.New(t)
	r := metadata.NewProjectsRepo(db)
	ctx := context.Background()
	if _, err := r.Create(ctx, "p1", ""); err != nil {
		t.Fatal(err)
	}
	_, err := r.Create(ctx, "p1", "")
	if err == nil {
		t.Fatalf("expected duplicate error")
	}
	if !strings.Contains(strings.ToUpper(err.Error()), "UNIQUE") && !strings.Contains(err.Error(), "constraint") {
		t.Fatalf("unexpected err: %v", err)
	}
}

func TestProjectsRepo_SoftDelete(t *testing.T) {
	db := sqlitetest.New(t)
	r := metadata.NewProjectsRepo(db)
	ctx := context.Background()
	id, err := r.Create(ctx, "p1", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.SoftDelete(ctx, id); err != nil {
		t.Fatal(err)
	}
	_, err = r.FindByName(ctx, "p1")
	if !errors.Is(err, metadata.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestProjectsRepo_ListAll(t *testing.T) {
	db := sqlitetest.New(t)
	r := metadata.NewProjectsRepo(db)
	ctx := context.Background()
	for _, n := range []string{"b", "a", "c"} {
		if _, err := r.Create(ctx, n, ""); err != nil {
			t.Fatal(err)
		}
	}
	list, err := r.ListAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 || list[0].Name != "a" || list[2].Name != "c" {
		t.Fatalf("unexpected order: %+v", list)
	}
}
