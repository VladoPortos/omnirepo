// Package oci — promote / retag (Phase 02-10, D-05, OCI-09).
//
// Zero-blob-copy retag between two local Docker repos. The handler
// operates entirely through metadata: a single writer tx inserts the
// manifest row in the destination repo (same digest + same bytes), upserts
// the dst tag, increments per-blob ref_count for every referenced layer +
// config (or per-child-manifest ref_count for indexes), and — on tag
// overwrite — decrements the prior refs (Pitfall 1, handled by the shared
// writeManifestWithRefcounts helper from manifests.go).
//
// Invariants:
//
//   - Actor must be a member of BOTH the src and dst projects (or super-admin).
//   - src tag MUST resolve to an existing manifest.
//   - dst manifest body is byte-identical to src (same digest).
//   - NO blob copy: CAS file count before == after. Proven by
//     TestPromote_ZeroBlobCopy counting files in blobs/sha256 before+after.
package oci

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/dxc-internal/omnirepo/internal/audit"
	"github.com/dxc-internal/omnirepo/internal/auth"
	"github.com/dxc-internal/omnirepo/internal/metadata"
)

// maxPromoteRequestBodyBytes caps the REST body.
const maxPromoteRequestBodyBytes = 8 * 1024

// PromoteRequest is the POST body for
// POST /api/v1/projects/{src_project}/repos/docker/{src_repo}/promote.
type PromoteRequest struct {
	SrcTag     string `json:"src_tag"`
	DstProject string `json:"dst_project"`
	DstRepo    string `json:"dst_repo"`
	DstTag     string `json:"dst_tag"`
}

// PromoteResponse is the 200 body on success.
type PromoteResponse struct {
	DstProject string `json:"dst_project"`
	DstRepo    string `json:"dst_repo"`
	DstTag     string `json:"dst_tag"`
	Digest     string `json:"digest"`
}

// PromoteREST is the REST handler for promote. Lives on the OCI Handler so
// it can reuse the existing repo + membership + auth helpers.
type PromoteREST struct {
	h *Handler
}

// NewPromoteREST constructs the handler.
func NewPromoteREST(h *Handler) *PromoteREST {
	return &PromoteREST{h: h}
}

