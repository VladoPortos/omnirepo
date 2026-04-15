package httpx

import (
	"net/http"

	"github.com/dxc-internal/omnirepo/internal/metadata"
	omrtls "github.com/dxc-internal/omnirepo/internal/tls"
)

// ReadyzDeps bundles the subsystems /readyz probes.
type ReadyzDeps struct {
	DB     *metadata.DB
	Holder *omrtls.CertHolder
}

// Readyz returns a chi handler that reports readiness (FOUND-06):
//   - DB reader pool must answer Ping
//   - CertHolder must have a certificate loaded (handshake-ready)
//
// Emits `{"status":"ready"}` on 200 or `{"status":"not-ready","reason":"..."}`
// on 503.
func Readyz(d ReadyzDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.DB != nil && d.DB.Reader != nil {
			if err := d.DB.Reader.PingContext(r.Context()); err != nil {
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(`{"status":"not-ready","reason":"db"}`))
				return
			}
		}
		if d.Holder != nil {
			if _, err := d.Holder.Get(nil); err != nil {
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(`{"status":"not-ready","reason":"tls"}`))
				return
			}
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ready"}`))
	}
}
