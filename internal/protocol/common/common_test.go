package common_test

// RequireRepoWrite and ActorCanRead are exercised end-to-end by every
// protocol package's auth tests (rpm/deb/pypi/helm/raw push+delete suites);
// the tests here cover the pure helpers.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vladoportos/omnirepo/internal/audit"
	"github.com/vladoportos/omnirepo/internal/auth"
	"github.com/vladoportos/omnirepo/internal/protocol/common"
)

func TestValidateFilename(t *testing.T) {
	cases := []struct {
		name   string
		in     string
		suffix string
		ok     bool
	}{
		{"plain", "pkg-1.0.tgz", "", true},
		{"empty", "", "", false},
		{"nul", "a\x00b", "", false},
		{"slash", "a/b", "", false},
		{"backslash", `a\b`, "", false},
		{"dot", ".", "", false},
		{"dotdot", "..", "", false},
		{"dotdot-prefix", "..hidden", "", true}, // legal: not a traversal
		{"non-canonical", "a/../b", "", false},
		{"suffix-ok", "x-1.el9.rpm", ".rpm", true},
		{"suffix-missing", "x-1.el9.deb", ".rpm", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := common.ValidateFilename(c.in, c.suffix)
			if c.ok && (err != nil || got != c.in) {
				t.Errorf("ValidateFilename(%q,%q) = (%q,%v), want pass-through", c.in, c.suffix, got, err)
			}
			if !c.ok && err == nil {
				t.Errorf("ValidateFilename(%q,%q) accepted, want error", c.in, c.suffix)
			}
		})
	}
}

func TestRepoURLExtractor(t *testing.T) {
	ex := common.RepoURLExtractor("helm")
	cases := []struct {
		path                  string
		wantOK                bool
		wantProject, wantRepo string
	}{
		{"/acme/helm/charts-repo/index.yaml", true, "acme", "charts-repo"},
		{"/acme/helm/charts-repo", true, "acme", "charts-repo"},
		{"/acme/rpm/el9/Packages/x.rpm", false, "", ""},
		{"/acme/helm", false, "", ""},
		{"//helm/x", false, "", ""},
		{"/acme/helm//index.yaml", false, "", ""},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", c.path, nil)
		project, typ, repo, ok := ex(r)
		if ok != c.wantOK {
			t.Errorf("%s: ok = %v, want %v", c.path, ok, c.wantOK)
			continue
		}
		if !ok {
			continue
		}
		if project != c.wantProject || repo != c.wantRepo || typ != "helm" {
			t.Errorf("%s: got (%s,%s,%s)", c.path, project, typ, repo)
		}
	}
}

func TestSkipIfActor(t *testing.T) {
	var mwRan bool
	mw := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mwRan = true
			next.ServeHTTP(w, r)
		})
	}
	final := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	wrapped := common.SkipIfActor(mw)(final)

	// No actor in ctx → middleware runs.
	mwRan = false
	wrapped.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/x", nil))
	if !mwRan {
		t.Errorf("middleware should run when no actor present")
	}

	// Actor in ctx → middleware skipped.
	mwRan = false
	r := httptest.NewRequest("GET", "/x", nil)
	r = r.WithContext(auth.WithActor(r.Context(), auth.Actor{Kind: auth.ActorKindAnonymous}))
	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, r)
	if mwRan {
		t.Errorf("middleware must be skipped when actor already present")
	}
	if rec.Code != http.StatusNoContent {
		t.Errorf("final handler did not run: %d", rec.Code)
	}
}

func TestAttachAnonymous(t *testing.T) {
	ctx := common.AttachAnonymous(context.Background())
	a, ok := auth.ActorFromContext(ctx)
	if !ok || a.Kind != auth.ActorKindAnonymous {
		t.Errorf("AttachAnonymous did not attach anonymous actor: %+v ok=%v", a, ok)
	}
}

// recordingLogger captures the last audit event for assertions.
type recordingLogger struct{ last *audit.Event }

func (l *recordingLogger) Record(_ context.Context, e audit.Event) error {
	l.last = &e
	return nil
}

func TestAuditEvent(t *testing.T) {
	// nil logger: must not panic.
	r := httptest.NewRequest("DELETE", "/acme/rpm/el9/packages/x.rpm", nil)
	common.AuditEvent(nil, r, audit.EventKind("rpm.delete"), "rpm_package", "x.rpm", "success", nil)

	// user actor attribution.
	log := &recordingLogger{}
	r = r.WithContext(auth.WithActor(r.Context(), auth.Actor{Kind: auth.ActorKindUser, ID: 42}))
	r.Header.Set("User-Agent", "test-agent")
	common.AuditEvent(log, r, audit.EventKind("rpm.delete"), "rpm_package", "x.rpm", "success",
		map[string]any{"k": "v"})
	if log.last == nil {
		t.Fatalf("no event recorded")
	}
	e := log.last
	if e.TargetKind != "rpm_package" || e.TargetID != "x.rpm" || e.Outcome != "success" {
		t.Errorf("event fields wrong: %+v", e)
	}
	if e.ActorUserID == nil || *e.ActorUserID != 42 {
		t.Errorf("user actor not attributed: %+v", e.ActorUserID)
	}
	if e.UserAgent != "test-agent" {
		t.Errorf("user agent missing: %q", e.UserAgent)
	}
	if e.OccurredAt.IsZero() {
		t.Errorf("OccurredAt not set")
	}

	// api-key actor attribution.
	log = &recordingLogger{}
	r2 := httptest.NewRequest("PUT", "/x", nil)
	r2 = r2.WithContext(auth.WithActor(r2.Context(), auth.Actor{Kind: auth.ActorKindAPIKey, APIKeyID: 7}))
	common.AuditEvent(log, r2, audit.EventKind("rpm.upload"), "rpm_package", "y.rpm", "success", nil)
	if log.last == nil || log.last.ActorAPIKeyID == nil || *log.last.ActorAPIKeyID != 7 {
		t.Errorf("api key actor not attributed: %+v", log.last)
	}
}

func TestWriteSeverityBlocked(t *testing.T) {
	rec := httptest.NewRecorder()
	common.WriteSeverityBlocked(rec, "critical", 99)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("content-type = %q", ct)
	}
	want := `{"error":"blocked_by_scan","severity":"critical","scan_id":99}`
	if rec.Body.String() != want {
		t.Errorf("body = %s, want %s", rec.Body.String(), want)
	}
}
