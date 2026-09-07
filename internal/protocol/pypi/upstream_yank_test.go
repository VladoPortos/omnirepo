package pypi_test

import (
	"context"
	"fmt"
	"github.com/vladoportos/omnirepo/internal/protocol/pypi"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPyPIParseHTMLHashlessYank(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<a href='/packages/acme-1.0.tar.gz' data-yanked='broken &amp; unsafe'>acme-1.0.tar.gz</a>`)
	}))
	defer srv.Close()
	files, err := pypi.ParseUpstreamProject(context.Background(), srv.Client(), srv.URL, "acme", pypi.AuthCreds{})
	if err != nil || len(files) != 1 {
		t.Fatalf("hashless HTML files = %+v, %v", files, err)
	}
	if !strings.Contains(fmt.Sprintf("%+v", files[0]), "broken & unsafe") {
		t.Fatalf("lost yank reason: %+v", files[0])
	}
}

func TestPyPIParseHTMLBareYankAndQuotedGreaterThan(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<a data-requires-python=">=3.8" href="/packages/acme-1.0.tar.gz" data-yanked>acme-1.0.tar.gz</a>`)
	}))
	defer srv.Close()
	files, err := pypi.ParseUpstreamProject(context.Background(), srv.Client(), srv.URL, "acme", pypi.AuthCreds{})
	if err != nil || len(files) != 1 {
		t.Fatalf("files = %+v, %v", files, err)
	}
	if !files[0].Yanked || files[0].RequiresPython != ">=3.8" {
		t.Fatalf("lost HTML attributes: %+v", files[0])
	}
}
