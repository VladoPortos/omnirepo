package app_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/dxc-internal/omnirepo/internal/app"
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
