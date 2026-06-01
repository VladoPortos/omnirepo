package app_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vladoportos/omnirepo/internal/app"
	"github.com/vladoportos/omnirepo/internal/metadata/sqlitetest"
)

func TestSeedTrivyDB_NoBakedDir(t *testing.T) {
	dataRoot := t.TempDir()
	// bakedDir does not exist — should be a silent no-op.
	err := app.SeedTrivyDB(context.Background(), dataRoot, filepath.Join(dataRoot, "nonexistent"))
	if err != nil {
		t.Fatalf("expected nil error when baked dir missing, got: %v", err)
	}
	// Target dir should not have been created.
	if _, err := os.Stat(filepath.Join(dataRoot, "trivy", "db")); !os.IsNotExist(err) {
		t.Fatal("expected trivy/db dir to not exist when baked dir is absent")
	}
}

func TestSeedTrivyDB_CopiesOnFirstBoot(t *testing.T) {
	dataRoot := t.TempDir()
	bakedDir := filepath.Join(t.TempDir(), "trivy-db")
	if err := os.MkdirAll(bakedDir, 0755); err != nil {
		t.Fatal(err)
	}
	// Create fake DB files in baked dir.
	for _, name := range []string{"trivy.db", "metadata.json"} {
		if err := os.WriteFile(filepath.Join(bakedDir, name), []byte("data-"+name), 0644); err != nil {
			t.Fatal(err)
		}
	}

	err := app.SeedTrivyDB(context.Background(), dataRoot, bakedDir)
	if err != nil {
		t.Fatalf("seed failed: %v", err)
	}

	// Verify files were copied.
	dbDir := filepath.Join(dataRoot, "trivy", "db")
	for _, name := range []string{"trivy.db", "metadata.json"} {
		data, err := os.ReadFile(filepath.Join(dbDir, name))
		if err != nil {
			t.Fatalf("expected %s to exist: %v", name, err)
		}
		if string(data) != "data-"+name {
			t.Fatalf("expected %q, got %q", "data-"+name, string(data))
		}
	}
}

func TestSeedTrivyDB_SkipsWhenAlreadyPresent(t *testing.T) {
	dataRoot := t.TempDir()
	bakedDir := filepath.Join(t.TempDir(), "trivy-db")
	if err := os.MkdirAll(bakedDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bakedDir, "trivy.db"), []byte("baked"), 0644); err != nil {
		t.Fatal(err)
	}

	// Pre-create target with existing data.
	dbDir := filepath.Join(dataRoot, "trivy", "db")
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dbDir, "trivy.db"), []byte("existing"), 0644); err != nil {
		t.Fatal(err)
	}

	err := app.SeedTrivyDB(context.Background(), dataRoot, bakedDir)
	if err != nil {
		t.Fatalf("seed failed: %v", err)
	}

	// Verify existing data was NOT overwritten.
	data, err := os.ReadFile(filepath.Join(dbDir, "trivy.db"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "existing" {
		t.Fatalf("expected existing data to be preserved, got %q", string(data))
	}
}

// TestRecordBakedTrivyDBMeta_InsertsRow covers F-T13: after SeedTrivyDB has
// copied Trivy's metadata.json onto disk, the app must populate trivy_db_meta
// so the dashboard age widget reads a real timestamp instead of "unknown".
func TestRecordBakedTrivyDBMeta_InsertsRow(t *testing.T) {
	db := sqlitetest.New(t)
	dataRoot := t.TempDir()
	dbDir := filepath.Join(dataRoot, "trivy", "db")
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		t.Fatal(err)
	}
	meta := `{
  "Version":2,
  "NextUpdate":"2026-04-17T18:52:29.285569899Z",
  "UpdatedAt":"2026-04-16T18:52:29.28557025Z",
  "DownloadedAt":"2026-04-18T19:01:08.782790623Z"
}`
	if err := os.WriteFile(filepath.Join(dbDir, "metadata.json"), []byte(meta), 0644); err != nil {
		t.Fatal(err)
	}
	// Size probe: write a small trivy.db so dirSizeBytes returns > 0.
	if err := os.WriteFile(filepath.Join(dbDir, "trivy.db"), []byte("0123456789"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := app.RecordBakedTrivyDBMeta(context.Background(), db.Writer, dataRoot); err != nil {
		t.Fatalf("record: %v", err)
	}

	var version, source, appliedAt string
	var sizeBytes int64
	err := db.Reader.QueryRow(`
		SELECT version, source, size_bytes, applied_at FROM trivy_db_meta
		ORDER BY id DESC LIMIT 1
	`).Scan(&version, &source, &sizeBytes, &appliedAt)
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if source != "baked-in" {
		t.Errorf("source=%q, want baked-in", source)
	}
	if version != "baked-20260416" {
		t.Errorf("version=%q, want baked-20260416", version)
	}
	if sizeBytes == 0 {
		t.Error("size_bytes=0, want >0 (sum of trivy.db + metadata.json)")
	}
	if _, perr := time.Parse(time.RFC3339, appliedAt); perr != nil {
		t.Errorf("applied_at not RFC3339: %q (%v)", appliedAt, perr)
	}
}

// TestRecordBakedTrivyDBMeta_Idempotent ensures a second call is a no-op when
// trivy_db_meta already has any row.
func TestRecordBakedTrivyDBMeta_Idempotent(t *testing.T) {
	db := sqlitetest.New(t)
	dataRoot := t.TempDir()
	dbDir := filepath.Join(dataRoot, "trivy", "db")
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dbDir, "metadata.json"),
		[]byte(`{"Version":2,"UpdatedAt":"2026-04-16T00:00:00Z","DownloadedAt":"2026-04-18T00:00:00Z"}`),
		0644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := app.RecordBakedTrivyDBMeta(ctx, db.Writer, dataRoot); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := app.RecordBakedTrivyDBMeta(ctx, db.Writer, dataRoot); err != nil {
		t.Fatalf("second: %v", err)
	}
	var n int
	if err := db.Reader.QueryRow(`SELECT COUNT(*) FROM trivy_db_meta`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("rows=%d, want 1 (second call should be a no-op)", n)
	}
}

// TestRecordBakedTrivyDBMeta_NoMetadataJSON covers the fresh-install path
// where SeedTrivyDB hasn't run (nothing to record). Must be a silent no-op,
// not an error.
func TestRecordBakedTrivyDBMeta_NoMetadataJSON(t *testing.T) {
	db := sqlitetest.New(t)
	dataRoot := t.TempDir()
	if err := app.RecordBakedTrivyDBMeta(context.Background(), db.Writer, dataRoot); err != nil {
		t.Fatalf("expected nil on missing metadata.json, got: %v", err)
	}
	var n int
	_ = db.Reader.QueryRow(`SELECT COUNT(*) FROM trivy_db_meta`).Scan(&n)
	if n != 0 {
		t.Errorf("rows=%d, want 0", n)
	}
}