// Handle implements http.Handler for POST
// /api/v1/projects/{name}/repos/docker/{repo}/promote.
func (p *PromoteREST) Handle(w http.ResponseWriter, r *http.Request) {
	actor, ok := auth.ActorFromContext(r.Context())
	if !ok || actor.Kind == auth.ActorKindAnonymous {
		writeActionErr(w, http.StatusUnauthorized, "unauthenticated", "")
		return
	}

	srcProjectName := chi.URLParam(r, "name")
	srcRepoName := chi.URLParam(r, "repo")
	if srcProjectName == "" || srcRepoName == "" {
		writeActionErr(w, http.StatusBadRequest, "validation_failed", "missing project or repo")
		return
	}

	var req PromoteRequest
	r.Body = http.MaxBytesReader(w, r.Body, maxPromoteRequestBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeActionErr(w, http.StatusBadRequest, "validation_failed", "invalid JSON: "+err.Error())
		return
	}
	if req.SrcTag == "" || req.DstProject == "" || req.DstRepo == "" || req.DstTag == "" {
		writeActionErr(w, http.StatusBadRequest, "validation_failed", "src_tag, dst_project, dst_repo, dst_tag required")
		return
	}

	// Resolve src.
	srcProject, err := p.h.projects.FindByName(r.Context(), srcProjectName)
	if err != nil || srcProject == nil {
		writeActionErr(w, http.StatusNotFound, "not_found", "src project")
		return
	}
	srcRepo, err := p.h.repos.FindByTriple(r.Context(), srcProject.ID, "docker", srcRepoName)
	if err != nil || srcRepo == nil {
		writeActionErr(w, http.StatusNotFound, "not_found", "src repo")
		return
	}

	// Resolve dst.
	dstProject, err := p.h.projects.FindByName(r.Context(), req.DstProject)
	if err != nil || dstProject == nil {
		writeActionErr(w, http.StatusNotFound, "not_found", "dst project")
		return
	}
	dstRepo, err := p.h.repos.FindByTriple(r.Context(), dstProject.ID, "docker", req.DstRepo)
	if err != nil || dstRepo == nil {
		writeActionErr(w, http.StatusNotFound, "not_found", "dst repo")
		return
	}

	// Authorization: read on src, write on dst. Both required.
	if ok, reason := p.h.canOnRepo(r.Context(), actor, auth.ActionRepoRead, srcRepo); !ok {
		writeActionErr(w, http.StatusForbidden, "forbidden_src", reason)
		return
	}
	if ok, reason := p.h.canOnRepo(r.Context(), actor, auth.ActionUpdateRepo, dstRepo); !ok {
		writeActionErr(w, http.StatusForbidden, "forbidden_dst", reason)
		return
	}

	// Resolve src tag → digest. WR-03: never echo raw Go error strings on
	// 5xx — they can carry file paths, SQL snippets, and internal state.
	// Log server-side with slog and respond with an empty detail.
	digest, err := p.h.tags.Resolve(r.Context(), srcRepo.ID, req.SrcTag)
	if err != nil {
		slog.ErrorContext(r.Context(), "oci.promote.tags.resolve",
			"src_repo", srcRepo.ID, "tag", req.SrcTag, "err", err)
		writeActionErr(w, http.StatusInternalServerError, "internal", "")
		return
	}
	if digest == "" {
		writeActionErr(w, http.StatusNotFound, "not_found", "src tag")
		return
	}

	// Load src manifest (byte-identical body + media type + refs).
	srcManifest, err := p.h.manifests.GetByDigest(r.Context(), srcRepo.ID, digest)
	if err != nil {
		slog.ErrorContext(r.Context(), "oci.promote.manifests.getbydigest",
			"src_repo", srcRepo.ID, "digest", digest, "err", err)
		writeActionErr(w, http.StatusInternalServerError, "internal", "")
		return
	}
	if srcManifest == nil {
		writeActionErr(w, http.StatusNotFound, "not_found", "src manifest")
		return
	}

	// Parse refs before opening the tx.
	refs, isIndex, err := manifestRefs(srcManifest.Body)
	if err != nil {
		// Malformed stored body — client-facing 400 with stable code, full
		// detail only to the log.
		slog.ErrorContext(r.Context(), "oci.promote.manifest_refs",
			"digest", digest, "err", err)
		writeActionErr(w, http.StatusBadRequest, "manifest_invalid", "stored manifest body is malformed")
		return
	}

	// For non-index manifests, every referenced blob digest must already
	// exist in docker_blobs (they should — they landed when the src
	// manifest was pushed). For cross-project promote where the blob
	// exists only under src's CAS path... it doesn't matter: CAS is
	// project-agnostic (content-addressed by sha256). docker_blobs is a
	// global table indexed by digest, so both src and dst see the same
	// refcount. The per-blob IncRef in writeManifestWithRefcounts is
	// exactly the ref delta we want.
	//
	// For index manifests, every child manifest must exist in the DST
	// repo because docker_manifests is keyed by (repo_id, digest). If
	// the operator is promoting an index whose children are not yet in
	// the dst repo, the ref check inside writeManifestWithRefcounts will
	// fail with "manifest: incref: missing" and the tx rolls back. The
	// caller-friendly fix is to promote each child tag first, then the
	// index; documented as a caveat in the plan.
	repoPath := req.DstProject + "/docker/" + req.DstRepo

	err = p.h.db.WriteTx(r.Context(), func(tx *sql.Tx) error {
		_, err := p.h.writeManifestWithRefcounts(
			r.Context(), tx, dstRepo.ID, repoPath, req.DstTag,
			digest, srcManifest.MediaType, srcManifest.Body,
			refs, isIndex, false, // autoScan deliberately false for promote (already scanned at src)
		)
		return err
	})
	if err != nil {
		// An index promote with missing child refs surfaces here.
		if errors.Is(err, metadata.ErrManifestDigestConflict) {
			// Digest conflicts are caller-caused and safe to echo back.
			writeActionErr(w, http.StatusConflict, "digest_conflict", err.Error())
			return
		}
		slog.ErrorContext(r.Context(), "oci.promote.tx",
			"dst_repo", dstRepo.ID, "digest", digest, "err", err)
		writeActionErr(w, http.StatusInternalServerError, "promote_tx", "")
		return
	}

	// Best-effort audit: oci.promote with src + dst details. Never plaintext
	// secrets (there are none on this path), just repo paths + digest.
	if p.h.auditLogger != nil {
		ev := audit.Event{
			Kind:       audit.EvtOCIPromote,
			TargetKind: "manifest",
			TargetID:   digest,
			OccurredAt: time.Now().UTC(),
			Details: map[string]any{
				"src":        fmt.Sprintf("%s/docker/%s:%s", srcProjectName, srcRepoName, req.SrcTag),
				"dst":        fmt.Sprintf("%s/docker/%s:%s", req.DstProject, req.DstRepo, req.DstTag),
				"digest":     digest,
				"media":      srcManifest.MediaType,
				"size_bytes": len(srcManifest.Body),
			},
		}
		if actor.Kind == auth.ActorKindUser {
			id := actor.ID
			ev.ActorUserID = &id
		} else if actor.Kind == auth.ActorKindAPIKey {
			id := actor.APIKeyID
			ev.ActorAPIKeyID = &id
		}
		_ = p.h.auditLogger.Record(r.Context(), ev)
	}

	writeActionOK(w, http.StatusOK, PromoteResponse{
		DstProject: req.DstProject,
		DstRepo:    req.DstRepo,
		DstTag:     req.DstTag,
		Digest:     digest,
	})
}
