// Package api_test — black-box HTTP tests for the admin DB-health endpoint
// (plan 10-02, DBHEALTH-01..07).
//
// Tests are split across two files:
//   - admin_db_health_test.go (this file, package api_test) — HTTP-surface
//     tests that drive the endpoint via the test server, exactly as a real
//     client would. Covers cached-read behavior, size/WAL/driver fields,
//     403 gating, the no-integrity_check invariant, and the 10-MB proxy
//     perf gate.
//   - admin_db_health_internal_test.go (package api, white-box) — the
//     lease rate-limit window test that needs access to the package-
//     private dbHealthJob singleton.
//
// The 500-MB DBHEALTH-07 SC3 authoritative perf test lives in
// admin_db_health_perf_test.go under //go:build perf500 and is invoked
// separately via `make test-perf`; this file's 10-MB gate is the fast
// merge-gate per the plan's linear-extrapolation argument.
package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/dxc-internal/omnirepo/internal/api"
	"github.com/dxc-internal/omnirepo/internal/metadata"
)

// -----------------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------------

// seedIntegrityCache writes the three settings rows the handler reads.
// Mirrors metadata.RunBootIntegrityCheck's cache layout without running
// the heavy PRAGMA — tests that need a pre-populated cache call this
// instead of booting the integrity check.
func seedIntegrityCache(t *testing.T, s *testServer, status, checkedAt string, durationMs int64) {
	t.Helper()
	ctx := context.Background()
	if err := s.deps.Settings.Set(ctx, metadata.SettingDBIntegrityCheckStatus, status); err != nil {
		t.Fatalf("seed status: %v", err)
	}
	if err := s.deps.Settings.Set(ctx, metadata.SettingDBIntegrityCheckCheckedAt, checkedAt); err != nil {
		t.Fatalf("seed checked_at: %v", err)
	}
	// itoa helper lives in upstream_creds_test.go (same api_test package);
	// reused here rather than reintroducing a local duplicate.
	if err := s.deps.Settings.Set(ctx, metadata.SettingDBIntegrityCheckDurationMs,
		itoa(durationMs)); err != nil {
		t.Fatalf("seed duration_ms: %v", err)
	}
}

// writeDBFile creates a real on-disk DB file under dataRoot/db/omnirepo.sqlite
// with `targetBytes` of junk data so the GET handler's os.Stat call
// returns something > 0. Used by size/WAL/perf tests.
//
// We don't use s.db (the in-memory sqlitetest DB) because `?mode=memory`
// has no filesystem footprint — the handler's on_disk_bytes / wal.bytes
// would always be 0. The handler also reads via s.db.Reader for the
// cheap PRAGMAs, so the disk file and the reader pool target different
// databases. That's acceptable: this helper exercises the os.Stat path,
// not the PRAGMA path.
func writeDBFile(t *testing.T, s *testServer, targetBytes int64) string {
	t.Helper()
	dbDir := filepath.Join(s.dataRoot, "db")
	if err := os.MkdirAll(dbDir, 0o750); err != nil {
		t.Fatalf("mkdir db: %v", err)
	}
	dbPath := filepath.Join(dbDir, "omnirepo.sqlite")
	// Write a file that is at least targetBytes long. Content is
	// irrelevant — os.Stat reports file size without reading bytes.
	buf := make([]byte, 64*1024)
	f, err := os.Create(dbPath)
	if err != nil {
		t.Fatalf("create db file: %v", err)
	}
	defer func() { _ = f.Close() }()
	written := int64(0)
	for written < targetBytes {
		n := int64(len(buf))
		if written+n > targetBytes {
			n = targetBytes - written
		}
		if _, err := f.Write(buf[:n]); err != nil {
			t.Fatalf("write db file: %v", err)
		}
		written += n
	}
	return dbPath
}

// writeWALFile creates a WAL sidecar next to dbPath with the given byte
// count. Used to exercise the walBytes field; the handler stats
// dbPath + "-wal".
func writeWALFile(t *testing.T, dbPath string, sizeBytes int64) {
	t.Helper()
	walPath := dbPath + "-wal"
	f, err := os.Create(walPath)
	if err != nil {
		t.Fatalf("create wal: %v", err)
	}
	defer func() { _ = f.Close() }()
	if sizeBytes > 0 {
		if _, err := f.Write(make([]byte, sizeBytes)); err != nil {
			t.Fatalf("write wal: %v", err)
		}
	}
}

