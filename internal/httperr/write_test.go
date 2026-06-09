package httperr_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/vladoportos/omnirepo/internal/httperr"
)

// decodeBody decodes the recorder body into a generic map so tests can assert
// on field presence/absence without the stricter typing of httperr.Envelope.
func decodeBody(t *testing.T, body io.Reader) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.NewDecoder(body).Decode(&m); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	return m
}

func TestWrite_SerializesEnvelopeOnly(t *testing.T) {
	e := httperr.Validation(
		"user.name_required",
		"Name is required",
		httperr.WithHint("Enter a name."),
		httperr.WithDetail("field", "user.name"),
	)
	e.Cause = errors.New("internal detail that must not be exposed")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/t", nil)
	httperr.Write(rec, req, e)

	m := decodeBody(t, rec.Body)

	// Required envelope keys present.
	if m["code"] != "user.name_required" {
		t.Errorf("code = %v", m["code"])
	}
	if m["message"] != "Name is required" {
		t.Errorf("message = %v", m["message"])
	}
	if m["class"] != "validation" {
		t.Errorf("class = %v", m["class"])
	}
	if m["hint"] != "Enter a name." {
		t.Errorf("hint = %v", m["hint"])
	}
	if d, ok := m["details"].(map[string]any); !ok || d["field"] != "user.name" {
		t.Errorf("details missing field: %v", m["details"])
	}

	// Non-envelope keys absent.
	for _, forbidden := range []string{"cause", "Cause", "status", "Status", "Envelope"} {
		if _, ok := m[forbidden]; ok {
			t.Errorf("forbidden key %q present in body: %v", forbidden, m)
		}
	}
}

func TestWrite_IncidentIDFromChiMiddleware(t *testing.T) {
	// Use a channel to capture the request-id chi assigned to the request
	// context so we can assert the envelope carries the same value. chi's
	// RequestID middleware seeds ctx via middleware.GetReqID but does not
	// echo it in a response header, so we compare via shared channel.
	gotReqID := make(chan string, 1)

	r := chi.NewRouter()
	r.Use(chimw.RequestID)
	r.Get("/t", func(w http.ResponseWriter, req *http.Request) {
		gotReqID <- chimw.GetReqID(req.Context())
		e := httperr.Permission("auth.forbidden", "You do not have permission to view this.")
		httperr.Write(w, req, e)
	})

	srv := httptest.NewServer(r)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/t")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	assigned := <-gotReqID
	if assigned == "" {
		t.Fatal("chi RequestID middleware produced empty request id")
	}

	m := decodeBody(t, resp.Body)
	got, ok := m["incident_id"].(string)
	if !ok {
		t.Fatalf("incident_id not string, got %T: %v", m["incident_id"], m["incident_id"])
	}
	if got != assigned {
		t.Errorf("incident_id = %q, chi request id = %q", got, assigned)
	}
}

func TestWrite_DefaultStatusForClass(t *testing.T) {
	cases := []struct {
		name string
		err  *httperr.Error
		want int
	}{
		{
			name: "validation",
			err:  &httperr.Error{Envelope: httperr.Envelope{Code: "a.b", Message: "x", Class: httperr.ClassValidation}},
			want: http.StatusBadRequest,
		},
		{
			name: "permission",
			err:  &httperr.Error{Envelope: httperr.Envelope{Code: "a.b", Message: "x", Class: httperr.ClassPermission}},
			want: http.StatusForbidden,
		},
		{
			name: "transient",
			err:  &httperr.Error{Envelope: httperr.Envelope{Code: "a.b", Message: "x", Class: httperr.ClassTransient}},
			want: http.StatusServiceUnavailable,
		},
		{
			name: "operator_required",
			err:  &httperr.Error{Envelope: httperr.Envelope{Code: "a.b", Message: "x", Class: httperr.ClassOperatorRequired}},
			want: http.StatusServiceUnavailable,
		},
		{
			name: "unknown_class",
			err:  &httperr.Error{Envelope: httperr.Envelope{Code: "a.b", Message: "x", Class: "???"}},
			want: http.StatusInternalServerError,
		},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest("GET", "/t", nil)
			httperr.Write(rec, req, c.err)
			if rec.Code != c.want {
				t.Errorf("status = %d, want %d", rec.Code, c.want)
			}
		})
	}
}

