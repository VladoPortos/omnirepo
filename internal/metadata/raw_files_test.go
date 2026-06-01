package metadata_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/vladoportos/omnirepo/internal/metadata"
	"github.com/vladoportos/omnirepo/internal/metadata/sqlitetest"
)

// seedRawRepo creates a project + a raw repo and returns the repo id.
func seedRawRepo(t *testing.T, db *metadata.DB) int64 {
	t.Helper()
	pid := seedProject(t, db, "p-raw")
	r := metadata.NewReposRepo(db)
	id, err := r.Create(context.Background(), pid, "raw", "r1", "", nil, nil, nil)
	if err != nil {
		t.Fatalf("seedRawRepo: %v", err)
	}
	return id
}

func TestPhase2RawFilesMigration(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	var name string
	if err := db.Reader.QueryRow(
		`SELECT name FROM sqlite_master WHERE type='table' AND name='raw_files'`,
	).Scan(&name); err != nil {
		t.Fatalf("raw_files table missing: %v", err)
	}
	// Also the modified-at index.
	if err := db.Reader.QueryRow(
		`SELECT name FROM sqlite_master WHERE type='index' AND name='idx_raw_files_modified'`,
	).Scan(&name); err != nil {
		t.Fatalf("idx_raw_files_modified missing: %v", err)
	}
	// schema_migrations recorded.
	var n int
	if err := db.Reader.QueryRow(
		`SELECT COUNT(*) FROM schema_migrations WHERE name='006_raw_files'`,
	).Scan(&n); err != nil || n != 1 {
		t.Fatalf("006_raw_files not recorded: n=%d err=%v", n, err)
	}
}

func TestRawFilesRepo_InsertGetDelete(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()
	repoID := seedRawRepo(t, db)
	r := metadata.NewRawFilesRepo(db)

	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		return r.Insert(ctx, tx, repoID, "assets/logo.png", 1234, "image/png", "sha256:deadbeef")
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	got, found, err := r.Get(ctx, repoID, "assets/logo.png")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !found {
		t.Fatal("expected found=true")
	}
	if got.Path != "assets/logo.png" || got.SizeBytes != 1234 || got.MIME != "image/png" || got.SHA256 != "sha256:deadbeef" {
		t.Fatalf("unexpected row: %+v", got)
	}
	if got.Modified.IsZero() {
		t.Fatal("modified should be set by DEFAULT CURRENT_TIMESTAMP")
	}

	// Missing row → found=false.
	_, found, err = r.Get(ctx, repoID, "nope.txt")
	if err != nil {
		t.Fatalf("get missing: %v", err)
	}
	if found {
		t.Fatal("expected found=false on missing path")
	}

	// Delete.
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		return r.Delete(ctx, tx, repoID, "assets/logo.png")
	}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	_, found, err = r.Get(ctx, repoID, "assets/logo.png")
	if err != nil {
		t.Fatalf("get after delete: %v", err)
	}
	if found {
		t.Fatal("expected found=false after delete")
	}
}

func TestRawFilesRepo_UpsertUpdatesSizeAndModified(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()
	repoID := seedRawRepo(t, db)
	r := metadata.NewRawFilesRepo(db)

	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		return r.Insert(ctx, tx, repoID, "a/b.txt", 10, "text/plain", "sha256:aaa")
	}); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	first, _, err := r.Get(ctx, repoID, "a/b.txt")
	if err != nil {
		t.Fatalf("get first: %v", err)
	}
	// Wait long enough for CURRENT_TIMESTAMP (second precision) to advance.
	time.Sleep(1100 * time.Millisecond)

	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		return r.Insert(ctx, tx, repoID, "a/b.txt", 99, "application/octet-stream", "sha256:bbb")
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	second, _, err := r.Get(ctx, repoID, "a/b.txt")
	if err != nil {
		t.Fatalf("get second: %v", err)
	}
	if second.SizeBytes != 99 || second.MIME != "application/octet-stream" || second.SHA256 != "sha256:bbb" {
		t.Fatalf("upsert did not refresh fields: %+v", second)
	}
	if !second.Modified.After(first.Modified) {
		t.Fatalf("modified should advance on upsert: first=%v second=%v", first.Modified, second.Modified)
	}

	// Still exactly one row.
	var n int
	if err := db.Reader.QueryRow(
		`SELECT COUNT(*) FROM raw_files WHERE repo_id=? AND path=?`, repoID, "a/b.txt",
	).Scan(&n); err != nil || n != 1 {
		t.Fatalf("expected 1 row, n=%d err=%v", n, err)
	}
}

func TestRawFilesRepo_ListDirDirectChildrenOnly(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()
	repoID := seedRawRepo(t, db)
	r := metadata.NewRawFilesRepo(db)

	paths := []string{
		"a.txt",                    // root
		"assets/logo.png",          // under assets/
		"assets/readme.md",         // under assets/
		"assets/sub/nested.bin",    // nested, NOT a direct child of assets/
		"assets/sub/deeper/x.yaml", // deeper
	}
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		for _, p := range paths {
			if err := r.Insert(ctx, tx, repoID, p, 1, "", ""); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// ListDir("assets") → only direct children: logo.png, readme.md. "sub" is
	// a directory that contains direct children but is itself NOT a raw_files
	// row (only files are stored), so it should not appear.
	got, err := r.ListDir(ctx, repoID, "assets")
	if err != nil {
		t.Fatalf("listdir: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 direct children, got %d: %+v", len(got), got)
	}
	names := make(map[string]bool)
	for _, row := range got {
		names[row.Path] = true
	}
	if !names["assets/logo.png"] || !names["assets/readme.md"] {
		t.Fatalf("missing expected children: %v", names)
	}
	for _, row := range got {
		if strings.Contains(strings.TrimPrefix(row.Path, "assets/"), "/") {
			t.Fatalf("nested file leaked: %s", row.Path)
		}
	}

	// ListDir("") → only root-level direct children: "a.txt".
	rootChildren, err := r.ListDir(ctx, repoID, "")
	if err != nil {
		t.Fatalf("listdir root: %v", err)
	}
	if len(rootChildren) != 1 || rootChildren[0].Path != "a.txt" {
		t.Fatalf("root ListDir expected [a.txt], got %+v", rootChildren)
	}
}