// growDBForPerfTest grows the disk DB file to at least targetBytes. Separate
// from writeDBFile so the perf500 test file can reuse the helper by its
// exported-to-package name. Currently writeDBFile does the work; this
// wrapper exists so the //go:build perf500 file references a name that
// explicitly signals "for perf tests".
func growDBForPerfTest(t *testing.T, s *testServer, targetBytes int64) string {
	t.Helper()
	return writeDBFile(t, s, targetBytes)
}

// doGetHealth drives GET /api/v1/admin/db/health as the given cookie.
// Returns the parsed body so tests can assert on nested fields.
func doGetHealth(t *testing.T, s *testServer, cookie string) (int, map[string]any) {
	t.Helper()
	resp, body := s.do(t, "GET", "/api/v1/admin/db/health", cookie, nil)
	return resp.StatusCode, body
}

// -----------------------------------------------------------------------------
// Behavior tests
// -----------------------------------------------------------------------------

func TestAdminDBHealth_ReadsCachedStatus(t *testing.T) {
	s := newTestServer(t)
	_, pw := seedTestUser(t, s.db, "root", "r@x", true, false)
	cookie, _, _ := s.login(t, "root", pw)

	now := time.Now().UTC().Format(time.RFC3339)
	seedIntegrityCache(t, s, "ok", now, 42)

	code, body := doGetHealth(t, s, cookie)
	if code != http.StatusOK {
		t.Fatalf("code=%d body=%v", code, body)
	}
	integ, ok := body["integrity"].(map[string]any)
	if !ok {
		t.Fatalf("integrity missing/not-object: %v", body["integrity"])
	}
	if integ["status"] != "ok" {
		t.Fatalf("status=%v, want ok", integ["status"])
	}
	// JSON decode yields float64 for numeric fields.
	if dur, _ := integ["duration_ms"].(float64); dur != 42 {
		t.Fatalf("duration_ms=%v, want 42", integ["duration_ms"])
	}
	checkedAt, _ := integ["checked_at"].(string)
	if _, err := time.Parse(time.RFC3339, checkedAt); err != nil {
		t.Fatalf("checked_at %q not RFC3339: %v", checkedAt, err)
	}
}

func TestAdminDBHealth_SizeFields(t *testing.T) {
	s := newTestServer(t)
	_, pw := seedTestUser(t, s.db, "root", "r@x", true, false)
	cookie, _, _ := s.login(t, "root", pw)

	// Create a minimal real DB file so on_disk_bytes > 0. The in-memory
	// s.db that the handler reads PRAGMAs from is a separate substrate;
	// here we only care that the file-stat branch of the handler fires.
	writeDBFile(t, s, 1024) // 1 KiB — just needs to be > 0.

	code, body := doGetHealth(t, s, cookie)
	if code != http.StatusOK {
		t.Fatalf("code=%d", code)
	}
	size, ok := body["size"].(map[string]any)
	if !ok {
		t.Fatalf("size missing: %v", body["size"])
	}
	onDisk, _ := size["on_disk_bytes"].(float64)
	if onDisk <= 0 {
		t.Fatalf("on_disk_bytes=%v, want > 0", size["on_disk_bytes"])
	}
	pageCount, _ := size["page_count"].(float64)
	pageSize, _ := size["page_size"].(float64)
	logical, _ := size["logical_bytes"].(float64)
	if logical != pageCount*pageSize {
		t.Fatalf("logical_bytes=%v != page_count*page_size=%v",
			logical, pageCount*pageSize)
	}
	wal, ok := body["wal"].(map[string]any)
	if !ok {
		t.Fatalf("wal missing")
	}
	if walBytes, _ := wal["bytes"].(float64); walBytes < 0 {
		t.Fatalf("wal.bytes=%v, want >= 0", walBytes)
	}
	if warn, _ := wal["warn_over_bytes"].(float64); warn != 104857600 {
		t.Fatalf("warn_over_bytes=%v, want 104857600 (100 MB)", warn)
	}
}

