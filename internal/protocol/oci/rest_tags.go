package oci

import (
	"database/sql"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/vladoportos/omnirepo/internal/audit"
	"github.com/vladoportos/omnirepo/internal/auth"
	"github.com/vladoportos/omnirepo/internal/metadata"
)

// DeleteTagREST is the session-authed REST shim — the UI
// can't call OCI /v2/.../manifests/<ref> DELETE directly because that
// endpoint requires Bearer auth from /v2/token, which the browser session
// cookie can't mint. This wrapper performs the same tag-form delete the
// OCI handler does at manifests.go:manifestDelete (tag unlink +
// cascade-on-last-reference), only with session/API-key auth already
// established by the admin REST middleware chain.
type DeleteTagREST struct {
	h *Handler
}

// NewDeleteTagREST constructs the handler.
func NewDeleteTagREST(h *Handler) *DeleteTagREST {
	return &DeleteTagREST{h: h}
}

// deleteTagResponse is the 200 body on success.
type deleteTagResponse struct {
	Deleted bool   `json:"deleted"`
	Digest  string `json:"digest"`
}

// Handle implements http.Handler for
// DELETE /api/v1/projects/{name}/repos/docker/{repo}/tags/{tag}.
func (d *DeleteTagREST) Handle(w http.ResponseWriter, r *http.Request) {
	actor, ok := auth.ActorFromContext(r.Context())
	if !ok || actor.Kind == auth.ActorKindAnonymous {
		writeActionErr(w, http.StatusUnauthorized, "unauthenticated", "")
		return
	}

	projectName := chi.URLParam(r, "name")
	repoName := chi.URLParam(r, "repo")
	tag := chi.URLParam(r, "tag")
	if projectName == "" || repoName == "" || tag == "" {
		writeActionErr(w, http.StatusBadRequest, "validation_failed", "missing project, repo, or tag")
		return
	}

	ctx := r.Context()

	project, err := d.h.projects.FindByName(ctx, projectName)
	if err != nil || project == nil {
		writeActionErr(w, http.StatusNotFound, "not_found", "project")
		return
	}
	rr, err := d.h.repos.FindByTriple(ctx, project.ID, "docker", repoName)
	if err != nil || rr == nil {
		writeActionErr(w, http.StatusNotFound, "not_found", "repo")
		return
	}
	// Write-intent check — same Action as blob push / manifest delete via
	// the /v2 endpoint. Project-member-or-super-admin gate via ResolveMembership.
	if allowed, reason := d.h.canOnRepo(ctx, actor, auth.ActionUpdateRepo, rr); !allowed {
		writeActionErr(w, http.StatusForbidden, "forbidden", reason)
		return
	}

	// Tag → digest. Image="" matches how the /v2 Docker surface stores
	// single-image repos, mirroring manifestDelete's tag-form branch.
	targetDigest, err := d.h.tags.Resolve(ctx, rr.ID, "", tag)
	if err != nil {
		writeActionErr(w, http.StatusInternalServerError, "resolve_failed", "")
		return
	}
	if targetDigest == "" {
		writeActionErr(w, http.StatusNotFound, "not_found", "tag")
		return
	}

	m, err := d.h.manifests.GetByDigest(ctx, rr.ID, targetDigest)
	if err != nil {
		writeActionErr(w, http.StatusInternalServerError, "manifest_lookup_failed", "")
		return
	}
	if m == nil {
		writeActionErr(w, http.StatusNotFound, "not_found", "manifest gone")
		return
	}
	refs, isIndex, refsErr := manifestRefs(m.Body)
	if refsErr != nil {
		// Match /v2 manifestDelete — a parse failure here is never silently
		// swallowed; the stored body is broken and blob-refcount
		// bookkeeping can't be done safely without it.
		writeActionErr(w, http.StatusBadRequest, "manifest_invalid", refsErr.Error())
		return
	}

	cascaded := false
	err = d.h.db.WriteTx(ctx, func(tx *sql.Tx) error {
		if _, derr := d.h.tags.Delete(ctx, tx, rr.ID, "", tag); derr != nil {
			return derr
		}
		count, cerr := d.h.tags.CountForDigestTx(ctx, tx, rr.ID, targetDigest)
		if cerr != nil {
			return cerr
		}
		// Other tags still point at this digest, or the manifest itself is
		// referenced by an index parent — stop after the tag unlink.
		if count > 0 || m.RefCount > 0 {
			return nil
		}
		// Last reference. Cascade to full manifest delete: ref-decrements,
		// row removal, FTS cleanup. Mirrors manifestDelete's tag-form
		// cascade exactly.
		if derr := d.h.decRefs(ctx, tx, rr.ID, refs, isIndex); derr != nil {
			return derr
		}
		if derr := d.h.manifests.Delete(ctx, tx, rr.ID, targetDigest); derr != nil {
			return derr
		}
		cascaded = true
		return metadata.IndexArtifactDelete(ctx, tx, rr.ID, targetDigest)
	})
	if err != nil {
		writeActionErr(w, http.StatusInternalServerError, "delete_failed", "")
		return
	}

	// Reuse the OCI manifestDelete audit shape so /v2 and the REST shim
	// produce indistinguishable audit-log rows for the same logical event.
	fullPath := projectName + "/docker/" + repoName
	d.h.emitManifestAudit(r, audit.EvtOCITagDeleted, targetDigest, "ok", map[string]any{
		"repo":     fullPath,
		"tag":      tag,
		"cascaded": cascaded,
	})

	writeActionOK(w, http.StatusOK, deleteTagResponse{Deleted: true, Digest: targetDigest})
}
