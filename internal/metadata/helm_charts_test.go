package metadata_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/vladoportos/omnirepo/internal/metadata"
	"github.com/vladoportos/omnirepo/internal/metadata/sqlitetest"
)

func seedHelmRepo(t *testing.T, db *metadata.DB) int64 {
	t.Helper()
	ctx := context.Background()
	if _, err := db.Writer.ExecContext(ctx, `INSERT INTO projects(name) VALUES ('helm-proj')`); err != nil {
		t.Fatalf("project: %v", err)
	}
	res, err := db.Writer.ExecContext(ctx,
		`INSERT INTO repos(project_id,type,name) VALUES (1,'helm','helm-repo')`,
	)
	if err != nil {
		t.Fatalf("repo: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

func TestHelmChartsRoundTrip(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	repoID := seedHelmRepo(t, db)
	r := metadata.NewHelmChartsRepo(db)
	ctx := context.Background()
	c := &metadata.HelmChart{
		RepoID: repoID, Name: "nginx-ingress", Version: "4.9.0",
		AppVersion: "1.10.0", Description: "ingress controller",
		KeywordsJSON: `["ingress","nginx"]`, MaintainersJSON: `[{"name":"me"}]`,
		SizeBytes: 98765, Digest: "sha256:helm", Filename: "nginx-ingress-4.9.0.tgz",
	}
	var id int64
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		v, err := r.Insert(ctx, tx, c)
		id = v
		return err
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	got, err := r.FindByNameVersion(ctx, repoID, "nginx-ingress", "4.9.0")
	if err != nil || got.ID != id {
		t.Fatalf("find: %+v err=%v", got, err)
	}
	if got.KeywordsJSON != `["ingress","nginx"]` || got.AppVersion != "1.10.0" {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	// JSON defaults when inserted empty.
	c2 := &metadata.HelmChart{
		RepoID: repoID, Name: "bare", Version: "0.1.0",
		Digest: "sha256:bare", Filename: "bare-0.1.0.tgz",
	}
	_ = db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := r.Insert(ctx, tx, c2)
		return err
	})
	got2, _ := r.FindByNameVersion(ctx, repoID, "bare", "0.1.0")
	if got2.KeywordsJSON != "[]" || got2.MaintainersJSON != "[]" {
		t.Fatalf("default JSON not applied: %+v", got2)
	}
}

func TestHelmChartsListByRepo(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	repoID := seedHelmRepo(t, db)
	r := metadata.NewHelmChartsRepo(db)
	ctx := context.Background()
	_ = db.WriteTx(ctx, func(tx *sql.Tx) error {
		for _, c := range []metadata.HelmChart{
			{RepoID: repoID, Name: "a", Version: "0.1.0", Digest: "d1", Filename: "a-0.1.0.tgz"},
			{RepoID: repoID, Name: "a", Version: "0.2.0", Digest: "d2", Filename: "a-0.2.0.tgz"},
			{RepoID: repoID, Name: "b", Version: "1.0.0", Digest: "d3", Filename: "b-1.0.0.tgz"},
		} {
			c := c
			if _, err := r.Insert(ctx, tx, &c); err != nil {
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
		t.Fatalf("len=%d", len(list))
	}
	if list[0].Name != "a" || list[0].Version != "0.2.0" {
		t.Fatalf("order wrong: %+v", list[0])
	}
}

func TestHelmChartsFindMissing(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	r := metadata.NewHelmChartsRepo(db)
	if _, err := r.FindByNameVersion(context.Background(), 99, "x", "1"); !errors.Is(err, metadata.ErrNotFound) {
		t.Fatalf("want ErrNotFound got %v", err)
	}
}
