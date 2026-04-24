package metadata_test

import (
	"context"
	"database/sql"
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

// TestProjectsRepo_CreateInTxRollsBackWithMembers pins audit finding #7:
// when CreateInTx + MembersRepo.AddInTx are composed inside a single writer
// tx, a failure in the second step must leave the DB with NO project row
// (no orphan). Pre-fix, Projects.Create and Members.Add were independent
// transactions and a failure in the second left an orphan project.
func TestProjectsRepo_CreateInTxRollsBackWithMembers(t *testing.T) {
	db := sqlitetest.New(t)
	projects := metadata.NewProjectsRepo(db)
	members := metadata.NewMembersRepo(db)
	ctx := context.Background()

	// Compose project insert + membership insert in a single writer tx
	// where the membership step deliberately fails (userID=0 violates the
	// FK to users.id). The whole tx must roll back.
	err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		id, insErr := projects.CreateInTx(ctx, tx, "doomed", "")
		if insErr != nil {
			return insErr
		}
		// userID=9999 does not exist — FK violation aborts the tx.
		return members.AddInTx(ctx, tx, id, 9999, "maintainer")
	})
	if err == nil {
		t.Fatal("expected tx error from FK failure")
	}

	// Project must NOT exist post-rollback.
	if _, ferr := projects.FindByName(ctx, "doomed"); !errors.Is(ferr, metadata.ErrNotFound) {
		t.Fatalf("expected ErrNotFound after rollback, got %v", ferr)
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
