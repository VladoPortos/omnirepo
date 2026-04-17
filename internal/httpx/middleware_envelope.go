package httpx

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"

	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"

	"github.com/dxc-internal/omnirepo/internal/httperr"
)

// IncidentIDMiddleware stamps a UUID v7 on every incoming request.
//
// It replaces chi's default middleware.RequestID (which emits a
// hostname-pid-counter string and leaks the hostname). The UUID v7 is
// time-sortable, privacy-safe, and serves as the public incident_id
// surfaced through httperr.Envelope and the X-Incident-Id response
// header. The same identifier is stashed under chimw.RequestIDKey so
// existing chi-aware logging middleware (StructuredLogger, AuditEnter)
// continues to work unchanged.
//
// Install this FIRST in the middleware chain so downstream middleware
// and handlers see a populated request ID.
func IncidentIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.NewV7()
		var idStr string
		if err != nil {
			// Extremely unlikely — uuid.NewV7 only fails on clock
			// regression. Fall back to v4 so the request still gets
			// a unique correlation ID.
			idStr = uuid.NewString()
		} else {
			idStr = id.String()
		}

		ctx := context.WithValue(r.Context(), chimw.RequestIDKey, idStr)

		// Surface the ID on the response for clients and log aggregators.
		// X-Request-Id is preserved for legacy clients that already grep
		// for it; both headers carry the same UUID v7.
		w.Header().Set("X-Incident-Id", idStr)
		w.Header().Set("X-Request-Id", idStr)

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// EnvelopeRecoverer catches panics inside downstream handlers and emits
// a canonical httperr.Internal envelope. Replaces chi's
// middleware.Recoverer so the response is a structured ApiErrorEnvelope
// JSON body instead of a raw stack trace (ERR-03).
//
// MUST be installed after IncidentIDMiddleware so the recovered-panic
// slog record and the emitted envelope share the request's incident ID.
// The full stack is logged at ERROR level via slog; it is never
// serialized to the client. The panic value is captured in the slog
// `cause` field only and does not appear in the response body.
func EnvelopeRecoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}
			stack := debug.Stack()
			slog.ErrorContext(r.Context(), "api.panic",
				slog.String("incident_id", chimw.GetReqID(r.Context())),
				slog.Any("cause", rec),
				slog.String("stack", string(stack)),
			)
			// Use httperr.Internal so the client sees a generic
			// "An internal error occurred." message and the cause
			// is logged (not serialized) via httperr.Write.
			e := httperr.Internal("api.panic", fmt.Errorf("panic: %v", rec))
			httperr.Write(w, r, e)
		}()
		next.ServeHTTP(w, r)
	})
}
