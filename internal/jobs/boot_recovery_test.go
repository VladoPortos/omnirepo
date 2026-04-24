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

// v1.5 Phase 5 Plan 04 — helm-specific boot-recovery sweep (HELMRETRY-03,
// D-02, D-03b). Stale kind='helm_sync' rows terminate at status='failed'
// with a null-counts partial-log JSON BEFORE the generic RecoverStale
// sweep runs (Pitfall 4 ordering). Non-helm kinds continue through the
// existing generic path unchanged.

// TestBootRecovery_HelmSync_StaleRunningToFailed seeds a single stale
// helm_sync row and asserts the boot-recovery helm branch terminally
// fails it with the null-counts partial-log JSON.
func TestBootRecovery_HelmSync_StaleRunningToFailed(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()

	_, err := db.Writer.ExecContext(ctx, `
		INSERT INTO sync_jobs(kind, status, leased_by, leased_at)
		VALUES ('helm_sync', 'running', 'w1', datetime('now','-11 minutes'))
	`)
	if err != nil {
		t.Fatalf("seed helm_sync: %v", err)
	}

	rep, err := jobs.RecoverStuckJobs(ctx, db)
	if err != nil {
		t.Fatalf("RecoverStuckJobs: %v", err)
	}
	if rep.HelmFailedTerminal != 1 {
		t.Errorf("HelmFailedTerminal=%d want 1", rep.HelmFailedTerminal)
	}
	if rep.SyncRecovered != 0 {
		t.Errorf("SyncRecovered=%d want 0 (helm sweep already consumed the row)", rep.SyncRecovered)
	}

	var status, logJSON, lastErr string
	err = db.Reader.QueryRow(`SELECT status, log, last_error FROM sync_jobs WHERE kind='helm_sync'`).
		Scan(&status, &logJSON, &lastErr)
	if err != nil {
		t.Fatalf("select helm row: %v", err)
	}
	if status != "failed" {
		t.Errorf("helm status=%q want failed", status)
	}
	const wantLog = `{"partial":true,"files_persisted":null,"files_expected":null}`
	if logJSON != wantLog {
		t.Errorf("helm log=%q want %q", logJSON, wantLog)
	}
	const wantLastErr = "stale running row terminated at boot"
	if lastErr != wantLastErr {
		t.Errorf("helm last_error=%q want %q", lastErr, wantLastErr)
	}
}

// TestBootRecovery_NonHelm_StaleRunningToPending seeds two stale non-helm
// rows (pypi_sync + rpm_sync) and asserts the generic sweep still routes
// them to 'pending' — the D-02 scope boundary at the boot path.
func TestBootRecovery_NonHelm_StaleRunningToPending(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()

	_, err := db.Writer.ExecContext(ctx, `
		INSERT INTO sync_jobs(kind, status, leased_by, leased_at)
		VALUES ('pypi_sync', 'running', 'w1', datetime('now','-11 minutes'))
	`)
	if err != nil {
		t.Fatalf("seed pypi_sync: %v", err)
	}
	_, err = db.Writer.ExecContext(ctx, `
		INSERT INTO sync_jobs(kind, status, leased_by, leased_at)
		VALUES ('rpm_sync', 'running', 'w2', datetime('now','-11 minutes'))
	`)
	if err != nil {
		t.Fatalf("seed rpm_sync: %v", err)
	}

	rep, err := jobs.RecoverStuckJobs(ctx, db)
	if err != nil {
		t.Fatalf("RecoverStuckJobs: %v", err)
	}
	if rep.HelmFailedTerminal != 0 {
		t.Errorf("HelmFailedTerminal=%d want 0 (no helm rows seeded)", rep.HelmFailedTerminal)
	}
	if rep.SyncRecovered != 2 {
		t.Errorf("SyncRecovered=%d want 2 (both non-helm rows)", rep.SyncRecovered)
	}

	// Both non-helm rows → pending; neither has the helm partial-log payload.
	for _, kind := range []string{"pypi_sync", "rpm_sync"} {
		var status, logJSON string
		err := db.Reader.QueryRow(
			`SELECT status, COALESCE(log,'') FROM sync_jobs WHERE kind=?`, kind,
		).Scan(&status, &logJSON)
		if err != nil {
			t.Fatalf("select %s: %v", kind, err)
		}
		if status != "pending" {
			t.Errorf("%s status=%q want pending", kind, status)
		}
		if logJSON == `{"partial":true,"files_persisted":null,"files_expected":null}` {
			t.Errorf("%s log=%q — helm partial payload leaked onto non-helm row", kind, logJSON)
		}
	}
}

// TestBootRecovery_MixedKinds_ScopeBoundary seeds one helm_sync + one
// pypi_sync + one oci_sync stale running row. Asserts helm → failed and
// non-helm → pending, covering ordering + scope boundary together.
func TestBootRecovery_MixedKinds_ScopeBoundary(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	ctx := context.Background()

	_, err := db.Writer.ExecContext(ctx, `
		INSERT INTO sync_jobs(kind, status, leased_by, leased_at)
		VALUES ('helm_sync', 'running', 'w1', datetime('now','-11 minutes'))
	`)
	if err != nil {
		t.Fatalf("seed helm_sync: %v", err)
	}
	_, err = db.Writer.ExecContext(ctx, `
		INSERT INTO sync_jobs(kind, status, leased_by, leased_at)
		VALUES ('pypi_sync', 'running', 'w2', datetime('now','-11 minutes'))
	`)
	if err != nil {
		t.Fatalf("seed pypi_sync: %v", err)
	}
	_, err = db.Writer.ExecContext(ctx, `
		INSERT INTO sync_jobs(kind, status, leased_by, leased_at)
		VALUES ('oci_sync', 'running', 'w3', datetime('now','-11 minutes'))
	`)
	if err != nil {
		t.Fatalf("seed oci_sync: %v", err)
	}

	rep, err := jobs.RecoverStuckJobs(ctx, db)
	if err != nil {
		t.Fatalf("RecoverStuckJobs: %v", err)
	}
	if rep.HelmFailedTerminal != 1 {
		t.Errorf("HelmFailedTerminal=%d want 1", rep.HelmFailedTerminal)
	}
	if rep.SyncRecovered != 2 {
		t.Errorf("SyncRecovered=%d want 2 (pypi+oci)", rep.SyncRecovered)
	}

	var helmStatus, helmLog string
	err = db.Reader.QueryRow(
		`SELECT status, log FROM sync_jobs WHERE kind='helm_sync'`,
	).Scan(&helmStatus, &helmLog)
	if err != nil {
		t.Fatalf("select helm row: %v", err)
	}
	if helmStatus != "failed" {
		t.Errorf("helm status=%q want failed", helmStatus)
	}
	const wantLog = `{"partial":true,"files_persisted":null,"files_expected":null}`
	if helmLog != wantLog {
		t.Errorf("helm log=%q want %q", helmLog, wantLog)
	}

	for _, kind := range []string{"pypi_sync", "oci_sync"} {
		var status string
		err := db.Reader.QueryRow(
			`SELECT status FROM sync_jobs WHERE kind=?`, kind,
		).Scan(&status)
		if err != nil {
			t.Fatalf("select %s: %v", kind, err)
		}
		if status != "pending" {
			t.Errorf("%s status=%q want pending", kind, status)
		}
	}
}
