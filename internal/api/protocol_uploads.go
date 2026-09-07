package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// ProtocolUploadHandler reuses a protocol's upload implementation after the
// REST router has authenticated the browser session or API key.
type ProtocolUploadHandler interface {
	PutREST(http.ResponseWriter, *http.Request)
}

type ProtocolUploadsDeps struct {
	RAW ProtocolUploadHandler
}

func RegisterProtocolUploadRoutes(r chi.Router, d *ProtocolUploadsDeps) {
	if d != nil && d.RAW != nil {
		r.Put("/projects/{name}/repos/raw/{repo}/artifacts/*", d.RAW.PutREST)
	}
}
