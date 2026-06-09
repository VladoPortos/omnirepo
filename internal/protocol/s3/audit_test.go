package s3

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vladoportos/omnirepo/internal/audit"
	"github.com/vladoportos/omnirepo/internal/auth"
)

// recordingLogger captures recorded events for assertions.
type recordingLogger struct{ events []audit.Event }

func (l *recordingLogger) Record(_ context.Context, e audit.Event) error {
	l.events = append(l.events, e)
	return nil
}

func runAudited(t *testing.T, log *recordingLogger, method, target string, status int, withActor bool) {
	t.Helper()
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
	})
	h := AuditMiddleware(log)(inner)
	r := httptest.NewRequest(method, target, nil)
	if withActor {
		id := int64(77)
		r = r.WithContext(auth.WithActor(r.Context(), auth.Actor{
			Kind:    auth.ActorKindS3Key,
			S3KeyID: &id,
		}))
	}
	h.ServeHTTP(httptest.NewRecorder(), r)
}

func TestAuditMiddleware_ObjectPut(t *testing.T) {
	log := &recordingLogger{}
	runAudited(t, log, http.MethodPut, "/s3/bkt/dir/file.bin", http.StatusOK, true)

	if len(log.events) != 1 {
		t.Fatalf("events = %d, want 1", len(log.events))
	}
	e := log.events[0]
	if e.Kind != audit.EvtS3ObjectPut || e.TargetKind != "s3_object" || e.TargetID != "bkt/dir/file.bin" {
		t.Errorf("event = %+v", e)
	}
	if e.Outcome != "ok" {
		t.Errorf("outcome = %q, want ok", e.Outcome)
	}
	if e.ActorS3KeyID == nil || *e.ActorS3KeyID != 77 {
		t.Errorf("s3 key actor not attributed: %+v", e.ActorS3KeyID)
	}
	if e.Details["bucket"] != "bkt" || e.Details["key"] != "dir/file.bin" {
		t.Errorf("details = %+v", e.Details)
	}
}

func TestAuditMiddleware_Classification(t *testing.T) {
	cases := []struct {
		name     string
		method   string
		target   string
		wantKind audit.EventKind
		wantNone bool
	}{
		{"get-not-audited", http.MethodGet, "/s3/bkt/key", "", true},
		{"head-not-audited", http.MethodHead, "/s3/bkt/key", "", true},
		{"list-root-not-audited", http.MethodGet, "/s3/", "", true},
		{"part-upload-not-audited", http.MethodPut, "/s3/bkt/key?partNumber=2&uploadId=abc", "", true},
		{"multipart-create-not-audited", http.MethodPost, "/s3/bkt/key?uploads", "", true},
		{"object-delete", http.MethodDelete, "/s3/bkt/key", audit.EvtS3ObjectDelete, false},
		{"multipart-abort", http.MethodDelete, "/s3/bkt/key?uploadId=abc", audit.EvtS3MultipartAborted, false},
		{"multipart-complete", http.MethodPost, "/s3/bkt/key?uploadId=abc", audit.EvtS3MultipartCompleted, false},
		{"bucket-create", http.MethodPut, "/s3/bkt", audit.EvtS3BucketCreated, false},
		{"bucket-delete", http.MethodDelete, "/s3/bkt", audit.EvtS3BucketDeleted, false},
		{"batch-delete", http.MethodPost, "/s3/bkt?delete", audit.EvtS3ObjectDelete, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			log := &recordingLogger{}
			runAudited(t, log, c.method, c.target, http.StatusOK, true)
			if c.wantNone {
				if len(log.events) != 0 {
					t.Fatalf("unexpected event: %+v", log.events[0])
				}
				return
			}
			if len(log.events) != 1 {
				t.Fatalf("events = %d, want 1", len(log.events))
			}
			if log.events[0].Kind != c.wantKind {
				t.Errorf("kind = %q, want %q", log.events[0].Kind, c.wantKind)
			}
		})
	}
}

func TestAuditMiddleware_OutcomeAndBucketSource(t *testing.T) {
	// 403 → denied.
	log := &recordingLogger{}
	runAudited(t, log, http.MethodDelete, "/s3/bkt/key", http.StatusForbidden, true)
	if len(log.events) != 1 || log.events[0].Outcome != "denied" {
		t.Fatalf("denied outcome not recorded: %+v", log.events)
	}

	// 500 → failed.
	log = &recordingLogger{}
	runAudited(t, log, http.MethodPut, "/s3/bkt/key", http.StatusInternalServerError, true)
	if len(log.events) != 1 || log.events[0].Outcome != "failed" {
		t.Fatalf("failed outcome not recorded: %+v", log.events)
	}

	// Bucket-level events carry source=s3-api so they're distinguishable
	// from the REST provisioning endpoint's events of the same kind.
	log = &recordingLogger{}
	runAudited(t, log, http.MethodPut, "/s3/newbkt", http.StatusOK, true)
	if len(log.events) != 1 || log.events[0].Details["source"] != "s3-api" {
		t.Fatalf("bucket event missing source detail: %+v", log.events)
	}

	// Actor absent → no attribution, no panic.
	log = &recordingLogger{}
	runAudited(t, log, http.MethodPut, "/s3/bkt/key", http.StatusOK, false)
	if len(log.events) != 1 || log.events[0].ActorS3KeyID != nil {
		t.Fatalf("unexpected attribution without actor: %+v", log.events)
	}
}

func TestAuditMiddleware_NilLoggerPassThrough(t *testing.T) {
	called := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })
	h := AuditMiddleware(nil)(inner)
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPut, "/s3/b/k", nil))
	if !called {
		t.Fatalf("nil-logger middleware must pass through")
	}
}
