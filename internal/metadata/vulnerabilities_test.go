package metadata_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/vladoportos/omnirepo/internal/metadata"
	"github.com/vladoportos/omnirepo/internal/metadata/sqlitetest"
)


// countVulnRows counts vulnerabilities rows for scanID via raw SQL — the
// repo layer deliberately has no production read path for this.
func countVulnRows(t *testing.T, db *metadata.DB, scanID int64) int {
	t.Helper()
	var n int
	if err := db.Reader.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM vulnerabilities WHERE scan_id=?`, scanID,
	).Scan(&n); err != nil {
		t.Fatalf("count vulnerabilities: %v", err)
	}
	return n
}

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

	if n := countVulnRows(t, db, scanID); n != 2 {
		t.Fatalf("count=%d want 2", n)
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
	if n := countVulnRows(t, db, scanID); n != 0 {
		t.Fatalf("want 0 rows after rejected batch, got %d", n)
	}
}

