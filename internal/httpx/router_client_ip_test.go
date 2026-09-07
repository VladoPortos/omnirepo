package httpx

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRouterPreservesPeerAddressWithUntrustedForwardingHeaders(t *testing.T) {
	for _, header := range []string{"True-Client-IP", "X-Real-IP", "X-Forwarded-For"} {
		t.Run(header, func(t *testing.T) {
			router := New(Deps{})
			var peer string
			router.Get("/peer", func(w http.ResponseWriter, r *http.Request) {
				peer = r.RemoteAddr
				w.WriteHeader(http.StatusNoContent)
			})
			req := httptest.NewRequest(http.MethodGet, "/peer", nil)
			req.RemoteAddr = "192.0.2.10:12345"
			req.Header.Set(header, "203.0.113.20")
			router.ServeHTTP(httptest.NewRecorder(), req)
			if peer != "192.0.2.10:12345" {
				t.Fatalf("forwarding header replaced socket peer: %q", peer)
			}
		})
	}
}
