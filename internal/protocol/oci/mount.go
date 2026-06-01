package oci

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/vladoportos/omnirepo/internal/audit"
	"github.com/vladoportos/omnirepo/internal/auth"
	"github.com/vladoportos/omnirepo/internal/metadata"
)

// blobMount implements POST /v2/<name>/blobs/uploads/?mount=<digest>&from=<repo>
// per OCI Distribution Spec v1.1 §4.2.2.
//
// Semantics:
//   - The 'from' parameter identifies a source repo in the form
//     "<project>/<type>/<repo>" or "<project>/<repo>" (shorthand —
//     type is inferred as "docker").
//   - Auth:
//   - actor must have repo.read on the source repo
//   - actor must have repo.write (modeled via ActionUpdateRepo) on
//     the destination repo
//   - If the blob already exists in CAS → 201 Created with Location header.
//     We briefly register the digest in blob_uploads to block GC during
//     the response, mirroring RESEARCH Pattern 3. The actual refcount
//     bump happens on the subsequent manifest PUT (02-07).
//   - If the blob does NOT exist in CAS → fall back to starting a normal
//     chunked upload session, per OCI spec. The response becomes a 202
//     from blobUploadPost.
func (h *Handler) blobMount(w http.ResponseWriter, r *http.Request) {
	dest := h.resolveRepo(w, r, true)
	if dest == nil {
		return
	}

	q := r.URL.Query()
	digest := q.Get("mount")
	fromRaw := q.Get("from")
	if !validDigest(digest) {
		writeOCIErr(w, http.StatusBadRequest, ErrCodeDigestInvalid,
			fmt.Errorf("malformed digest %q", digest))
		return
	}

	fromProject, fromType, fromRepoName, ok := parseFromRepo(fromRaw)
	if !ok {
		writeOCIErr(w, http.StatusBadRequest, ErrCodeNameInvalid,
			fmt.Errorf("malformed from=%q (expected <project>/<type>/<repo>)", fromRaw))
		return
	}

	// Resolve source repo row. Per OCI Distribution Spec v1.1 §4.2.1,
	// when the source of a cross-repo mount cannot be satisfied the
	// server SHOULD fall through to a standard upload-session start
	// rather than surfacing a 404 — docker push first tries to mount
	// from the original upstream path (e.g. "library/alpine" for a
	// retagged docker.io/alpine) and relies on this fall-through to
	// continue when the source project/repo isn't mirrored locally.
	// Returning 404 here bricks every first-time push of an image
	// retagged from docker.io, so we silently fall through instead.
	p, err := h.projects.FindByName(r.Context(), fromProject)
	if err != nil || p == nil {
		h.blobUploadPost(w, r)
		return
	}
	src, err := h.repos.FindByTriple(r.Context(), p.ID, fromType, fromRepoName)
	if err != nil || src == nil {
		h.blobUploadPost(w, r)
		return
	}

	actor, ok := auth.ActorFromContext(r.Context())
	if !ok || actor.Kind == auth.ActorKindAnonymous {
		h.challenge(w, r)
		return
	}
	// Both checks required.
	if ok, reason := h.canOnRepo(r.Context(), actor, auth.ActionRepoRead, src); !ok {
		writeOCIErr(w, http.StatusForbidden, ErrCodeDenied,
			fmt.Errorf("from: %s", reason))
		return
	}
	if ok, reason := h.canOnRepo(r.Context(), actor, auth.ActionUpdateRepo, dest.repo); !ok {
		writeOCIErr(w, http.StatusForbidden, ErrCodeDenied,
			fmt.Errorf("to: %s", reason))
		return
	}

	// Is the blob actually in CAS?
	_, exists, err := h.cas.Stat(r.Context(), digest)
	if err != nil {
		writeOCIErr(w, http.StatusInternalServerError, ErrCodeUnknown, err)
		return
	}
	if !exists {
		// Per OCI spec, fall through to a normal upload session start.
		// We intentionally reuse blobUploadPost so the response shape
		// (202 + Location + Range) matches what clients expect when a
		// mount cannot be satisfied.
		h.blobUploadPost(w, r)
		return
	}

	// Briefly register the digest in blob_uploads inside a writer tx to
	// block GC between the CAS stat above and the upcoming manifest PUT
	// that will ++ ref_count. Also Touch docker_blobs so last_touched_at
	// is recent (keeps the blob out of the quiescence window).
	if h.blobUploads != nil {
		if err := h.blobUploads.Start(r.Context(), digest, time.Hour); err != nil {
			writeOCIErr(w, http.StatusInternalServerError, ErrCodeUnknown, err)
			return
		}
	}
	if err := h.db.WriteTx(r.Context(), func(tx *sql.Tx) error {
		// Ensure the docker_blobs row exists (it should, since CAS has
		// the bytes from a previous upload, but be robust against
		// direct-CAS-import paths). If absent, upsert at ref_count=0
		// with size=0 — the manifest PUT will refine it via Touch.
		b, serr := h.blobs.Stat(r.Context(), digest)
		if serr != nil {
			return serr
		}
		if b == nil {
			if err := h.blobs.UpsertZeroRef(r.Context(), tx, digest, 0); err != nil {
				return err
			}
		}
		return h.blobs.Touch(r.Context(), tx, digest)
	}); err != nil {
		writeOCIErr(w, http.StatusInternalServerError, ErrCodeUnknown, err)
		return
	}

	h.emitAudit(r, audit.EvtOCIBlobMounted, digest, "ok", map[string]any{
		"from": fromRaw,
		"to":   dest.fullPath,
	})

	w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/%s", dest.fullPath, digest))
	w.Header().Set("Docker-Content-Digest", digest)
	w.Header().Set("Content-Length", "0")
	w.WriteHeader(http.StatusCreated)
}

// parseFromRepo accepts the shapes emitted by `docker push` when it tries to
// cross-mount a blob it knows is already present on this registry:
//
//   - "<project>/<repo>"                  — shorthand, type inferred docker
//   - "<project>/<type>/<repo>"           — canonical 3-segment
//   - "<project>/<type>/<repo>/<image>"   — 4-segment (docker push derives
//     this from the PUSH target URL, which the OCI router matches at
//     /v2/{project}/{type}/{repo}/{image}). The <image> segment is below
//     repo-granularity for OmniRepo — the blob lives at repo level — so we
//     drop it and resolve the repo from the first three segments.
//
// Anything else returns ok=false.
func parseFromRepo(raw string) (project, repoType, repoName string, ok bool) {
	if raw == "" {
		return "", "", "", false
	}
	// Reject leading/trailing slashes.
	if strings.HasPrefix(raw, "/") || strings.HasSuffix(raw, "/") {
		return "", "", "", false
	}
	parts := strings.Split(raw, "/")
	switch len(parts) {
	case 2:
		if parts[0] == "" || parts[1] == "" {
			return "", "", "", false
		}
		return parts[0], "docker", parts[1], true
	case 3, 4:
		if parts[0] == "" || parts[1] == "" || parts[2] == "" {
			return "", "", "", false
		}
		if len(parts) == 4 && parts[3] == "" {
			return "", "", "", false
		}
		return parts[0], parts[1], parts[2], true
	default:
		return "", "", "", false
	}
}

// Compile-time reminder: blob mount reuses Handler.resolveRepo + canOnRepo
// + emitAudit from blobs.go. Any change to those helpers' signatures must
// update both files in lockstep.
var _ = errors.New
var _ = metadata.ErrNotFound
