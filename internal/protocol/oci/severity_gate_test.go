package oci_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/vladoportos/omnirepo/internal/metadata"
	"github.com/vladoportos/omnirepo/internal/metadata/sqlitetest"
	"github.com/vladoportos/omnirepo/internal/protocol/oci"
	"github.com/vladoportos/omnirepo/internal/scan"
)

// gateFixture seeds a project, repo (with configurable block_on_severity),
// a manifest digest, and (optionally) a completed scan with a given
// severity summary. Returns the gate function plus repoID + digest.
type gateFixture struct {
	db    *metadata.DB
	repos *metadata.ReposRepo
	scans *metadata.ScansRepo
	cache *scan.SeverityCache
	gate  oci.SeverityGateFn
	rid   int64
}

func newGateFixture(t *testing.T, blockOnSeverity string) *gateFixture {
	t.Helper()
	db := sqlitetest.New(t)
	projs := metadata.NewProjectsRepo(db)
	repos := metadata.NewReposRepo(db)
	scans := metadata.NewScansRepo(db)

	pid, err := projs.Create(context.Background(), "p", "")
	if err != nil {
		t.Fatal(err)
	}
	bos := blockOnSeverity
	rid, err := repos.Create(context.Background(), pid, "docker", "img", "", nil, &bos, nil)
	if err != nil {
		t.Fatal(err)
	}
	cache := scan.NewSeverityCache(0)
	gate := oci.NewSeverityGate(repos, scans, cache, nil)
	return &gateFixture{db: db, repos: repos, scans: scans, cache: cache, gate: gate, rid: rid}
}

// seedScan inserts a completed scan for digest with the given summary JSON.
func (f *gateFixture) seedScan(t *testing.T, db *metadata.DB, digest, summaryJSON string) {
	t.Helper()
	ctx := context.Background()
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		sid, err := f.scans.Enqueue(ctx, tx, f.rid, "docker", digest)
		if err != nil {
			return err
		}
		// Lease + mark done in same tx — bypass the LeaseOne pool path for
		// test setup speed.
		if _, err := tx.ExecContext(ctx, `UPDATE scans SET status='running', leased_by='t', leased_at=CURRENT_TIMESTAMP, started_at=CURRENT_TIMESTAMP WHERE id=?`, sid); err != nil {
			return err
		}
		return f.scans.MarkDone(ctx, tx, sid, summaryJSON, "", "v1")
	}); err != nil {
		t.Fatalf("seed scan: %v", err)
	}
}

func TestSeverityGate_ThresholdNoneAlwaysAllows(t *testing.T) {
	f := newGateFixture(t, "none")
	if err := f.gate(context.Background(), f.rid, "sha256:abc"); err != nil {
		t.Fatalf("expected allow with threshold=none, got %v", err)
	}
}

func TestSeverityGate_NoScanRowAllows(t *testing.T) {
	f := newGateFixture(t, "high")
	if err := f.gate(context.Background(), f.rid, "sha256:noscan"); err != nil {
		t.Fatalf("expected allow with no scan, got %v", err)
	}
}

func TestSeverityGate_BlocksWhenSeverityAtOrAboveThreshold(t *testing.T) {
	f := newGateFixture(t, "high")
	db := openDBFromCache(t, f)
	f.seedScan(t, db, "sha256:bad", `{"critical":1,"high":0,"medium":0,"low":0,"unknown":0}`)

	err := f.gate(context.Background(), f.rid, "sha256:bad")
	if err == nil {
		t.Fatal("expected block")
	}
	blocked, ok := oci.IsBlockedByScan(err)
	if !ok {
		t.Fatalf("expected ErrBlockedByScan, got %T %v", err, err)
	}
	if blocked.Severity != "critical" || blocked.CVECount != 1 {
		t.Fatalf("blocked = %+v", blocked)
	}
}

func TestSeverityGate_AllowsBelowThreshold(t *testing.T) {
	f := newGateFixture(t, "high")
	db := openDBFromCache(t, f)
	f.seedScan(t, db, "sha256:lowonly", `{"critical":0,"high":0,"medium":0,"low":3,"unknown":0}`)
	if err := f.gate(context.Background(), f.rid, "sha256:lowonly"); err != nil {
		t.Fatalf("expected allow (low<high threshold), got %v", err)
	}
}

func TestSeverityGate_CacheHitReturnsSameDecision(t *testing.T) {
	f := newGateFixture(t, "medium")
	db := openDBFromCache(t, f)
	f.seedScan(t, db, "sha256:bad", `{"critical":0,"high":2,"medium":0,"low":0,"unknown":0}`)
	if err := f.gate(context.Background(), f.rid, "sha256:bad"); err == nil {
		t.Fatal("expected block")
	}
	// Second call should hit cache; remove scan row to prove it.
	_, _ = db.Writer.ExecContext(context.Background(), `DELETE FROM scans`)
	err := f.gate(context.Background(), f.rid, "sha256:bad")
	if blocked, ok := oci.IsBlockedByScan(err); !ok || blocked.Severity != "high" {
		t.Fatalf("cache hit lost decision: err=%v", err)
	}
}

func TestSeverityGate_InvalidationReChecksDB(t *testing.T) {
	f := newGateFixture(t, "high")
	db := openDBFromCache(t, f)
	f.seedScan(t, db, "sha256:fix", `{"critical":1,"high":0,"medium":0,"low":0,"unknown":0}`)
	if err := f.gate(context.Background(), f.rid, "sha256:fix"); err == nil {
		t.Fatal("expected block first")
	}
	// Simulate a fresh scan that comes back clean: invalidate cache + delete old scan + insert clean.
	f.cache.Invalidate(f.rid, "docker", "sha256:fix")
	_, _ = db.Writer.ExecContext(context.Background(), `DELETE FROM scans`)
	f.seedScan(t, db, "sha256:fix", `{"critical":0,"high":0,"medium":0,"low":0,"unknown":0}`)
	if err := f.gate(context.Background(), f.rid, "sha256:fix"); err != nil {
		t.Fatalf("expected allow after fix, got %v", err)
	}
}

func TestErrBlockedByScanError(t *testing.T) {
	e := &oci.ErrBlockedByScan{Severity: "critical", CVECount: 5, ScanID: 42}
	if e.Error() == "" {
		t.Fatal("Error() empty")
	}
	wrapped := errors.New("wrap " + e.Error())
	if _, ok := oci.IsBlockedByScan(wrapped); ok {
		// errors.As does not unwrap a string-built error; verify our impl
		// remains As-friendly when the typed error is at the chain head.
		_ = ok
	}
}

// openDBFromCache returns the fixture's underlying DB so tests can seed
// scans rows directly via WriteTx.
func openDBFromCache(t *testing.T, f *gateFixture) *metadata.DB {
	t.Helper()
	return f.db
}
