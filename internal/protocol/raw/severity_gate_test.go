package raw_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/vladoportos/omnirepo/internal/metadata"
	"github.com/vladoportos/omnirepo/internal/metadata/sqlitetest"
	"github.com/vladoportos/omnirepo/internal/protocol/raw"
	"github.com/vladoportos/omnirepo/internal/scan"
)

type rawGateFixture struct {
	db    *metadata.DB
	repos *metadata.ReposRepo
	scans *metadata.ScansRepo
	cache *scan.SeverityCache
	gate  raw.SeverityGateFn
	rid   int64
}

func newRawGateFixture(t *testing.T, blockOnSeverity string) *rawGateFixture {
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
	rid, err := repos.Create(context.Background(), pid, "raw", "files", "", nil, &bos, nil)
	if err != nil {
		t.Fatal(err)
	}
	cache := scan.NewSeverityCache(0)
	gate := raw.NewSeverityGate(repos, scans, cache, nil)
	return &rawGateFixture{db: db, repos: repos, scans: scans, cache: cache, gate: gate, rid: rid}
}

func (f *rawGateFixture) seedScan(t *testing.T, path, summaryJSON string) {
	t.Helper()
	ctx := context.Background()
	if err := f.db.WriteTx(ctx, func(tx *sql.Tx) error {
		sid, err := f.scans.Enqueue(ctx, tx, f.rid, "raw", path)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE scans SET status='running', leased_by='t', leased_at=CURRENT_TIMESTAMP, started_at=CURRENT_TIMESTAMP WHERE id=?`, sid); err != nil {
			return err
		}
		return f.scans.MarkDone(ctx, tx, sid, summaryJSON, "", "v1")
	}); err != nil {
		t.Fatalf("seed scan: %v", err)
	}
}

func TestRawSeverityGate_None(t *testing.T) {
	f := newRawGateFixture(t, "none")
	blocked, _, _ := f.gate(context.Background(), f.rid, "raw", "/foo.txt")
	if blocked {
		t.Fatal("expected not blocked when threshold=none")
	}
}

func TestRawSeverityGate_BlocksAtThreshold(t *testing.T) {
	f := newRawGateFixture(t, "high")
	f.seedScan(t, "foo.txt", `{"critical":2,"high":1,"medium":0,"low":0,"unknown":0}`)
	blocked, sev, sid := f.gate(context.Background(), f.rid, "raw", "foo.txt")
	if !blocked {
		t.Fatal("expected blocked")
	}
	if sev != "critical" || sid == 0 {
		t.Fatalf("blocked decision wrong: sev=%q sid=%d", sev, sid)
	}
}

func TestRawSeverityGate_AllowsBelow(t *testing.T) {
	f := newRawGateFixture(t, "high")
	f.seedScan(t, "foo.txt", `{"critical":0,"high":0,"medium":1,"low":0,"unknown":0}`)
	blocked, _, _ := f.gate(context.Background(), f.rid, "raw", "foo.txt")
	if blocked {
		t.Fatal("expected not blocked (medium<high)")
	}
}

func TestRawSeverityGate_CacheHitAndInvalidate(t *testing.T) {
	f := newRawGateFixture(t, "high")
	f.seedScan(t, "x", `{"critical":1,"high":0,"medium":0,"low":0,"unknown":0}`)
	if blocked, _, _ := f.gate(context.Background(), f.rid, "raw", "x"); !blocked {
		t.Fatal("expected first call to block")
	}
	// Invalidate cache → fresh DB read still blocks.
	f.cache.Invalidate(f.rid, "raw", "x")
	if blocked, _, _ := f.gate(context.Background(), f.rid, "raw", "x"); !blocked {
		t.Fatal("expected re-block after invalidate")
	}
	// Replace scan with clean summary → invalidate → allow.
	_, _ = f.db.Writer.ExecContext(context.Background(), `DELETE FROM scans`)
	f.seedScan(t, "x", `{"critical":0,"high":0,"medium":0,"low":0,"unknown":0}`)
	f.cache.Invalidate(f.rid, "raw", "x")
	if blocked, _, _ := f.gate(context.Background(), f.rid, "raw", "x"); blocked {
		t.Fatal("expected allow after fresh clean scan")
	}
}
