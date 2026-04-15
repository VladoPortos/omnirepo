package httpx_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/dxc-internal/omnirepo/internal/httpx"
)

type recordingHandler struct {
	enterCount atomic.Int64
	exitCount  atomic.Int64
}

func (h *recordingHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }
func (h *recordingHandler) Handle(_ context.Context, r slog.Record) error {
	switch r.Message {
	case "audit.enter":
		h.enterCount.Add(1)
	case "audit.exit":
		h.exitCount.Add(1)
	}
	return nil
}
func (h *recordingHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *recordingHandler) WithGroup(_ string) slog.Handler      { return h }

func TestAuditEnterExitEmitsSlogLines(t *testing.T) {
	rh := &recordingHandler{}
	orig := slog.Default()
	slog.SetDefault(slog.New(rh))
	t.Cleanup(func() { slog.SetDefault(orig) })

	final := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mw := httpx.AuditEnter(httpx.AuditExit(final))

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d", rec.Code)
	}
	if rh.enterCount.Load() != 1 {
		t.Fatalf("audit.enter count = %d, want 1", rh.enterCount.Load())
	}
	if rh.exitCount.Load() != 1 {
		t.Fatalf("audit.exit count = %d, want 1", rh.exitCount.Load())
	}
}
