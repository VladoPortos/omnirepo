package httpx

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"
)

// AuditEnter emits an slog "audit.enter" record at the start of the request
// and an "audit.exit" record with the duration at the end. These are the
// entry/exit hooks in the middleware chain; a REST-level audit table writer
// layers on top.
func AuditEnter(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		reqID := middleware.GetReqID(r.Context())
		slog.InfoContext(r.Context(), "audit.enter",
			slog.String("request_id", reqID),
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
		)
		next.ServeHTTP(w, r)
		slog.InfoContext(r.Context(), "audit.exit",
			slog.String("request_id", reqID),
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int64("duration_ms", time.Since(start).Milliseconds()),
		)
	})
}

// AuditExit is a pass-through kept for API symmetry with AuditEnter so the
// router constructor's middleware chain reads cleanly (Use(AuditEnter) +
// Use(AuditExit)). It may later flip into a real record-writer when the
// REST surface lands.
func AuditExit(next http.Handler) http.Handler { return next }
