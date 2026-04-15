package metadata_test

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/metadata/sqlitetest"
)

func TestSyncJobs_EnqueueLeaseMarkDone(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	jobs := metadata.NewSyncJobsRepo(db)
	ctx := context.Background()

	var id int64
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		var err error
		id, err = jobs.Enqueue(ctx, tx, "pull_external", 0, 0, `{"foo":"bar"}`)
		return err
	}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if id == 0 {
		t.Fatal("expected non-zero id")
	}

	j, ok, err := jobs.LeaseOne(ctx, "worker-1")
	if err != nil || !ok {
		t.Fatalf("lease: %v ok=%v", err, ok)
	}
	if j.ID != id || j.Kind != "pull_external" || j.PayloadJSON != `{"foo":"bar"}` || j.LeaseID != "worker-1" {
		t.Fatalf("unexpected: %+v", j)
	}

	// Second lease finds nothing (only one row, now running).
	_, ok2, _ := jobs.LeaseOne(ctx, "worker-2")
	if ok2 {
		t.Fatalf("expected no lease available after first")
	}

	_ = db.WriteTx(ctx, func(tx *sql.Tx) error {
		return jobs.MarkDone(ctx, tx, id)
	})

	var status string
	_ = db.Reader.QueryRow(`SELECT status FROM sync_jobs WHERE id=?`, id).Scan(&status)
	if status != "done" {
		t.Fatalf("status=%q want done", status)
	}
}

func TestSyncJobs_MarkFailedRetriesAndTermination(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	jobs := metadata.NewSyncJobsRepo(db)
	ctx := context.Background()
	var id int64
	_ = db.WriteTx(ctx, func(tx *sql.Tx) error {
		var err error
		id, err = jobs.Enqueue(ctx, tx, "gc", 0, 0, "{}")
		return err
	})
	_, _, _ = jobs.LeaseOne(ctx, "w")

	future := time.Now().Add(5 * time.Minute)
	_ = db.WriteTx(ctx, func(tx *sql.Tx) error {
		return jobs.MarkFailed(ctx, tx, id, "boom", future)
	})

	// After failure the row is pending again but next_run_at is in the
	// future, so LeaseOne should find nothing.
	_, ok, _ := jobs.LeaseOne(ctx, "w2")
	if ok {
		t.Fatalf("expected no lease before next_run_at")
	}

	_ = db.WriteTx(ctx, func(tx *sql.Tx) error {
		return jobs.MarkPermanentlyFailed(ctx, tx, id, "too many attempts")
	})
	var status string
	_ = db.Reader.QueryRow(`SELECT status FROM sync_jobs WHERE id=?`, id).Scan(&status)
	if status != "failed" {
		t.Fatalf("status=%q want failed", status)
	}
}

func TestSyncJobs_RecoverStale(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	jobs := metadata.NewSyncJobsRepo(db)
	ctx := context.Background()
	// Seed a running job with stale leased_at.
	_, _ = db.Writer.ExecContext(ctx, `
		INSERT INTO sync_jobs(kind, status, leased_by, leased_at)
		VALUES ('x','running','w1',datetime('now','-1 hour'))
	`)
	var n int
	_ = db.WriteTx(ctx, func(tx *sql.Tx) error {
		var err error
		n, err = jobs.RecoverStale(ctx, tx, time.Now().Add(-10*time.Minute))
		return err
	})
	if n != 1 {
		t.Fatalf("recovered=%d want 1", n)
	}
	var status string
	_ = db.Reader.QueryRow(`SELECT status FROM sync_jobs WHERE kind='x'`).Scan(&status)
	if status != "pending" {
		t.Fatalf("status=%q want pending", status)
	}
}

// TestSyncJobsLeaseRace: 8 goroutines contend for one pending row;
// exactly one must win. Proves D-15 lease atomicity under -race.
func TestSyncJobsLeaseRace(t *testing.T) {
	t.Parallel()
	db := sqlitetest.New(t)
	jobs := metadata.NewSyncJobsRepo(db)
	ctx := context.Background()
	// Seed ONE pending row.
	_ = db.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := jobs.Enqueue(ctx, tx, "promote", 0, 0, "{}")
		return err
	})

	const N = 8
	var wg sync.WaitGroup
	wins := make([]*metadata.SyncJob, N)
	errs := make([]error, N)
	start := make(chan struct{})
	for i := 0; i < N; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			j, _, err := jobs.LeaseOne(ctx, fmt.Sprintf("w%d", i))
			wins[i] = j
			errs[i] = err
		}()
	}
	close(start)
	wg.Wait()

	winners := 0
	for i := 0; i < N; i++ {
		if errs[i] != nil {
			t.Fatalf("worker %d err: %v", i, errs[i])
		}
		if wins[i] != nil {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("expected exactly one winner, got %d", winners)
	}
}
