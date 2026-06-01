package api_test

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/vladoportos/omnirepo/internal/api"
	"github.com/vladoportos/omnirepo/internal/audit"
	"github.com/vladoportos/omnirepo/internal/auth"
)

// stubAuditLogger captures every Event that flows through Record. err is
// returned verbatim so the Deps.recordAudit error path is exercisable.
type stubAuditLogger struct {
	events []audit.Event
	err    error
}

func (s *stubAuditLogger) Record(_ context.Context, e audit.Event) error {
	s.events = append(s.events, e)
	return s.err
}

func TestDepsRecordAuditAs_ProjectOwnedAPIKey_NoFKViolation(t *testing.T) {
	stub := &stubAuditLogger{}
	deps := api.Deps{Audit: stub}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/p/s3-buckets", nil)

	actor := auth.Actor{
		Kind:      auth.ActorKindAPIKey,
		OwnerKind: auth.OwnerKindProject,
		APIKeyID:  9,
		// ID is deliberately 0 — the audit-finding-#7 case.
	}
	deps.RecordAuditAsForTest(req, audit.Event{
		Kind:       audit.EvtS3BucketCreated,
		TargetKind: "s3_bucket",
		TargetID:   "alpha",
	}, actor)

	if len(stub.events) != 1 {
		t.Fatalf("recorded events = %d, want 1", len(stub.events))
	}
	e := stub.events[0]
	if e.ActorUserID != nil {
		t.Fatalf("ActorUserID = %v, want nil (FK-violation guard)", *e.ActorUserID)
	}
	if e.ActorAPIKeyID == nil || *e.ActorAPIKeyID != 9 {
		t.Fatalf("ActorAPIKeyID = %v, want &9", e.ActorAPIKeyID)
	}
	if e.Kind != audit.EvtS3BucketCreated || e.TargetID != "alpha" {
		t.Fatalf("event passthrough broken: %+v", e)
	}
}

// warnCapture mirrors internal/audit/audit_test.go:111 warnCountHandler but
// also keeps the message string for assertion.
type warnCapture struct {
	msgs    []string
	warnCnt atomic.Int64
}

func (h *warnCapture) Enabled(_ context.Context, _ slog.Level) bool { return true }
func (h *warnCapture) Handle(_ context.Context, r slog.Record) error {
	if r.Level == slog.LevelWarn {
		h.warnCnt.Add(1)
		h.msgs = append(h.msgs, r.Message)
	}
	return nil
}
func (h *warnCapture) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *warnCapture) WithGroup(_ string) slog.Handler      { return h }

func TestDepsRecordAudit_LogsWarnOnAuditError(t *testing.T) {
	stub := &stubAuditLogger{err: errors.New("simulated db failure")}
	deps := api.Deps{Audit: stub}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/anything", nil)

	orig := slog.Default()
	h := &warnCapture{}
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(orig) })

	deps.RecordAuditForTest(req, audit.Event{Kind: audit.EvtS3BucketCreated})

	if got := h.warnCnt.Load(); got != 1 {
		t.Fatalf("WARN count = %d, want 1", got)
	}
	if len(h.msgs) != 1 || h.msgs[0] != "audit.record.failed" {
		t.Fatalf("WARN messages = %v, want [\"audit.record.failed\"]", h.msgs)
	}
}

func TestDepsRecordAudit_NoWarnOnSuccess(t *testing.T) {
	stub := &stubAuditLogger{} // err == nil
	deps := api.Deps{Audit: stub}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/anything", nil)

	orig := slog.Default()
	h := &warnCapture{}
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(orig) })

	deps.RecordAuditForTest(req, audit.Event{Kind: audit.EvtS3BucketCreated})
	if got := h.warnCnt.Load(); got != 0 {
		t.Fatalf("WARN count = %d, want 0 on success path", got)
	}
}
