package metadata_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/vladoportos/omnirepo/internal/metadata"
	"github.com/vladoportos/omnirepo/internal/metadata/sqlitetest"
)

func TestVulnerabilities_InsertBatchAndCount(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	seedProjectRepo(t, db)
	scans := metadata.NewScansRepo(db)
	vrepo := metadata.NewVulnerabilitiesRepo(db)
	ctx := context.Background()

	var scanID int64
	_ = db.WriteTx(ctx, func(tx *sql.Tx) error {
		var err error
		scanID, err = scans.Enqueue(ctx, tx, 1, "docker", "sha256:v")
		return err
	})

	batch := []metadata.Vuln{
		{CVEID: "CVE-2026-0001", Severity: "HIGH", PackageName: "openssl"},
		{CVEID: "CVE-2026-0002", Severity: "CRITICAL", PackageName: "glibc"},
	}
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		return vrepo.InsertBatch(ctx, tx, scanID, batch, 1000)
	}); err != nil {
		t.Fatalf("insert batch: %v", err)
	}

	n, err := vrepo.CountByScan(ctx, scanID)
	if err != nil || n != 2 {
		t.Fatalf("count=%d err=%v", n, err)
	}
}

func TestVulnerabilities_InsertBatchCapEnforced(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	seedProjectRepo(t, db)
	scans := metadata.NewScansRepo(db)
	vrepo := metadata.NewVulnerabilitiesRepo(db)
	ctx := context.Background()
	var scanID int64
	_ = db.WriteTx(ctx, func(tx *sql.Tx) error {
		var err error
		scanID, err = scans.Enqueue(ctx, tx, 1, "docker", "sha256:v")
		return err
	})

	big := make([]metadata.Vuln, 11)
	for i := range big {
		big[i] = metadata.Vuln{CVEID: "CVE-X", Severity: "LOW", PackageName: "pkg"}
	}
	err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		return vrepo.InsertBatch(ctx, tx, scanID, big, 10)
	})
	if !errors.Is(err, metadata.ErrVulnBatchTooLarge) {
		t.Fatalf("want ErrVulnBatchTooLarge, got %v", err)
	}
	// Because tx rolls back, no rows were inserted.
	n, _ := vrepo.CountByScan(ctx, scanID)
	if n != 0 {
		t.Fatalf("want 0 rows after rejected batch, got %d", n)
	}
}

func TestVulnerabilities_DeleteByScan(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	seedProjectRepo(t, db)
	scans := metadata.NewScansRepo(db)
	vrepo := metadata.NewVulnerabilitiesRepo(db)
	ctx := context.Background()
	var scanID int64
	_ = db.WriteTx(ctx, func(tx *sql.Tx) error {
		var err error
		scanID, err = scans.Enqueue(ctx, tx, 1, "docker", "sha256:q")
		return err
	})
	_ = db.WriteTx(ctx, func(tx *sql.Tx) error {
		return vrepo.InsertBatch(ctx, tx, scanID, []metadata.Vuln{
			{CVEID: "CVE-1", Severity: "LOW", PackageName: "a"},
			{CVEID: "CVE-2", Severity: "LOW", PackageName: "b"},
		}, 0)
	})
	_ = db.WriteTx(ctx, func(tx *sql.Tx) error {
		return vrepo.DeleteByScan(ctx, tx, scanID)
	})
	n, _ := vrepo.CountByScan(ctx, scanID)
	if n != 0 {
		t.Fatalf("want 0 after delete, got %d", n)
	}
}
