package metadata_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/metadata/sqlitetest"
)

func seedAPTRepo(t *testing.T, db *metadata.DB) int64 {
	t.Helper()
	ctx := context.Background()
	if _, err := db.Writer.ExecContext(ctx, `INSERT INTO projects(name) VALUES ('apt-proj')`); err != nil {
		t.Fatalf("project: %v", err)
	}
	res, err := db.Writer.ExecContext(ctx,
		`INSERT INTO repos(project_id,type,name) VALUES (1,'deb','apt-repo')`,
	)
	if err != nil {
		t.Fatalf("repo: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

func TestAptSuitesInsertIdempotent(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	repoID := seedAPTRepo(t, db)
	r := metadata.NewAptSuitesRepo(db)
	ctx := context.Background()
	var id1, id2 int64
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		v, err := r.Insert(ctx, tx, repoID, "stable", "main", "amd64")
		id1 = v
		return err
	}); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		v, err := r.Insert(ctx, tx, repoID, "stable", "main", "amd64")
		id2 = v
		return err
	}); err != nil {
		t.Fatalf("second: %v", err)
	}
	if id1 != id2 || id1 == 0 {
		t.Fatalf("Insert must be idempotent: %d %d", id1, id2)
	}
}

func TestAptSuitesListAndFind(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	repoID := seedAPTRepo(t, db)
	r := metadata.NewAptSuitesRepo(db)
	ctx := context.Background()
	_ = db.WriteTx(ctx, func(tx *sql.Tx) error {
		return r.InsertBatch(ctx, tx, repoID, []metadata.AptSuite{
			{Suite: "stable", Component: "main", Architecture: "amd64"},
			{Suite: "stable", Component: "main", Architecture: "arm64"},
			{Suite: "stable", Component: "contrib", Architecture: "amd64"},
		})
	})
	list, err := r.ListByRepo(ctx, repoID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("list len=%d want 3", len(list))
	}
	got, err := r.FindByTuple(ctx, repoID, "stable", "contrib", "amd64")
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if got.Component != "contrib" {
		t.Fatalf("find got component=%q", got.Component)
	}
	if _, err := r.FindByTuple(ctx, repoID, "stable", "nope", "amd64"); !errors.Is(err, metadata.ErrNotFound) {
		t.Fatalf("find missing: want ErrNotFound, got %v", err)
	}
}

func TestAptSuitesDelete(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	repoID := seedAPTRepo(t, db)
	r := metadata.NewAptSuitesRepo(db)
	ctx := context.Background()
	var id int64
	_ = db.WriteTx(ctx, func(tx *sql.Tx) error {
		v, err := r.Insert(ctx, tx, repoID, "testing", "main", "amd64")
		id = v
		return err
	})
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		return r.Delete(ctx, tx, id)
	}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	list, _ := r.ListByRepo(ctx, repoID)
	if len(list) != 0 {
		t.Fatalf("after delete list len=%d", len(list))
	}
}
