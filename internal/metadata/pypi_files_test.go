package metadata_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/vladoportos/omnirepo/internal/metadata"
	"github.com/vladoportos/omnirepo/internal/metadata/sqlitetest"
)

func seedPyPIRepo(t *testing.T, db *metadata.DB) int64 {
	t.Helper()
	ctx := context.Background()
	if _, err := db.Writer.ExecContext(ctx, `INSERT INTO projects(name) VALUES ('py-proj')`); err != nil {
		t.Fatalf("project: %v", err)
	}
	res, err := db.Writer.ExecContext(ctx,
		`INSERT INTO repos(project_id,type,name) VALUES (1,'pypi','py-repo')`,
	)
	if err != nil {
		t.Fatalf("repo: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

func TestPyPIFilesRoundTrip(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	repoID := seedPyPIRepo(t, db)
	r := metadata.NewPyPIFilesRepo(db)
	ctx := context.Background()

	p := &metadata.PyPIFile{
		RepoID: repoID, ProjectNormalized: "requests", Version: "2.32.0",
		Filename: "requests-2.32.0-py3-none-any.whl", Kind: "wheel",
		RequiresPython: ">=3.8", SizeBytes: 1000, Digest: "sha256:r1",
		CoreMetadataJSON: `{"name":"requests"}`,
	}
	var id int64
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		v, err := r.Insert(ctx, tx, p)
		id = v
		return err
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	got, err := r.FindByFilename(ctx, repoID, p.Filename)
	if err != nil || got.ID != id {
		t.Fatalf("find: %+v err=%v", got, err)
	}
	if got.CoreMetadataJSON != `{"name":"requests"}` || got.Kind != "wheel" {
		t.Fatalf("round-trip metadata mismatch: %+v", got)
	}
}

func TestPyPIFilesRejectBadKind(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	r := metadata.NewPyPIFilesRepo(db)
	err := db.WriteTx(context.Background(), func(tx *sql.Tx) error {
		_, err := r.Insert(context.Background(), tx, &metadata.PyPIFile{
			RepoID: 1, ProjectNormalized: "x", Version: "1", Filename: "x.tar.gz",
			Kind: "egg", Digest: "d",
		})
		return err
	})
	if err == nil {
		t.Fatal("expected rejection for kind=egg")
	}
}

func TestPyPIFilesListByProject(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	repoID := seedPyPIRepo(t, db)
	r := metadata.NewPyPIFilesRepo(db)
	ctx := context.Background()
	seed := []*metadata.PyPIFile{
		{RepoID: repoID, ProjectNormalized: "requests", Version: "2.32.0", Filename: "a-2.32.0.whl", Kind: "wheel", Digest: "d1"},
		{RepoID: repoID, ProjectNormalized: "requests", Version: "2.31.0", Filename: "a-2.31.0.whl", Kind: "wheel", Digest: "d2"},
		{RepoID: repoID, ProjectNormalized: "click", Version: "8.1.7", Filename: "click-8.1.7.tar.gz", Kind: "sdist", Digest: "d3"},
	}
	_ = db.WriteTx(ctx, func(tx *sql.Tx) error {
		for _, p := range seed {
			if _, err := r.Insert(ctx, tx, p); err != nil {
				return err
			}
		}
		return nil
	})
	files, err := r.ListByProject(ctx, repoID, "requests")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("list len=%d", len(files))
	}
	if files[0].Version != "2.32.0" {
		t.Fatalf("ordering: %+v", files)
	}
	projs, _ := r.ListProjects(ctx, repoID)
	if len(projs) != 2 || projs[0] != "click" || projs[1] != "requests" {
		t.Fatalf("projects: %v", projs)
	}
}

func TestPyPIFilesFindMissing(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	r := metadata.NewPyPIFilesRepo(db)
	if _, err := r.FindByFilename(context.Background(), 99, "nope.whl"); !errors.Is(err, metadata.ErrNotFound) {
		t.Fatalf("want ErrNotFound got %v", err)
	}
}