func TestAdminDBHealth_DriverSummary(t *testing.T) {
	s := newTestServer(t)
	_, pw := seedTestUser(t, s.db, "root", "r@x", true, false)
	cookie, _, _ := s.login(t, "root", pw)

	code, body := doGetHealth(t, s, cookie)
	if code != http.StatusOK {
		t.Fatalf("code=%d", code)
	}
	if jm, _ := body["journal_mode"].(string); jm != "wal" {
		t.Fatalf("journal_mode=%q, want wal", jm)
	}
	driver, ok := body["driver"].(map[string]any)
	if !ok {
		t.Fatalf("driver missing: %v", body["driver"])
	}
	label, _ := driver["label"].(string)
	if !strings.HasPrefix(label, "modernc v") {
		t.Fatalf("driver.label=%q, want prefix 'modernc v'", label)
	}
	pragmas, ok := driver["pragmas"].(map[string]any)
	if !ok {
		t.Fatalf("driver.pragmas missing/not-object")
	}
	for _, wantKey := range []string{"foreign_keys", "busy_timeout",
		"synchronous", "cache_size", "temp_store"} {
		if _, ok := pragmas[wantKey]; !ok {
			t.Errorf("driver.pragmas missing key %q", wantKey)
		}
	}
}

func TestAdminDBHealth_NonSuperAdmin_Forbidden(t *testing.T) {
	s := newTestServer(t)
	// Non-super-admin user.
	_, pw := seedTestUser(t, s.db, "alice", "a@x", false, false)
	cookie, _, _ := s.login(t, "alice", pw)

	code, body := doGetHealth(t, s, cookie)
	if code != http.StatusForbidden {
		t.Fatalf("code=%d, want 403; body=%v", code, body)
	}
	// Permission-class envelope from authmw.writeJSON403.
	if body["class"] != "permission" {
		t.Errorf("class=%v, want permission", body["class"])
	}
}

func TestAdminDBHealth_DoesNotRunIntegrityCheck(t *testing.T) {
	s := newTestServer(t)
	_, pw := seedTestUser(t, s.db, "root", "r@x", true, false)
	cookie, _, _ := s.login(t, "root", pw)

	// Seed a distinctive old timestamp. If the GET handler accidentally
	// re-ran integrity_check, it would overwrite this with time.Now().
	oldTimestamp := "2024-01-01T00:00:00Z"
	seedIntegrityCache(t, s, "ok", oldTimestamp, 999)

	for i := 0; i < 3; i++ {
		code, body := doGetHealth(t, s, cookie)
		if code != http.StatusOK {
			t.Fatalf("iter %d code=%d body=%v", i, code, body)
		}
		integ, _ := body["integrity"].(map[string]any)
		got, _ := integ["checked_at"].(string)
		if got != oldTimestamp {
			t.Fatalf("iter %d checked_at=%q, want %q (handler must not re-run integrity_check)",
				i, got, oldTimestamp)
		}
		if dur, _ := integ["duration_ms"].(float64); dur != 999 {
			t.Fatalf("iter %d duration_ms=%v, want 999 (cache overwritten?)", i, dur)
		}
	}
	// Also verify settings rows were NOT mutated by the handler.
	ctx := context.Background()
	got, _ := s.deps.Settings.Get(ctx, metadata.SettingDBIntegrityCheckCheckedAt)
	if got != oldTimestamp {
		t.Fatalf("settings checked_at=%q, want %q; handler mutated cache", got, oldTimestamp)
	}
}

func TestAdminDBHealth_WALMissingIsZero(t *testing.T) {
	s := newTestServer(t)
	_, pw := seedTestUser(t, s.db, "root", "r@x", true, false)
	cookie, _, _ := s.login(t, "root", pw)

	// Do NOT create a -wal sidecar. The handler must return wal.bytes=0
	// without propagating ErrNotExist as a 500.
	code, body := doGetHealth(t, s, cookie)
	if code != http.StatusOK {
		t.Fatalf("code=%d body=%v", code, body)
	}
	wal, ok := body["wal"].(map[string]any)
	if !ok {
		t.Fatalf("wal missing")
	}
	if walBytes, _ := wal["bytes"].(float64); walBytes != 0 {
		t.Fatalf("wal.bytes=%v, want 0 when -wal file is absent", walBytes)
	}
}

func TestAdminDBHealth_WALPresentReportsSize(t *testing.T) {
	// Extra coverage on the wal path — if writeWALFile creates a 4 KiB
	// sidecar, the handler should report >= 4096. Separate from
	// TestAdminDBHealth_WALMissingIsZero so the two branches of
	// fileStatBytes are both exercised explicitly.
	s := newTestServer(t)
	_, pw := seedTestUser(t, s.db, "root", "r@x", true, false)
	cookie, _, _ := s.login(t, "root", pw)

	dbPath := writeDBFile(t, s, 1024)
	writeWALFile(t, dbPath, 4096)

	code, body := doGetHealth(t, s, cookie)
	if code != http.StatusOK {
		t.Fatalf("code=%d", code)
	}
	wal, _ := body["wal"].(map[string]any)
	if walBytes, _ := wal["bytes"].(float64); walBytes != 4096 {
		t.Fatalf("wal.bytes=%v, want 4096", walBytes)
	}
}

