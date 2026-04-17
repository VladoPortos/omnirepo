package httpx_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/dxc-internal/omnirepo/internal/httperr"
	"github.com/dxc-internal/omnirepo/internal/httpx"
)

// uuidV7Pattern is the canonical UUID v7 regex: the 13th nibble is "7"
// (version) and the 17th nibble is 8/9/a/b (RFC 4122 variant).
var uuidV7Pattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// mustDoRequest runs req through next (wrapped with optional middleware)
// and returns the recorded response.
func runThrough(t *testing.T, mw func(http.Handler) http.Handler, handler http.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	var h http.Handler = handler
	if mw != nil {
		h = mw(h)
	}
	h.ServeHTTP(rec, req)
	return rec
}

func TestIncidentIDMiddleware_SetsUUIDv7Header(t *testing.T) {
	rec := runThrough(t, httpx.IncidentIDMiddleware, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	id := rec.Header().Get("X-Incident-Id")
	if id == "" {
		t.Fatalf("expected X-Incident-Id header to be set, got empty")
	}
	if !uuidV7Pattern.MatchString(id) {
		t.Fatalf("X-Incident-Id %q does not match UUID v7 pattern", id)
	}
}

func TestIncidentIDMiddleware_ContextMatchesHeader(t *testing.T) {
	var captured string
	rec := runThrough(t, httpx.IncidentIDMiddleware, func(w http.ResponseWriter, r *http.Request) {
		captured = chimw.GetReqID(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	header := rec.Header().Get("X-Incident-Id")
	if header == "" {
		t.Fatalf("header missing")
	}
	if captured == "" {
		t.Fatalf("context request ID missing")
	}
	if captured != header {
		t.Fatalf("context id %q != header id %q", captured, header)
	}
}

func TestIncidentIDMiddleware_AlsoSetsXRequestId(t *testing.T) {
	rec := runThrough(t, httpx.IncidentIDMiddleware, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	incident := rec.Header().Get("X-Incident-Id")
	legacy := rec.Header().Get("X-Request-Id")
	if incident == "" || legacy == "" {
		t.Fatalf("expected both X-Incident-Id and X-Request-Id to be set, got incident=%q legacy=%q", incident, legacy)
	}
	if incident != legacy {
		t.Fatalf("X-Incident-Id %q != X-Request-Id %q", incident, legacy)
	}
}

func TestIncidentIDMiddleware_UniquePerRequest(t *testing.T) {
	mw := httpx.IncidentIDMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req1 := httptest.NewRequest(http.MethodGet, "/", nil)
	rec1 := httptest.NewRecorder()
	mw.ServeHTTP(rec1, req1)

	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	rec2 := httptest.NewRecorder()
	mw.ServeHTTP(rec2, req2)

	if rec1.Header().Get("X-Incident-Id") == rec2.Header().Get("X-Incident-Id") {
		t.Fatalf("two consecutive requests produced the same incident id")
	}
}

func TestEnvelopeRecoverer_CatchesPanic(t *testing.T) {
	rec := runThrough(t, httpx.EnvelopeRecoverer, func(w http.ResponseWriter, r *http.Request) {
		panic("boom")
	})

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}

	body := rec.Body.Bytes()
	var env httperr.Envelope
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("response body is not a valid envelope: %v; body=%s", err, string(body))
	}
	if env.Code != "api.panic" {
		t.Fatalf("expected code api.panic, got %q", env.Code)
	}
	if env.Class != httperr.ClassTransient {
		t.Fatalf("expected class transient, got %q", env.Class)
	}
	if env.Message == "" {
		t.Fatalf("expected non-empty message")
	}
	// Panic value ("boom") must never appear in the response body.
	if strings.Contains(string(body), "boom") {
		t.Fatalf("panic value leaked into response body: %s", string(body))
	}
	// Stack markers must never appear in the response body.
	if strings.Contains(string(body), "goroutine") || strings.Contains(string(body), ".go:") {
		t.Fatalf("stack trace leaked into response body: %s", string(body))
	}
}

func TestEnvelopeRecoverer_IncidentIDPropagates(t *testing.T) {
	chain := httpx.IncidentIDMiddleware(httpx.EnvelopeRecoverer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("kapow")
	})))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	chain.ServeHTTP(rec, req)

	headerID := rec.Header().Get("X-Incident-Id")
	if headerID == "" {
		t.Fatalf("expected X-Incident-Id header after panic recovery")
	}

	var env httperr.Envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("body is not envelope JSON: %v", err)
	}
	if env.IncidentID != headerID {
		t.Fatalf("envelope incident_id %q != header %q", env.IncidentID, headerID)
	}
}

func TestEnvelopeRecoverer_NoPanic_NoInterference(t *testing.T) {
	rec := runThrough(t, httpx.EnvelopeRecoverer, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusTeapot)
		_, _ = io.WriteString(w, "ok")
	})

	if rec.Code != http.StatusTeapot {
		t.Fatalf("expected 418, got %d", rec.Code)
	}
	if got := rec.Body.String(); got != "ok" {
		t.Fatalf("expected body 'ok', got %q", got)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/plain" {
		t.Fatalf("expected pass-through content-type text/plain, got %q", ct)
	}
}

func TestEnvelopeRecoverer_NilPanic_Ignored(t *testing.T) {
	// panic(nil) in Go 1.21+ still triggers recover() with a runtime.PanicNilError.
	// The recoverer must still emit a valid envelope (no double-panic).
	rec := runThrough(t, httpx.EnvelopeRecoverer, func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			// Intentionally empty — let the panic propagate.
		}()
		// Trigger a genuine panic path that handlers might accidentally hit.
		var p *int
		_ = *p // nil dereference → runtime.PanicNilError-like recovery
	})

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 after nil-deref panic, got %d", rec.Code)
	}
	var env httperr.Envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("body not envelope after nil-deref panic: %v", err)
	}
	if env.Code != "api.panic" {
		t.Fatalf("expected code api.panic, got %q", env.Code)
	}
}

// _ prevents an "unused import" if context is ever dropped below.
var _ = context.Background