func TestWrite_ExplicitStatusWins(t *testing.T) {
	e := httperr.Validation("user.name_required", "Name is required", httperr.WithStatus(418))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/t", nil)
	httperr.Write(rec, req, e)
	if rec.Code != 418 {
		t.Errorf("status = %d, want 418", rec.Code)
	}
}

func TestWrite_NilErrorEmitsGeneric500(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/t", nil)
	httperr.Write(rec, req, nil)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	m := decodeBody(t, rec.Body)
	if m["code"] != "api.unexpected" {
		t.Errorf("code = %v, want api.unexpected", m["code"])
	}
	if m["message"] != "An internal error occurred." {
		t.Errorf("message = %v", m["message"])
	}
	if m["class"] != "transient" {
		t.Errorf("class = %v, want transient", m["class"])
	}
}

func TestWrite_ContentTypeJSON(t *testing.T) {
	e := httperr.Validation("user.name_required", "Name is required")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/t", nil)
	httperr.Write(rec, req, e)
	ct := rec.Header().Get("Content-Type")
	if ct != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q, want application/json; charset=utf-8", ct)
	}
}

func TestWrite_NoRequestIDMiddleware_IncidentIDEmpty(t *testing.T) {
	// Request without chi.RequestID middleware — incident_id should be
	// omitted from the JSON envelope (unit-test ergonomics).
	e := httperr.Validation("user.name_required", "Name is required")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/t", nil)
	httperr.Write(rec, req, e)
	m := decodeBody(t, rec.Body)
	if _, ok := m["incident_id"]; ok {
		t.Errorf("incident_id should be omitted when no request ID on context: %v", m)
	}
}

// TestWrite_LogLevelByStatus pins the level-routing rule so client-error
// 4xx responses don't flood ERROR-level alerting. 5xx or
// operator_action_required always ERROR; 4xx validation / permission /
// transient always WARN.
func TestWrite_LogLevelByStatus(t *testing.T) {
	cases := []struct {
		name      string
		err       *httperr.Error
		wantLevel slog.Level
	}{
		{
			name:      "400 validation → WARN",
			err:       httperr.Validation("user.name_required", "Name is required"),
			wantLevel: slog.LevelWarn,
		},
		{
			name:      "403 permission → WARN",
			err:       httperr.Permission("auth.forbidden", "You do not have permission."),
			wantLevel: slog.LevelWarn,
		},
		{
			name:      "422 explicit → WARN",
			err:       httperr.Validation("validation.failed", "bad input", httperr.WithStatus(422)),
			wantLevel: slog.LevelWarn,
		},
		{
			name:      "503 transient → ERROR (5xx)",
			err:       httperr.Transient("svc.unavailable", "retry", 0),
			wantLevel: slog.LevelError,
		},
		{
			name:      "500 internal → ERROR",
			err:       httperr.Internal("api.internal", errors.New("boom")),
			wantLevel: slog.LevelError,
		},
		{
			name:      "operator_required → ERROR regardless of status",
			err:       httperr.OperatorRequired("trivy.db_missing", "run trivy init", "/admin/trivy", "Open Trivy admin"),
			wantLevel: slog.LevelError,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var buf bytes.Buffer
			// Capture at DEBUG so both WARN and ERROR records land in buf.
			h := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
			prev := slog.Default()
			slog.SetDefault(slog.New(h))
			t.Cleanup(func() { slog.SetDefault(prev) })

			rec := httptest.NewRecorder()
			req := httptest.NewRequest("GET", "/t", nil)
			httperr.Write(rec, req, c.err)

			out := buf.String()
			if !strings.Contains(out, "msg=api.error") {
				t.Fatalf("no api.error log line emitted: %q", out)
			}
			var wantKey string
			switch c.wantLevel {
			case slog.LevelError:
				wantKey = "level=ERROR"
			case slog.LevelWarn:
				wantKey = "level=WARN"
			default:
				t.Fatalf("unexpected wantLevel: %v", c.wantLevel)
			}
			if !strings.Contains(out, wantKey) {
				t.Errorf("expected %q in log, got: %q", wantKey, out)
			}
		})
	}
}