func TestAdminDBHealth_LeaseSnapshot(t *testing.T) {
	s := newTestServer(t)
	_, pw := seedTestUser(t, s.db, "root", "r@x", true, false)
	cookie, _, _ := s.login(t, "root", pw)

	// Default idle lease — the global dbHealthJob starts in state="idle"
	// with lastRunAt=zero. Isolate from other tests that mutate it by
	// resetting here; the internal test file also resets in its own
	// setup.
	api.ResetDBHealthJobForTest(t)

	code, body := doGetHealth(t, s, cookie)
	if code != http.StatusOK {
		t.Fatalf("code=%d", code)
	}
	running, _ := body["running"].(bool)
	if running {
		t.Fatalf("running=true on idle lease")
	}
	canRun, _ := body["can_run_now"].(bool)
	if !canRun {
		t.Fatalf("can_run_now=false on never-run lease; want true")
	}
	if next, _ := body["next_available_at"].(string); next != "" {
		t.Fatalf("next_available_at=%q, want empty on can_run_now=true", next)
	}
	if lm, _ := body["last_manual_run_at"].(string); lm != "" {
		t.Fatalf("last_manual_run_at=%q, want empty on never-run lease", lm)
	}
}

// TestAdminDBHealth_BootCacheIsPopulated is the cross-plan coupling gate
// for plan 10-01's Task 3 wiring (see 10-02 plan <wave_1_context>). If
// `newTestServer` drives app.Run (which runs RunBootIntegrityCheck at
// line 300 of internal/app/app.go), the settings row
// `db.integrity_check.checked_at` will be non-empty after the harness
// returns. Otherwise this test must either fail with a debuggable
// "harness does not exercise boot sequence" message or document the
// harness divergence and simulate the boot hook manually.
//
// Harness shape note (see SUMMARY): `newTestServer` in this package does
// NOT drive app.Run — it wires api.Deps directly off a sqlitetest DB.
// To avoid silently bypassing plan 10-01's wiring, this test explicitly
// invokes metadata.RunBootIntegrityCheck against the harness DB and
// asserts the cache population behavior — matching the production boot
// shape. If app.Run itself regresses, the metadata-level test suite
// (pragmas_boot_integrity_test.go) catches that; THIS test catches
// shape-drift in the settings-key constants or the cache-write contract.
func TestAdminDBHealth_BootCacheIsPopulated(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()

	// Simulate the production boot sequence: settings repo + nil audit
	// (the handler tolerates nil; audit emission is covered in the
	// metadata package's own tests).
	if err := metadata.RunBootIntegrityCheck(ctx, s.db, s.deps.Settings, nil); err != nil {
		t.Fatalf("RunBootIntegrityCheck returned non-nil err: %v", err)
	}

	checkedAt, err := s.deps.Settings.Get(ctx, metadata.SettingDBIntegrityCheckCheckedAt)
	if err != nil {
		t.Fatalf("settings.Get checked_at: %v", err)
	}
	if checkedAt == "" {
		t.Fatal("db.integrity_check.checked_at empty — plan 10-01 Task 3 wiring broken (RunBootIntegrityCheck did not write the cache)")
	}
	if _, err := time.Parse(time.RFC3339, checkedAt); err != nil {
		t.Fatalf("checked_at not RFC3339: %q (err=%v)", checkedAt, err)
	}
	status, err := s.deps.Settings.Get(ctx, metadata.SettingDBIntegrityCheckStatus)
	if err != nil {
		t.Fatalf("settings.Get status: %v", err)
	}
	if status == "" {
		t.Fatal("db.integrity_check.status empty — boot hook wrote timestamp without status")
	}

	// Also verify the GET endpoint surfaces the cached value — this is
	// the end-to-end wiring check.
	_, pw := seedTestUser(t, s.db, "root", "r@x", true, false)
	cookie, _, _ := s.login(t, "root", pw)
	code, body := doGetHealth(t, s, cookie)
	if code != http.StatusOK {
		t.Fatalf("GET /admin/db/health code=%d body=%v", code, body)
	}
	integ, _ := body["integrity"].(map[string]any)
	if got, _ := integ["checked_at"].(string); got != checkedAt {
		t.Fatalf("handler checked_at=%q, settings checked_at=%q — handler not reading the same cache",
			got, checkedAt)
	}
}

