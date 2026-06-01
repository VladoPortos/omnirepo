package metadata_test

import (
	"context"
	"testing"

	"github.com/vladoportos/omnirepo/internal/metadata"
	"github.com/vladoportos/omnirepo/internal/metadata/sqlitetest"
)

func TestMembersRepo_AddIsMemberRemove(t *testing.T) {
	db := sqlitetest.New(t)
	ctx := context.Background()
	pid := seedProject(t, db, "p")
	uid := seedUser(t, db, "alice")

	m := metadata.NewMembersRepo(db)
	ok, err := m.IsMember(ctx, pid, uid)
	if err != nil || ok {
		t.Fatalf("expected not member, got ok=%v err=%v", ok, err)
	}
	if err := m.Add(ctx, pid, uid, "maintainer"); err != nil {
		t.Fatal(err)
	}
	ok, err = m.IsMember(ctx, pid, uid)
	if err != nil || !ok {
		t.Fatalf("expected member after add, ok=%v err=%v", ok, err)
	}
	if err := m.Remove(ctx, pid, uid); err != nil {
		t.Fatal(err)
	}
	ok, _ = m.IsMember(ctx, pid, uid)
	if ok {
		t.Fatalf("still member after remove")
	}
}

func TestMembersRepo_ListProjectIDsForUser(t *testing.T) {
	db := sqlitetest.New(t)
	ctx := context.Background()
	p1 := seedProject(t, db, "p1")
	p2 := seedProject(t, db, "p2")
	p3 := seedProject(t, db, "p3")
	uid := seedUser(t, db, "bob")

	m := metadata.NewMembersRepo(db)
	if err := m.Add(ctx, p1, uid, "maintainer"); err != nil {
		t.Fatal(err)
	}
	if err := m.Add(ctx, p3, uid, "maintainer"); err != nil {
		t.Fatal(err)
	}
	_ = p2
	ids, err := m.ListProjectIDsForUser(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 {
		t.Fatalf("expected 2, got %v", ids)
	}
}
