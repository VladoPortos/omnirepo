package metadata_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/metadata/sqlitetest"
)

func seedGitRepo(t *testing.T, db *metadata.DB) int64 {
	t.Helper()
	ctx := context.Background()
	if _, err := db.Writer.ExecContext(ctx, `INSERT INTO projects(name) VALUES ('gitp')`); err != nil {
		t.Fatalf("project: %v", err)
	}
	res, err := db.Writer.ExecContext(ctx, `INSERT INTO repos(project_id,type,name) VALUES (1,'git','g1')`)
	if err != nil {
		t.Fatalf("repo: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

func TestGitRefsReplaceAll_EmptyToPopulated(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()
	repoID := seedGitRepo(t, db)
	r := metadata.NewGitRefsRepo(db)

	refs := []metadata.GitRef{
		{Name: "refs/heads/main", Target: "abc1", Type: metadata.GitRefBranch},
		{Name: "refs/heads/dev", Target: "abc2", Type: metadata.GitRefBranch},
		{Name: "refs/tags/v1.0", Target: "abc3", Type: metadata.GitRefTag},
		{Name: "HEAD", Target: "refs/heads/main", Type: metadata.GitRefSymbolic},
	}
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		return r.ReplaceAll(ctx, tx, repoID, refs)
	}); err != nil {
		t.Fatalf("replaceall: %v", err)
	}
	got, err := r.List(ctx, repoID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != len(refs) {
		t.Fatalf("want %d refs, got %d", len(refs), len(got))
	}
	// ORDER BY name ASC: HEAD, refs/heads/dev, refs/heads/main, refs/tags/v1.0.
	want := []string{"HEAD", "refs/heads/dev", "refs/heads/main", "refs/tags/v1.0"}
	for i, w := range want {
		if got[i].Name != w {
			t.Fatalf("got[%d].Name = %q, want %q", i, got[i].Name, w)
		}
	}
}

func TestGitRefsReplaceAll_RebuildsCompletely(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()
	repoID := seedGitRepo(t, db)
	r := metadata.NewGitRefsRepo(db)

	_ = db.WriteTx(ctx, func(tx *sql.Tx) error {
		return r.ReplaceAll(ctx, tx, repoID, []metadata.GitRef{
			{Name: "refs/heads/main", Target: "t1", Type: metadata.GitRefBranch},
			{Name: "refs/tags/old", Target: "t2", Type: metadata.GitRefTag},
		})
	})
	// Replace with a completely different set.
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		return r.ReplaceAll(ctx, tx, repoID, []metadata.GitRef{
			{Name: "refs/heads/feature", Target: "t3", Type: metadata.GitRefBranch},
		})
	}); err != nil {
		t.Fatalf("replace: %v", err)
	}
	got, _ := r.List(ctx, repoID)
	if len(got) != 1 || got[0].Name != "refs/heads/feature" {
		t.Fatalf("want only refs/heads/feature, got %+v", got)
	}

	// Replace with empty set leaves repo with no refs.
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		return r.ReplaceAll(ctx, tx, repoID, nil)
	}); err != nil {
		t.Fatalf("replace empty: %v", err)
	}
	got, _ = r.List(ctx, repoID)
	if len(got) != 0 {
		t.Fatalf("want 0 refs, got %d", len(got))
	}
}

func TestGitRefsReplaceAll_ChunkedOver200(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()
	repoID := seedGitRepo(t, db)
	r := metadata.NewGitRefsRepo(db)

	// Build 450 refs — forces at least 3 chunks (200 + 200 + 50).
	refs := make([]metadata.GitRef, 450)
	for i := range refs {
		refs[i] = metadata.GitRef{
			Name:   fmt.Sprintf("refs/heads/br-%04d", i),
			Target: fmt.Sprintf("sha-%04d", i),
			Type:   metadata.GitRefBranch,
		}
	}
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		return r.ReplaceAll(ctx, tx, repoID, refs)
	}); err != nil {
		t.Fatalf("replaceall: %v", err)
	}
	got, err := r.List(ctx, repoID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 450 {
		t.Fatalf("want 450, got %d", len(got))
	}
}

func TestGitRefsCheckConstraintRejectsBogusType(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()
	repoID := seedGitRepo(t, db)
	r := metadata.NewGitRefsRepo(db)

	err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		return r.ReplaceAll(ctx, tx, repoID, []metadata.GitRef{
			{Name: "refs/heads/main", Target: "t", Type: metadata.GitRefType("invalid")},
		})
	})
	if err == nil {
		t.Fatal("want CHECK violation, got nil")
	}
}

func TestGitRefsFindByName(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()
	repoID := seedGitRepo(t, db)
	r := metadata.NewGitRefsRepo(db)

	_ = db.WriteTx(ctx, func(tx *sql.Tx) error {
		return r.ReplaceAll(ctx, tx, repoID, []metadata.GitRef{
			{Name: "refs/heads/main", Target: "abc", Type: metadata.GitRefBranch},
		})
	})
	g, err := r.FindByName(ctx, repoID, "refs/heads/main")
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if g.Target != "abc" || g.Type != metadata.GitRefBranch {
		t.Fatalf("mismatch: %+v", g)
	}
	if _, err := r.FindByName(ctx, repoID, "refs/heads/nope"); !errors.Is(err, metadata.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestReposGitMaxPushBytes(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()
	// Seed a project and two repos: one with NULL git_max_push_bytes, one with override.
	if _, err := db.Writer.ExecContext(ctx, `INSERT INTO projects(name) VALUES ('gmax')`); err != nil {
		t.Fatalf("project: %v", err)
	}
	if _, err := db.Writer.ExecContext(ctx, `INSERT INTO repos(project_id,type,name) VALUES (1,'git','r-null')`); err != nil {
		t.Fatalf("repo-null: %v", err)
	}
	if _, err := db.Writer.ExecContext(ctx,
		`INSERT INTO repos(project_id,type,name,git_max_push_bytes) VALUES (1,'git','r-set',?)`,
		int64(1024*1024*100),
	); err != nil {
		t.Fatalf("repo-set: %v", err)
	}

	reposRepo := metadata.NewReposRepo(db)
	rNull, err := reposRepo.FindByTriple(ctx, 1, "git", "r-null")
	if err != nil {
		t.Fatalf("find r-null: %v", err)
	}
	if rNull.GitMaxPushBytes != nil {
		t.Fatalf("r-null GitMaxPushBytes should be nil, got %v", *rNull.GitMaxPushBytes)
	}
	rSet, err := reposRepo.FindByTriple(ctx, 1, "git", "r-set")
	if err != nil {
		t.Fatalf("find r-set: %v", err)
	}
	if rSet.GitMaxPushBytes == nil || *rSet.GitMaxPushBytes != int64(1024*1024*100) {
		t.Fatalf("r-set GitMaxPushBytes mismatch: %+v", rSet.GitMaxPushBytes)
	}
}
