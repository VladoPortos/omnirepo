package httperr

import (
	"encoding/json"
	"log/slog"
	"net/http"

	chimw "github.com/go-chi/chi/v5/middleware"
)

// Write serializes e.Envelope as the HTTP response body, stamps the
// envelope's incident_id from the chi request ID, and logs the internal
// cause keyed by the same ID. Safe to call from any handler.
//
// Behavior:
//
//   - Content-Type is always application/json; charset=utf-8.
//   - HTTP status is e.Status; if zero, derived from Envelope.Class via
//     defaultStatusForClass (validation=400, permission=403,
//     transient=503, operator_action_required=503, anything else=500).
//   - Envelope.IncidentID is set to middleware.GetReqID(ctx) if non-empty;
//     unit tests without chi middleware get an empty incident_id which
//     serializes as an omitted field (omitempty).
//   - An slog record with keys incident_id, status, code, class, cause is
//     emitted on r.Context(). Level is ERROR for 5xx or
//     operator_action_required, WARN for 4xx client errors, so client
//     bugs do not flood ERROR-level alerting. The cause is logged only
//     here — it is never serialized into the response body.
//   - e.Cause and e.Status are never marshalled; only e.Envelope is.
//   - Calling Write(w, r, nil) emits a generic 500 envelope rather than
//     panicking, for defensive routing in recover middleware.
func Write(w http.ResponseWriter, r *http.Request, e *Error) {
	if e == nil {
		// Defensive — should never happen in production call sites.
		// Emit a generic 500 so panic-recover middleware has a safe
		// fallback.
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(Envelope{
			Code:    "api.unexpected",
			Message: "An internal error occurred.",
			Class:   ClassTransient,
		})
		return
	}

	reqID := chimw.GetReqID(r.Context())
	if reqID != "" {
		e.Envelope.IncidentID = reqID
	}

	status := e.Status
	if status == 0 {
		status = defaultStatusForClass(e.Envelope.Class)
	}

	// Log the internal cause (never serialized to client). Routed by
	// response status class so 4xx client errors don't pollute ERROR-level
	// alerting: 5xx (server/operator bug) stays at ERROR; 4xx (client bug)
	// drops to WARN but remains fully structured and searchable by
	// incident_id / code / class. The OperatorRequired class is always
	// ERROR since an operator must act regardless of HTTP status.
	logLevel := slog.LevelError
	if status < 500 && e.Envelope.Class != ClassOperatorRequired {
		logLevel = slog.LevelWarn
	}
	slog.Log(r.Context(), logLevel, "api.error",
		slog.String("incident_id", reqID),
		slog.Int("status", status),
		slog.String("code", e.Envelope.Code),
		slog.String("class", string(e.Envelope.Class)),
		slog.Any("cause", e.Cause),
	)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(e.Envelope)
}

// defaultStatusForClass maps a Class to its canonical HTTP status code.
// Used by Write when caller did not set Error.Status explicitly.
func defaultStatusForClass(c Class) int {
	switch c {
	case ClassValidation:
		return http.StatusBadRequest
	case ClassPermission:
		return http.StatusForbidden
	case ClassTransient:
		return http.StatusServiceUnavailable
	case ClassOperatorRequired:
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}
