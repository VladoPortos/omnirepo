package metadata_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/metadata/sqlitetest"
)

func TestScans_LeaseMarkDoneLatest(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	seedProjectRepo(t, db)
	scans := metadata.NewScansRepo(db)
	ctx := context.Background()

	var id int64
	_ = db.WriteTx(ctx, func(tx *sql.Tx) error {
		var err error
		id, err = scans.Enqueue(ctx, tx, 1, "docker", "sha256:img")
		return err
	})
	s, ok, err := scans.LeaseOne(ctx, "scan-worker")
	if err != nil || !ok {
		t.Fatalf("lease: %v ok=%v", err, ok)
	}
	if s.ID != id || s.ArtifactKind != "docker" {
		t.Fatalf("unexpected %+v", s)
	}

	summary := `{"critical":0,"high":1,"medium":0,"low":2,"unknown":0}`
	_ = db.WriteTx(ctx, func(tx *sql.Tx) error {
		return scans.MarkDone(ctx, tx, id, summary, "/var/lib/omnirepo/sboms/1.json", "v1.69-2026-04-01")
	})

	got, err := scans.LatestForArtifact(ctx, 1, "docker", "sha256:img")
	if err != nil || got == nil {
		t.Fatalf("latest: %v %+v", err, got)
	}
	if got.SeveritySummaryJSON != summary {
		t.Fatalf("summary mismatch: %q", got.SeveritySummaryJSON)
	}
}

func TestScans_LatestNoRow(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	seedProjectRepo(t, db)
	scans := metadata.NewScansRepo(db)
	got, err := scans.LatestForArtifact(context.Background(), 1, "docker", "nope")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil, got %+v", got)
	}
}

func TestScans_MarkFailedThenPerm(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	seedProjectRepo(t, db)
	scans := metadata.NewScansRepo(db)
	ctx := context.Background()
	var id int64
	_ = db.WriteTx(ctx, func(tx *sql.Tx) error {
		var err error
		id, err = scans.Enqueue(ctx, tx, 1, "raw", "/foo")
		return err
	})
	_, _, _ = scans.LeaseOne(ctx, "w")
	_ = db.WriteTx(ctx, func(tx *sql.Tx) error {
		return scans.MarkFailed(ctx, tx, id, "timeout", time.Now().Add(time.Hour))
	})
	var status string
	_ = db.Reader.QueryRow(`SELECT status FROM scans WHERE id=?`, id).Scan(&status)
	if status != "pending" {
		t.Fatalf("status=%q want pending", status)
	}
	_ = db.WriteTx(ctx, func(tx *sql.Tx) error {
		return scans.MarkPermanentlyFailed(ctx, tx, id, "too many")
	})
	_ = db.Reader.QueryRow(`SELECT status FROM scans WHERE id=?`, id).Scan(&status)
	if status != "failed" {
		t.Fatalf("status=%q want failed", status)
	}
}

func TestScans_RecoverStale(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	seedProjectRepo(t, db)
	scans := metadata.NewScansRepo(db)
	ctx := context.Background()
	_, _ = db.Writer.ExecContext(ctx, `
		INSERT INTO scans(repo_id, artifact_kind, artifact_id, status, leased_by, leased_at)
		VALUES (1,'docker','sha256:z','running','w1', datetime('now','-1 hour'))
	`)
	var n int
	_ = db.WriteTx(ctx, func(tx *sql.Tx) error {
		var err error
		n, err = scans.RecoverStale(ctx, tx, time.Now().Add(-10*time.Minute))
		return err
	})
	if n != 1 {
		t.Fatalf("recovered=%d want 1", n)
	}
}

// Use sql to avoid unused-import errors if repo shrinks later.
var _ = sql.ErrNoRows
