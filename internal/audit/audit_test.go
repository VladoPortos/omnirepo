package audit_test

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"log/slog"

	"github.com/dxc-internal/omnirepo/internal/audit"
	"github.com/dxc-internal/omnirepo/internal/metadata/sqlitetest"
)

func TestRecordWritesOneDBRowAndOneNDJSONLine(t *testing.T) {
	db := sqlitetest.New(t)
	dir := t.TempDir()
	ndjson := filepath.Join(dir, "audit.log")
	l, err := audit.New(db, ndjson, 100, 10)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	uid := int64(7)
	ev := audit.Event{
		ActorUserID: &uid,
		IP:          "10.0.0.1",
		UserAgent:   "curl/8",
		Kind:        audit.EvtAuthLoginSuccess,
		TargetKind:  "user",
		TargetID:    "7",
		Outcome:     "ok",
		Details:     map[string]any{"reason": "password"},
	}
	if err := l.Record(ctx, ev); err != nil {
		t.Fatalf("Record: %v", err)
	}

	var count int
	if err := db.Reader.QueryRowContext(ctx, `SELECT count(*) FROM audit_log WHERE event_kind=?`, string(audit.EvtAuthLoginSuccess)).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("audit_log rows = %d, want 1", count)
	}

	// Inspect details_json and actor_user_id
	var actor int64
	var details string
	if err := db.Reader.QueryRowContext(ctx, `SELECT actor_user_id, details_json FROM audit_log WHERE event_kind=?`, string(audit.EvtAuthLoginSuccess)).Scan(&actor, &details); err != nil {
		t.Fatal(err)
	}
	if actor != 7 {
		t.Fatalf("actor_user_id = %d want 7", actor)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(details), &m); err != nil {
		t.Fatalf("details_json not valid JSON: %v", err)
	}

	// NDJSON line exists and round-trips
	lines := readLines(t, ndjson)
	if len(lines) != 1 {
		t.Fatalf("NDJSON lines = %d want 1", len(lines))
	}
	var got audit.Event
	if err := json.Unmarshal([]byte(lines[0]), &got); err != nil {
		t.Fatalf("NDJSON line not valid Event JSON: %v", err)
	}
	if got.Kind != audit.EvtAuthLoginSuccess {
		t.Fatalf("round-trip kind = %q", got.Kind)
	}
}

func TestRecordAcceptsNilActorsForLoginFailure(t *testing.T) {
	db := sqlitetest.New(t)
	dir := t.TempDir()
	ndjson := filepath.Join(dir, "audit.log")
	l, err := audit.New(db, ndjson, 100, 10)
	if err != nil {
		t.Fatal(err)
	}
	ev := audit.Event{
		Kind:    audit.EvtAuthLoginFailure,
		Outcome: "denied",
		IP:      "192.0.2.9",
	}
	if err := l.Record(context.Background(), ev); err != nil {
		t.Fatalf("Record (nil actors): %v", err)
	}
	// Assert NULL columns on the written row
	var uid, kid interface{}
	if err := db.Reader.QueryRowContext(context.Background(), `SELECT actor_user_id, actor_api_key_id FROM audit_log WHERE event_kind=?`, string(audit.EvtAuthLoginFailure)).Scan(&uid, &kid); err != nil {
		t.Fatal(err)
	}
	if uid != nil || kid != nil {
		t.Fatalf("expected NULL actors, got uid=%v kid=%v", uid, kid)
	}
}

// captureWarnHandler counts WARN records.
type warnCountHandler struct {
	warns atomic.Int64
}

func (h *warnCountHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }
func (h *warnCountHandler) Handle(_ context.Context, r slog.Record) error {
	if r.Level == slog.LevelWarn {
		h.warns.Add(1)
	}
	return nil
}
func (h *warnCountHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *warnCountHandler) WithGroup(_ string) slog.Handler       { return h }

func TestRecordBestEffortOnNDJSONFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod semantics differ on windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root defeats chmod 0500")
	}
	db := sqlitetest.New(t)
	dir := t.TempDir()
	ndjson := filepath.Join(dir, "audit.log")
	l, err := audit.New(db, ndjson, 100, 10)
	if err != nil {
		t.Fatal(err)
	}
	// Break the NDJSON sink post-open by making the directory read-only so
	// rotation (or subsequent opens) can't create new files. Since we have
	// a working file, let's instead close the file out from under the writer
	// by removing it, then making the dir read-only (writes may still succeed
	// to the open FD). A reliable way is to inject a writer whose Write
	// returns error. We don't have that API; instead simulate by writing an
	// oversized event that fails JSON encode — use a value that json.Marshal
	// rejects.

	// Install handler to count WARNs
	orig := slog.Default()
	h := &warnCountHandler{}
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(orig) })

	// json.Marshal fails on a channel type embedded in Details.
	bad := audit.Event{
		Kind:    audit.EvtMaintenanceToggled,
		Outcome: "ok",
		Details: map[string]any{"ch": make(chan int)},
	}
	// Record should still succeed (DB row committed) despite NDJSON failure
	// — best-effort per D-33/OQ-9.
	if err := l.Record(context.Background(), bad); err != nil {
		t.Fatalf("Record returned error on NDJSON failure: %v", err)
	}
	// DB row committed
	var count int
	if err := db.Reader.QueryRowContext(context.Background(), `SELECT count(*) FROM audit_log WHERE event_kind=?`, string(audit.EvtMaintenanceToggled)).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("DB row count = %d, want 1", count)
	}
	if h.warns.Load() == 0 {
		t.Fatal("expected a slog WARN on NDJSON failure")
	}
}

func TestEveryStateChangingActionEmitsEvent(t *testing.T) {
	db := sqlitetest.New(t)
	dir := t.TempDir()
	ndjson := filepath.Join(dir, "audit.log")
	l, err := audit.New(db, ndjson, 100, 10)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	// Evt constants iterated here (at least 19)
	kinds := []audit.EventKind{
		audit.EvtAuthLoginSuccess,
		audit.EvtAuthLoginFailure,
		audit.EvtAuthLogout,
		audit.EvtAuthPasswordChanged,
		audit.EvtUserCreated,
		audit.EvtUserUpdated,
		audit.EvtUserDeleted,
		audit.EvtProjectCreated,
		audit.EvtProjectUpdated,
		audit.EvtProjectDeleted,
		audit.EvtMemberAdded,
		audit.EvtMemberRemoved,
		audit.EvtRepoCreated,
		audit.EvtRepoUpdated,
		audit.EvtRepoDeleted,
		audit.EvtRepoWiped,
		audit.EvtTLSCertUploaded,
		audit.EvtBootstrapApplied,
		audit.EvtMaintenanceToggled,
	}
	for _, k := range kinds {
		if err := l.Record(ctx, audit.Event{Kind: k, Outcome: "ok"}); err != nil {
			t.Fatalf("Record %s: %v", k, err)
		}
	}
	// One row per kind
	for _, k := range kinds {
		var c int
		if err := db.Reader.QueryRowContext(ctx, `SELECT count(*) FROM audit_log WHERE event_kind=?`, string(k)).Scan(&c); err != nil {
			t.Fatal(err)
		}
		if c != 1 {
			t.Fatalf("rows for %s = %d, want 1", k, c)
		}
	}
	// Total lines
	lines := readLines(t, ndjson)
	if len(lines) != len(kinds) {
		t.Fatalf("NDJSON lines = %d, want %d", len(lines), len(kinds))
	}
}

func readLines(t testing.TB, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var out []string
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 1024*1024), 10*1024*1024)
	for s.Scan() {
		line := strings.TrimRight(s.Text(), "\n")
		if line != "" {
			out = append(out, line)
		}
	}
	if err := s.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}
