// Package oci — Cosign signature badge (D-08, OCI-10).
//
// Tag-presence badge only: for a manifest at sha256:<hex>, "signed" iff a
// tag named "sha256-<hex>.sig" exists in the same docker repo. No crypto,
// no Sigstore, no network. Exposed via /api/v1/projects/{project}/repos/
// docker/{repo}/tags/{tag}/cosign rather than under /v2 because it is an
// OmniRepo-specific indicator, not part of the OCI Distribution surface.
package oci

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/dxc-internal/omnirepo/internal/auth"
	authmw "github.com/dxc-internal/omnirepo/internal/auth/middleware"
)

// CosignTag derives the conventional cosign .sig tag from a manifest digest.
//
//	sha256:<hex> → sha256-<hex>.sig
//
// Exported so REST handlers and tests can re-use the exact derivation.
func CosignTag(digest string) string {
	return strings.Replace(digest, ":", "-", 1) + ".sig"
}

// cosignBadgeResponse is the JSON envelope returned by cosignBadge.
type cosignBadgeResponse struct {
	Signed bool   `json:"signed"`
	Tag    string `json:"tag"`              // sig tag that was probed
	Digest string `json:"digest,omitempty"` // manifest digest resolved from the requested tag
}

// cosignBadge handles GET /api/v1/projects/{project}/repos/docker/{repo}/tags/{tag}/cosign.
// Returns {"signed": bool, "tag": "sha256-<hex>.sig", "digest": "sha256:..."}.
func (h *Handler) cosignBadge(w http.ResponseWriter, r *http.Request) {
	projectName := chi.URLParam(r, "project")
	repoName := chi.URLParam(r, "repo")
	tag := chi.URLParam(r, "tag")
	if projectName == "" || repoName == "" || tag == "" {
		writeJSONErr(w, http.StatusBadRequest, "missing path parameters")
		return
	}

	// Require an actor (anonymous-read allowed only if the repo is public).
	actor, ok := auth.ActorFromContext(r.Context())
	if !ok {
		writeJSONErr(w, http.StatusUnauthorized, "authentication required")
		return
	}

	ctx := r.Context()
	p, err := h.projects.FindByName(ctx, projectName)
	if err != nil || p == nil {
		writeJSONErr(w, http.StatusNotFound, "project not found")
		return
	}
	rr, err := h.repos.FindByTriple(ctx, p.ID, "docker", repoName)
	if err != nil || rr == nil {
		writeJSONErr(w, http.StatusNotFound, "repo not found")
		return
	}

	// Authorize via the same policy engine.
	ctx = auth.ResolveMembership(ctx, actor, h.members)
	target := auth.Target{
		Kind:       "repo",
		ProjectID:  rr.ProjectID,
		RepoID:     rr.ID,
		PublicRead: rr.PublicRead,
	}
	if allowed, reason := auth.Can(ctx, actor, auth.ActionRepoRead, target); !allowed {
		writeJSONErr(w, http.StatusForbidden, reason)
		return
	}

	digest, err := h.tags.Resolve(ctx, rr.ID, "", tag)
	if err != nil {
		writeJSONErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if digest == "" {
		writeJSONErr(w, http.StatusNotFound, fmt.Sprintf("tag %q not found", tag))
		return
	}

	sigTag := CosignTag(digest)
	signed, err := h.tags.ExistsTag(ctx, rr.ID, "", sigTag)
	if err != nil {
		writeJSONErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(cosignBadgeResponse{
		Signed: signed,
		Tag:    sigTag,
		Digest: digest,
	})
}

// MountCosign registers the cosign badge endpoint on parent at
// /api/v1/projects/{project}/repos/docker/{repo}/tags/{tag}/cosign.
//
// Applies BasicOrAPIKey so the endpoint accepts the same credentials as
// every other /api/v1 route. Bearer tokens are NOT accepted — this endpoint
// is OmniRepo-specific, not part of the OCI Distribution surface. For tests
// and clients that already hold a Docker bearer token, reuse the Basic
// credentials that minted it.
func (h *Handler) MountCosign(parent chi.Router) {
	midDeps := authmw.Deps{
		Users:    h.users,
		Sessions: h.sessions,
		APIKeys:  h.apiKeys,
	}
	parent.With(authmw.BasicOrAPIKey(midDeps)).Get(
		"/api/v1/projects/{project}/repos/docker/{repo}/tags/{tag}/cosign",
		h.cosignBadge,
	)
}

// writeJSONErr writes a minimal {"error":"..."} envelope.
func writeJSONErr(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(struct {
		Error string `json:"error"`
	}{Error: msg})
	_ = errors.New // keep errors import if future extensions need it
}