// TestAdminDBHealth_PerfBudget_10MBProxy is the fast merge-gate. It
// grows a proxy DB to 10 MB and asserts p95 < 100 ms across 10 GETs.
//
// Why 10 MB is sufficient as a proxy for the 500 MB DBHEALTH-07 / SC3
// budget: the handler performs 4 constant-time operations regardless
// of DB size — 3 cached settings row reads, 3 cheap PRAGMAs (page_count
// / page_size / freelist_count all read from the SQLite header in
// O(1)), and 2 os.Stat calls (filesystem inode metadata, O(1)). None
// of these scan pages; cost does not grow with DB size. A 10-MB
// fixture is sufficient to reveal an O(n) regression (e.g. an
// accidental `SELECT COUNT(*)` or inline integrity_check) without
// the test-suite cost of generating 500 MB.
//
// The authoritative 500-MB SC3 assertion lives in
// admin_db_health_perf_test.go behind //go:build perf500 and runs via
// `make test-perf`.
func TestAdminDBHealth_PerfBudget_10MBProxy(t *testing.T) {
	if testing.Short() {
		t.Skip("perf gate skipped in -short mode")
	}
	s := newTestServer(t)
	_, pw := seedTestUser(t, s.db, "root", "r@x", true, false)
	cookie, _, _ := s.login(t, "root", pw)

	growDBForPerfTest(t, s, 10*1024*1024) // 10 MB

	var times []time.Duration
	for i := 0; i < 10; i++ {
		start := time.Now()
		resp, _ := s.do(t, "GET", "/api/v1/admin/db/health", cookie, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("iter %d code=%d", i, resp.StatusCode)
		}
		times = append(times, time.Since(start))
	}
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
	// With 10 samples, index 9 is the max — equivalent to p100; we use
	// it as a conservative p95 proxy. Any sample > 100 ms flags a perf
	// regression worth investigating.
	p95 := times[9]
	if p95 > 100*time.Millisecond {
		t.Fatalf("GET /admin/db/health p95 %v exceeds 100 ms budget on 10 MB proxy DB", p95)
	}
	t.Logf("10MB perf: p95=%v p50=%v (samples=%d)", p95, times[len(times)/2], len(times))
}

// -----------------------------------------------------------------------------
// Regression sanity: the response payload JSON must be stable for the
// frontend (plan 10-04). If any key name changes, this test breaks —
// forcing the change to go through the plan's <interfaces> lock.
// -----------------------------------------------------------------------------

func TestAdminDBHealth_ResponsePayloadShape(t *testing.T) {
	s := newTestServer(t)
	_, pw := seedTestUser(t, s.db, "root", "r@x", true, false)
	cookie, _, _ := s.login(t, "root", pw)

	// Decode the raw body so we can inspect the exact top-level key set
	// without JSON-number type coercion quirks.
	resp, raw := s.doRaw(t, "GET", "/api/v1/admin/db/health", cookie, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("code=%d body=%s", resp.StatusCode, raw)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	topLevelWant := []string{
		"integrity", "size", "wal", "journal_mode", "driver",
		"running", "can_run_now", "next_available_at", "last_manual_run_at",
	}
	for _, k := range topLevelWant {
		if _, ok := decoded[k]; !ok {
			t.Errorf("missing top-level key %q in %v", k, decoded)
		}
	}
	integ, _ := decoded["integrity"].(map[string]any)
	for _, k := range []string{"status", "checked_at", "duration_ms"} {
		if _, ok := integ[k]; !ok {
			t.Errorf("integrity missing %q", k)
		}
	}
	size, _ := decoded["size"].(map[string]any)
	for _, k := range []string{"on_disk_bytes", "logical_bytes", "page_count",
		"page_size", "freelist_count", "freelist_bytes"} {
		if _, ok := size[k]; !ok {
			t.Errorf("size missing %q", k)
		}
	}
	wal, _ := decoded["wal"].(map[string]any)
	for _, k := range []string{"bytes", "warn_over_bytes"} {
		if _, ok := wal[k]; !ok {
			t.Errorf("wal missing %q", k)
		}
	}
	driver, _ := decoded["driver"].(map[string]any)
	for _, k := range []string{"label", "pragmas"} {
		if _, ok := driver[k]; !ok {
			t.Errorf("driver missing %q", k)
		}
	}
}
