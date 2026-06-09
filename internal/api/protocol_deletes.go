// Package api — session-authed row-delete REST shims.
//
// The protocol DELETE routes (/<project>/rpm/<repo>/packages/<f>, etc.) are
// mounted with BasicOrAPIKey middleware — a browser session cookie can't
// drive them. The UI needs row-level Delete buttons to work from the
// authenticated SPA, so we surface the same handlers under /api/v1 where
// SessionOrAPIKey is already active.
//
// Each handler's resolveRepo accepts either {project} or {name} as the
// URL param, so the identical method serves both the protocol and REST
// mount points. No logic duplication.
package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// ProtocolDeleteHandler is the minimal interface each shim needs from the
// protocol handler. rpm.Handler, deb.Handler, pypi.Handler, helm.Handler
// all satisfy it via their DeleteREST method.
type ProtocolDeleteHandler interface {
	DeleteREST(w http.ResponseWriter, r *http.Request)
}

// ProtocolDeletesDeps bundles the four protocol handlers whose row-delete
// surface is re-exposed under /api/v1. Any field may be nil — the route is
// skipped for that protocol. Mirrors the OCIActionsDeps pattern.
type ProtocolDeletesDeps struct {
	RPM   ProtocolDeleteHandler
	DEB   ProtocolDeleteHandler
	PyPI  ProtocolDeleteHandler
	Helm  ProtocolDeleteHandler
	Go    ProtocolDeleteHandler
	NPM   ProtocolDeleteHandler
	Maven ProtocolDeleteHandler
}

// RegisterProtocolDeleteRoutes mounts session-authed DELETE routes that
// dispatch to the protocol handlers' existing delete logic.
//
// Route paths match the UI/API convention: /api/v1/projects/{name}/repos/
// <type>/{repo}/<content-prefix>/*. The trailing wildcard is only needed
// for DEB, whose packages live at pool/<component>/<first-letter>/<name>/
// <filename>.deb. RPM/PyPI use a flat packages/{filename} shape; Helm
// uses charts/{filename}.
func RegisterProtocolDeleteRoutes(r chi.Router, d *ProtocolDeletesDeps) {
	if d == nil {
		return
	}
	if d.RPM != nil {
		r.Delete("/projects/{name}/repos/rpm/{repo}/packages/{filename}", d.RPM.DeleteREST)
	}
	if d.DEB != nil {
		r.Delete("/projects/{name}/repos/deb/{repo}/pool/*", d.DEB.DeleteREST)
	}
	if d.PyPI != nil {
		r.Delete("/projects/{name}/repos/pypi/{repo}/packages/{filename}", d.PyPI.DeleteREST)
	}
	if d.Helm != nil {
		r.Delete("/projects/{name}/repos/helm/{repo}/charts/{filename}", d.Helm.DeleteREST)
	}
	if d.Go != nil {
		// Wildcard carries <escaped-module>/@v/<version> — same tail shape
		// as the protocol-native DELETE route.
		r.Delete("/projects/{name}/repos/go/{repo}/*", d.Go.DeleteREST)
	}
	if d.NPM != nil {
		// Wildcard carries <name>/-/<version> — same tail shape as the
		// protocol-native DELETE route.
		r.Delete("/projects/{name}/repos/npm/{repo}/*", d.NPM.DeleteREST)
	}
	if d.Maven != nil {
		// Wildcard carries the layout path of the file to delete.
		r.Delete("/projects/{name}/repos/maven/{repo}/*", d.Maven.DeleteREST)
	}
}
