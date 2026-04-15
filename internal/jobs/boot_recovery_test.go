package jobs_test

import (
	"context"
	"testing"

	"github.com/dxc-internal/omnirepo/internal/jobs"
	"github.com/dxc-internal/omnirepo/internal/metadata/sqlitetest"
)

func TestRecoverStuckJobs_RependsOldRunning(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()

	// Stuck for 11 min (must be recovered).
	_, err := db.Writer.ExecContext(ctx, `
		INSERT INTO sync_jobs(kind, status, leased_by, leased_at)
		VALUES ('old', 'running', 'w1', datetime('now','-11 minutes'))
	`)
	if err != nil {
		t.Fatalf("seed old: %v", err)
	}
	// Stuck for 5 min (must NOT be recovered).
	_, err = db.Writer.ExecContext(ctx, `
		INSERT INTO sync_jobs(kind, status, leased_by, leased_at)
		VALUES ('young', 'running', 'w2', datetime('now','-5 minutes'))
	`)
	if err != nil {
		t.Fatalf("seed young: %v", err)
	}

	// Insert a project+repo so scans FK is satisfied, then one 11-min-stuck scan.
	var repoID int64
	row := db.Writer.QueryRowContext(ctx, `INSERT INTO projects(name) VALUES('p') RETURNING id`)
	var pid int64
	if err := row.Scan(&pid); err != nil {
		t.Fatalf("insert project: %v", err)
	}
	row = db.Writer.QueryRowContext(ctx, `INSERT INTO repos(project_id,type,name) VALUES(?,'docker','r') RETURNING id`, pid)
	if err := row.Scan(&repoID); err != nil {
		t.Fatalf("insert repo: %v", err)
	}
	_, err = db.Writer.ExecContext(ctx, `
		INSERT INTO scans(repo_id, artifact_kind, artifact_id, status, leased_by, leased_at)
		VALUES (?, 'docker', 'sha256:abc', 'running', 'w3', datetime('now','-15 minutes'))
	`, repoID)
	if err != nil {
		t.Fatalf("seed scan: %v", err)
	}

	rep, err := jobs.RecoverStuckJobs(ctx, db)
	if err != nil {
		t.Fatalf("RecoverStuckJobs: %v", err)
	}
	if rep.SyncRecovered != 1 {
		t.Errorf("SyncRecovered=%d want 1", rep.SyncRecovered)
	}
	if rep.ScansRecovered != 1 {
		t.Errorf("ScansRecovered=%d want 1", rep.ScansRecovered)
	}

	var oldStatus, youngStatus string
	_ = db.Reader.QueryRow(`SELECT status FROM sync_jobs WHERE kind='old'`).Scan(&oldStatus)
	_ = db.Reader.QueryRow(`SELECT status FROM sync_jobs WHERE kind='young'`).Scan(&youngStatus)
	if oldStatus != "pending" {
		t.Errorf("old status=%q want pending", oldStatus)
	}
	if youngStatus != "running" {
		t.Errorf("young status=%q want running (not yet stale)", youngStatus)
	}

	var scanStatus string
	_ = db.Reader.QueryRow(`SELECT status FROM scans WHERE id=1`).Scan(&scanStatus)
	if scanStatus != "pending" {
		t.Errorf("scan status=%q want pending", scanStatus)
	}
}

func TestRecoverStuckJobs_EmptyDB(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	rep, err := jobs.RecoverStuckJobs(context.Background(), db)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if rep.SyncRecovered != 0 || rep.ScansRecovered != 0 {
		t.Errorf("empty-db recovery report = %+v", rep)
	}
}
