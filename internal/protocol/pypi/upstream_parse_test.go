package pypi_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dxc-internal/omnirepo/internal/protocol/pypi"
)

const pep691IndexBody = `{
  "meta": {"api-version": "1.0"},
  "projects": [{"name": "Flask"}, {"name": "Flask-SQLAlchemy"}]
}`

const pep691ProjectBody = `{
  "meta": {"api-version": "1.0"},
  "name": "flask",
  "files": [
    {
      "filename": "Flask-3.0.0-py3-none-any.whl",
      "url": "files/Flask-3.0.0-py3-none-any.whl",
      "hashes": {"sha256": "aabbcc"},
      "requires-python": ">=3.8",
      "size": 9876
    }
  ]
}`

const html503ProjectBody = `<!DOCTYPE html><html><body>
<a href="/files/Flask-3.0.0.tar.gz#sha256=deadbeef" data-requires-python="&gt;=3.8">Flask-3.0.0.tar.gz</a>
</body></html>`

func TestPyPIParseUpstreamJSONPreferred(t *testing.T) {
	var seenAccept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAccept = r.Header.Get("Accept")
		switch r.URL.Path {
		case "/simple/":
			w.Header().Set("Content-Type", "application/vnd.pypi.simple.v1+json")
			_, _ = w.Write([]byte(pep691IndexBody))
		case "/simple/flask/":
			w.Header().Set("Content-Type", "application/vnd.pypi.simple.v1+json")
			_, _ = w.Write([]byte(pep691ProjectBody))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	ctx := context.Background()
	projects, err := pypi.ParseUpstreamSimpleIndex(ctx, srv.Client(), srv.URL, pypi.AuthCreds{})
	if err != nil {
		t.Fatalf("ParseUpstreamSimpleIndex: %v", err)
	}
	if len(projects) != 2 || projects[0] != "flask" || projects[1] != "flask-sqlalchemy" {
		t.Fatalf("unexpected projects: %v", projects)
	}
	if !strings.Contains(seenAccept, "application/vnd.pypi.simple.v1+json") {
		t.Fatalf("expected JSON in Accept, got %q", seenAccept)
	}

	files, err := pypi.ParseUpstreamProject(ctx, srv.Client(), srv.URL, "flask", pypi.AuthCreds{})
	if err != nil {
		t.Fatalf("ParseUpstreamProject: %v", err)
	}
	if len(files) != 1 || files[0].SHA256 != "aabbcc" || files[0].Size != 9876 {
		t.Fatalf("unexpected files: %+v", files)
	}
	if !strings.HasSuffix(files[0].URL, "/simple/flask/files/Flask-3.0.0-py3-none-any.whl") {
		t.Fatalf("URL not resolved: %s", files[0].URL)
	}
	if files[0].RequiresPython != ">=3.8" {
		t.Fatalf("requires_python missing: %+v", files[0])
	}
}

func TestPyPIParseUpstreamHTMLFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Always return HTML, ignoring Accept.
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch r.URL.Path {
		case "/simple/":
			_, _ = fmt.Fprintln(w, `<a href="flask/">Flask</a>`)
		case "/simple/flask/":
			_, _ = fmt.Fprint(w, html503ProjectBody)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	ctx := context.Background()
	projects, err := pypi.ParseUpstreamSimpleIndex(ctx, srv.Client(), srv.URL, pypi.AuthCreds{})
	if err != nil {
		t.Fatalf("ParseUpstreamSimpleIndex: %v", err)
	}
	if len(projects) != 1 || projects[0] != "flask" {
		t.Fatalf("html fallback projects: %v", projects)
	}

	files, err := pypi.ParseUpstreamProject(ctx, srv.Client(), srv.URL, "flask", pypi.AuthCreds{})
	if err != nil {
		t.Fatalf("ParseUpstreamProject: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d (%+v)", len(files), files)
	}
	if files[0].Filename != "Flask-3.0.0.tar.gz" || files[0].SHA256 != "deadbeef" {
		t.Fatalf("html parse wrong: %+v", files[0])
	}
}

func TestPyPIParseUpstreamCreds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok || u != "alice" || p != "s3cret" {
			http.Error(w, "auth required", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/vnd.pypi.simple.v1+json")
		_, _ = w.Write([]byte(pep691IndexBody))
	}))
	defer srv.Close()

	ctx := context.Background()
	if _, err := pypi.ParseUpstreamSimpleIndex(ctx, srv.Client(), srv.URL, pypi.AuthCreds{}); err == nil {
		t.Fatal("expected 401 without creds")
	}
	if _, err := pypi.ParseUpstreamSimpleIndex(ctx, srv.Client(), srv.URL,
		pypi.AuthCreds{User: "alice", Password: "s3cret"}); err != nil {
		t.Fatalf("with creds: %v", err)
	}
}

func TestPyPIParseUpstreamContextCancel(t *testing.T) {
	hold := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-hold
	}))
	defer srv.Close()
	defer close(hold)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := pypi.ParseUpstreamSimpleIndex(ctx, srv.Client(), srv.URL, pypi.AuthCreds{}); err == nil {
		t.Fatal("expected ctx err")
	}
}

func TestPyPIParseUpstreamTimeout(t *testing.T) {
	hold := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-hold
	}))
	defer srv.Close()
	defer close(hold)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := pypi.ParseUpstreamSimpleIndex(ctx, srv.Client(), srv.URL, pypi.AuthCreds{}); err == nil {
		t.Fatal("expected timeout err")
	}
}

func TestPyPIFilterAcceptsByName(t *testing.T) {
	sf := pypi.SyncFilter{Names: []string{"Flask"}}
	if !sf.AcceptProject("flask") {
		t.Fatal("name filter should accept flask")
	}
	if sf.AcceptProject("requests") {
		t.Fatal("name filter should reject requests")
	}
	f := pypi.UpstreamFile{Filename: "Flask-1.0.tar.gz"}
	if !sf.FilterFile(f, "flask") {
		t.Fatal("file under matching project should pass")
	}
	sfg := pypi.SyncFilter{Globs: []string{"*.whl"}}
	if sfg.FilterFile(f, "flask") {
		t.Fatal("glob *.whl should reject .tar.gz")
	}
	if !sfg.FilterFile(pypi.UpstreamFile{Filename: "Flask-1.0.whl"}, "flask") {
		t.Fatal("glob *.whl should accept .whl")
	}
}
