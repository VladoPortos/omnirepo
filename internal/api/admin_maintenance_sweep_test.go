package api_test

// Admin sweep-multipart endpoint tests.
//
// POST /api/v1/admin/maintenance/sweep-multipart — super-admin gated,
// returns JSON `{swept_uploads, cleaned_dirs, duration_ms}`. The handler
// dispatches to the same single SweepOrphanMultiparts function the boot
// goroutine uses (one function, two callers).

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/vladoportos/omnirepo/internal/api"
	"github.com/vladoportos/omnirepo/internal/metadata"
	"github.com/vladoportos/omnirepo/internal/protocol/s3/backend"
	"github.com/vladoportos/omnirepo/internal/storage"
)

// withSweepDeps wires an S3 backend + a non-zero retention into the test
// server's Deps so handleSweepMultipart can dispatch.
func withSweepDeps(retention time.Duration) testServerOpt {
	return func(d *api.Deps) {
		// Build a Backend on the test server's dataRoot, but use the same DB.
		// d.DataRoot is already set by newTestServer above.
		d.S3Backend = backend.New(d.DataRoot, d.DB, storage.NewLocks())
		d.S3MultipartRetention = retention
	}
}

// seedStaleMultipartUpload inserts a stale s3_multipart_uploads row so the
// sweep has something to abort. We bypass the chi-intercept here because
// the test exercises the admin endpoint, not the create path.
func seedStaleMultipartUpload(t *testing.T, db *metadata.DB) string {
	t.Helper()
	ctx := context.Background()

	if _, err := db.Writer.ExecContext(ctx,
		`INSERT INTO users(id, login, email, password_hash) VALUES (2, 'mpu-seed', 'mpu@x', 'x')`,
	); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	res, err := db.Writer.ExecContext(ctx,
		`INSERT INTO projects(name) VALUES ('mpu-seed-proj')`,
	)
	if err != nil {
		t.Fatalf("seed project: %v", err)
	}
	pid, _ := res.LastInsertId()
	if _, err := db.Writer.ExecContext(ctx,
		`INSERT INTO s3_buckets(name, project_id) VALUES ('mpu-seed-bucket', ?)`, pid,
	); err != nil {
		t.Fatalf("seed bucket: %v", err)
	}
	var bid int64
	_ = db.Reader.QueryRowContext(ctx,
		`SELECT id FROM s3_buckets WHERE name='mpu-seed-bucket'`,
	).Scan(&bid)
	res, err = db.Writer.ExecContext(ctx,
		`INSERT INTO s3_access_keys(project_id, label, access_key_id, secret_enc, created_by_user_id)
		 VALUES (?, 'mpu-seed', 'AKIDMPUSEED', X'00', 2)`, pid,
	)
	if err != nil {
		t.Fatalf("seed s3_access_keys: %v", err)
	}
	keyID, _ := res.LastInsertId()
	uploadID := "stale-upload-id-1"
	if _, err := db.Writer.ExecContext(ctx,
		`INSERT INTO s3_multipart_uploads(upload_id, bucket_id, key, initiated_by_s3_key_id, initiated_at, metadata_json)
		 VALUES (?, ?, 'k', ?, '2020-01-01T00:00:00.000Z', '{}')`,
		uploadID, bid, keyID,
	); err != nil {
		t.Fatalf("seed s3_multipart_uploads: %v", err)
	}
	return uploadID
}

// TestSweepMultipart_RequiresSuperAdmin pins ActionTriggerGC gate.
func TestSweepMultipart_RequiresSuperAdmin(t *testing.T) {
	s := newTestServer(t, withSweepDeps(24*time.Hour))

	// Non-super-admin user — must be blocked.
	_, pw := seedTestUser(t, s.db, "norm", "n@x", false, false)
	cookie, _, _ := s.login(t, "norm", pw)
	resp, _ := s.do(t, "POST", "/api/v1/admin/maintenance/sweep-multipart", cookie, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("non-super-admin status=%d, want 403", resp.StatusCode)
	}

	// Super-admin user — must succeed.
	_, sup := seedTestUser(t, s.db, "root", "r@x", true, false)
	cookieSup, _, _ := s.login(t, "root", sup)
	resp, body := s.do(t, "POST", "/api/v1/admin/maintenance/sweep-multipart", cookieSup, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("super-admin status=%d body=%v", resp.StatusCode, body)
	}
}

// TestSweepMultipart_PayloadShape pins JSON envelope shape: three keys,
// numbers for each, duration_ms always positive.
func TestSweepMultipart_PayloadShape(t *testing.T) {
	s := newTestServer(t, withSweepDeps(24*time.Hour))
	_, pw := seedTestUser(t, s.db, "root", "r@x", true, false)
	cookie, _, _ := s.login(t, "root", pw)

	resp, body := s.do(t, "POST", "/api/v1/admin/maintenance/sweep-multipart", cookie, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%v", resp.StatusCode, body)
	}
	for _, k := range []string{"swept_uploads", "cleaned_dirs", "duration_ms"} {
		if _, ok := body[k]; !ok {
			t.Errorf("response missing %q key; got %v", k, body)
		}
	}

	// With no stale rows: counts are zero, duration_ms is non-negative.
	if v, _ := body["swept_uploads"].(float64); v != 0 {
		t.Errorf("swept_uploads = %v, want 0", v)
	}
	if v, _ := body["cleaned_dirs"].(float64); v != 0 {
		t.Errorf("cleaned_dirs = %v, want 0", v)
	}
	if v, _ := body["duration_ms"].(float64); v < 0 {
		t.Errorf("duration_ms = %v, want >= 0", v)
	}
}

// TestSweepMultipart_CountsStaleRows seeds one stale row + drives the
// admin endpoint, asserts swept_uploads == 1.
func TestSweepMultipart_CountsStaleRows(t *testing.T) {
	s := newTestServer(t, withSweepDeps(24*time.Hour))
	_, pw := seedTestUser(t, s.db, "root", "r@x", true, false)
	cookie, _, _ := s.login(t, "root", pw)

	uploadID := seedStaleMultipartUpload(t, s.db)

	resp, body := s.do(t, "POST", "/api/v1/admin/maintenance/sweep-multipart", cookie, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%v", resp.StatusCode, body)
	}
	if v, _ := body["swept_uploads"].(float64); v != 1 {
		t.Errorf("swept_uploads = %v, want 1", v)
	}
	// Verify row gone.
	var n int
	_ = s.db.Reader.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM s3_multipart_uploads WHERE upload_id = ?`, uploadID,
	).Scan(&n)
	if n != 0 {
		t.Errorf("count for stale upload after sweep = %d, want 0", n)
	}
}

// silence "declared and not used" lint if json import is unused.
var _ = json.Marshal
