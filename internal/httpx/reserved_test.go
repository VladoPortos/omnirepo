package httpx_test

import (
	"net/http"
	"testing"

	"github.com/vladoportos/omnirepo/internal/httpx"
)

func TestIsReserved(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"v2", true},
		{"s3", true},
		{"git", true},
		{"api", true},
		{"ui", true},
		{"assets", true},
		{"static", true},
		{"login", true},
		{"logout", true},
		{"healthz", true},
		{"readyz", true},
		{"acme", false},
		{"V2", false}, // case-sensitive
		{"", false},
		{"v2/subpath", false}, // exact-match only
	}
	for _, c := range cases {
		if got := httpx.IsReserved(c.in); got != c.want {
			t.Errorf("IsReserved(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestReservedPrefixesContainsAll(t *testing.T) {
	want := []string{
		"v2", "s3", "git", "api", "ui", "assets", "static",
		"login", "logout", "healthz", "readyz",
	}
	if len(httpx.ReservedPrefixes) != len(want) {
		t.Fatalf("ReservedPrefixes len = %d, want %d", len(httpx.ReservedPrefixes), len(want))
	}
	for i, w := range want {
		if httpx.ReservedPrefixes[i] != w {
			t.Errorf("ReservedPrefixes[%d] = %q, want %q", i, httpx.ReservedPrefixes[i], w)
		}
	}
}

// Phase 4 Plan 03 — defensive check that s3 and git remain reserved, since
// Phase 4 is the first phase that actually mounts handlers at those prefixes.
func TestPhase04ReservedIncludesS3AndGit(t *testing.T) {
	for _, p := range []string{"s3", "git"} {
		if !httpx.IsReserved(p) {
			t.Fatalf("reserved prefix missing: %s", p)
		}
	}
}

func TestMountReservedPanicsOnReservedPrefix(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("MountReserved did not panic on reserved prefix")
		}
	}()
	// We use a nil handler because the panic fires before any handler invocation.
	httpx.MountReserved(nil, "v2", nil)
}

func TestMountReservedAcceptsNonReserved(t *testing.T) {
	// Use a real chi router; we do not need to panic here.
	r := httpx.NewBareRouter()
	httpx.MountReserved(r, "acme-project", emptyHandler{})
}

type emptyHandler struct{}

func (emptyHandler) ServeHTTP(_ http.ResponseWriter, _ *http.Request) {}
