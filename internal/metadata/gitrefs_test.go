package metadata_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"github.com/vladoportos/omnirepo/internal/metadata"
	"github.com/vladoportos/omnirepo/internal/metadata/sqlitetest"
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

// --- ReplaceAllTx explicit tx-scoped variant ---

// TestGitRefs_ReplaceAllTx_AtomicReplace seeds prior ref rows then calls
// ReplaceAllTx inside WriteTx with a replacement set. The commit must be
// atomic — post-commit we see only the replacement set, no leftovers from
// the prior state.
func TestGitRefs_ReplaceAllTx_AtomicReplace(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()
	repoID := seedGitRepo(t, db)
	r := metadata.NewGitRefsRepo(db)

	// Seed 3 prior rows.
	prior := []metadata.GitRef{
		{Name: "refs/heads/main", Target: "sha-main-old", Type: metadata.GitRefBranch},
		{Name: "refs/heads/dev", Target: "sha-dev-old", Type: metadata.GitRefBranch},
		{Name: "refs/tags/v0.9", Target: "sha-tag-old", Type: metadata.GitRefTag},
	}
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		return r.ReplaceAllTx(ctx, tx, repoID, prior)
	}); err != nil {
		t.Fatalf("seed prior: %v", err)
	}

	// Replace with 4 new rows.
	next := []metadata.GitRef{
		{Name: "refs/heads/main", Target: "sha-main-new", Type: metadata.GitRefBranch},
		{Name: "refs/heads/feature", Target: "sha-feat", Type: metadata.GitRefBranch},
		{Name: "refs/tags/v1.0", Target: "sha-v10", Type: metadata.GitRefTag},
		{Name: "HEAD", Target: "refs/heads/main", Type: metadata.GitRefSymbolic},
	}
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		return r.ReplaceAllTx(ctx, tx, repoID, next)
	}); err != nil {
		t.Fatalf("replace_all_tx: %v", err)
	}

	got, err := r.List(ctx, repoID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != len(next) {
		t.Fatalf("want %d refs, got %d: %+v", len(next), len(got), got)
	}
	// Prior refs must be completely gone.
	for _, g := range got {
		if g.Name == "refs/heads/dev" || g.Name == "refs/tags/v0.9" {
			t.Fatalf("prior ref %q should have been pruned", g.Name)
		}
		if g.Name == "refs/heads/main" && g.Target == "sha-main-old" {
			t.Fatalf("refs/heads/main still carries the old target")
		}
	}
}

// TestGitRefs_ReplaceAllTx_RollbackLeavesPriorState pins the writer-tx
// atomicity invariant: when the closure returns a non-nil error AFTER
// ReplaceAllTx ran, the whole transaction rolls back and the prior rows
// remain untouched. This is the load-bearing guarantee: no partial state
// after a failed mid-sync.
func TestGitRefs_ReplaceAllTx_RollbackLeavesPriorState(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()
	repoID := seedGitRepo(t, db)
	r := metadata.NewGitRefsRepo(db)

	prior := []metadata.GitRef{
		{Name: "refs/heads/main", Target: "sha-main", Type: metadata.GitRefBranch},
		{Name: "refs/tags/v1.0", Target: "sha-v10", Type: metadata.GitRefTag},
	}
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		return r.ReplaceAllTx(ctx, tx, repoID, prior)
	}); err != nil {
		t.Fatalf("seed prior: %v", err)
	}

	sentinel := errors.New("deliberate failure after ReplaceAllTx")
	err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		if err := r.ReplaceAllTx(ctx, tx, repoID, []metadata.GitRef{
			{Name: "refs/heads/feature", Target: "never-visible", Type: metadata.GitRefBranch},
		}); err != nil {
			return err
		}
		return sentinel // force rollback
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel error, got %v", err)
	}

	// Prior state must be intact.
	got, err := r.List(ctx, repoID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != len(prior) {
		t.Fatalf("want prior state (%d refs), got %d: %+v", len(prior), len(got), got)
	}
	for _, g := range got {
		if g.Name == "refs/heads/feature" {
			t.Fatalf("rollback failed — refs/heads/feature leaked post-rollback")
		}
	}
}

// TestGitRefs_ReplaceAllTx_EmptySetPrunes pins the prune-only path: calling
// with refs=nil (or empty slice) must DELETE every row for repoID and leave
// the table free of any entry for that repo.
func TestGitRefs_ReplaceAllTx_EmptySetPrunes(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()
	repoID := seedGitRepo(t, db)
	r := metadata.NewGitRefsRepo(db)

	// Seed some rows.
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		return r.ReplaceAllTx(ctx, tx, repoID, []metadata.GitRef{
			{Name: "refs/heads/main", Target: "t1", Type: metadata.GitRefBranch},
			{Name: "refs/heads/dev", Target: "t2", Type: metadata.GitRefBranch},
			{Name: "refs/tags/v1", Target: "t3", Type: metadata.GitRefTag},
		})
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Prune with nil.
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		return r.ReplaceAllTx(ctx, tx, repoID, nil)
	}); err != nil {
		t.Fatalf("prune nil: %v", err)
	}
	got, err := r.List(ctx, repoID)
	if err != nil {
		t.Fatalf("list after nil: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected 0 rows after nil prune, got %d: %+v", len(got), got)
	}

	// Re-seed and prune with empty slice — same outcome.
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		return r.ReplaceAllTx(ctx, tx, repoID, []metadata.GitRef{
			{Name: "refs/heads/only", Target: "x", Type: metadata.GitRefBranch},
		})
	}); err != nil {
		t.Fatalf("re-seed: %v", err)
	}
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		return r.ReplaceAllTx(ctx, tx, repoID, []metadata.GitRef{})
	}); err != nil {
		t.Fatalf("prune empty-slice: %v", err)
	}
	got, _ = r.List(ctx, repoID)
	if len(got) != 0 {
		t.Fatalf("expected 0 rows after empty-slice prune, got %d", len(got))
	}
}

// TestGitRefs_ReplaceAllTx_ColumnOrderMatchesSchema inserts a ref with
// explicit Name/Target/Type values and then SELECTs it back via FindByName,
// asserting each field survives the round-trip. Guards against column-order
// drift in the INSERT statement (e.g. future refactor that accidentally
// swaps target and name).
func TestGitRefs_ReplaceAllTx_ColumnOrderMatchesSchema(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()
	repoID := seedGitRepo(t, db)
	r := metadata.NewGitRefsRepo(db)

	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		return r.ReplaceAllTx(ctx, tx, repoID, []metadata.GitRef{
			{Name: "refs/tags/v9.9.9", Target: "deadbeef0000111122223333444455556666777", Type: metadata.GitRefTag},
		})
	}); err != nil {
		t.Fatalf("replace_all_tx: %v", err)
	}
	got, err := r.FindByName(ctx, repoID, "refs/tags/v9.9.9")
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if got.Name != "refs/tags/v9.9.9" {
		t.Fatalf("Name = %q, want refs/tags/v9.9.9", got.Name)
	}
	if got.Target != "deadbeef0000111122223333444455556666777" {
		t.Fatalf("Target = %q, want the full sha", got.Target)
	}
	if got.Type != metadata.GitRefTag {
		t.Fatalf("Type = %q, want tag", got.Type)
	}
	if got.RepoID != repoID {
		t.Fatalf("RepoID = %d, want %d", got.RepoID, repoID)
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
