// Package oci — /v2 blob state machine.
//
// File layout:
//   - blobs.go    : POST/PATCH/PUT/GET status, GET/HEAD/DELETE blob handlers
//   - mount.go    : cross-repo mount (POST ?mount=&from=)
//   - handler.go  : Handler struct, Mount wiring, token routes
//
// Invariants:
//
//  1. Chunk bytes NEVER buffered in memory. PATCH streams through
//     io.LimitReader → append-only O_APPEND|O_WRONLY file at
//     <dataRoot>/tmp/uploads/<uuid>.
//  2. PUT finalization: recompute sha256 over the entire tmp file, then
//     call blobUploads.Start(digest, 1h) BEFORE cas.PutFromPath rename.
//     That ordering closes the GC race — GC sees the digest in the
//     exclusion set before the CAS file appears under its content address.
//  3. After the CAS rename, the writer tx upserts docker_blobs with
//     ref_count=0 and removes the blob_upload_sessions row. The manifest
//     PUT will ++ ref_count; blob_uploads.Complete at that time.
//  4. DELETE only succeeds at ref_count==0. Non-zero → 405 MethodNotAllowed
//     per OCI Distribution conventions (only GC may delete live blobs).
package oci

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/vladoportos/omnirepo/internal/audit"
	"github.com/vladoportos/omnirepo/internal/auth"
	"github.com/vladoportos/omnirepo/internal/metadata"
)

// sha256HexLen is 64 hex chars for a sha256:<hex> digest.
const sha256HexLen = 64

// isUploadUUID reports whether s is a syntactically valid UUID. Defense in
// depth: chi's {uuid} regex defaults to a greedy [^/]+ match, so callers
// that interpolate the URL param into filesystem paths must reject
// malformed values explicitly before touching the session table or disk.
// sess.Lookup also defends (an attacker cannot forge a session row), but
// rejecting early surfaces a clean BLOB_UPLOAD_INVALID instead of relying
// on downstream layers to notice.
func isUploadUUID(s string) bool {
	_, err := uuid.Parse(s)
	return err == nil
}

// resolvedRepo bundles what every blob handler needs after resolving the
// URL-encoded (project, type, repo) triple, plus the optional {image}
// 4th segment that Helm OCI (and nested Docker paths) use. For 3-segment
// URLs image is "".
type resolvedRepo struct {
	project  *metadata.Project
	repo     *metadata.Repo
	image    string // "" for 3-segment paths; chart-name for Helm, image-name for nested Docker
	fullPath string // "<project>/<type>/<repo>" or "<project>/<type>/<repo>/<image>" for Location headers
}

// resolveRepo parses {project}, {type}, {repo} chi URL params plus the
// optional {image} 4th-segment param, validates the triple, and returns the
// repo row. Writes the OCI error envelope and returns nil on any failure.
//
// Routes are registered in two shapes: the classic 3-segment form
// (/v2/{project}/{type}/{repo}/...) and the 4-segment form
// (/v2/{project}/{type}/{repo}/{image}/...). The latter is required for
// Helm OCI — the Helm CLI always appends the chart name as a 4th URL
// segment so each chart is a distinct OCI "image" inside an OmniRepo helm
// repo. Docker clients can also use the 4-segment form to host multiple
// images under a single OmniRepo docker repo.
//
// Accepts only OCI-native repo types: "docker"
// (standard image registry) and "helm" (charts pushed via `helm push
// oci://…`; a post-commit mirror wires OCI-pushed charts into the
// traditional /<project>/helm/<repo>/ tree so `helm repo add` can see
// them). Other repo types are rejected with NAME_INVALID.
func (h *Handler) resolveRepo(w http.ResponseWriter, r *http.Request) *resolvedRepo {
	projectName := chi.URLParam(r, "project")
	repoType := chi.URLParam(r, "type")
	repoName := chi.URLParam(r, "repo")
	image := chi.URLParam(r, "image") // "" when the 3-segment route matched
	if projectName == "" || repoType == "" || repoName == "" {
		writeOCIErr(w, http.StatusBadRequest, ErrCodeNameInvalid, errors.New("missing name components"))
		return nil
	}
	if err := auth.ProjectNameValid(projectName); err != nil {
		writeOCIErr(w, http.StatusBadRequest, ErrCodeNameInvalid, err)
		return nil
	}
	if image != "" {
		if err := auth.RepoNameValid(image); err != nil {
			writeOCIErr(w, http.StatusBadRequest, ErrCodeNameInvalid,
				fmt.Errorf("invalid image/chart segment: %w", err))
			return nil
		}
	}
	// OCI v2 multiplexes Docker registry traffic and Helm OCI traffic on the
	// same /v2 surface. Both speak the distribution protocol; the difference
	// lives in the manifest config mediaType and the post-commit hooks.
	if repoType != "docker" && repoType != "helm" {
		writeOCIErr(w, http.StatusBadRequest, ErrCodeNameInvalid,
			fmt.Errorf("expected type=docker or helm, got %s", repoType))
		return nil
	}
	if h.projects == nil || h.repos == nil {
		writeOCIErr(w, http.StatusInternalServerError, ErrCodeUnknown, errors.New("handler misconfigured"))
		return nil
	}
	p, err := h.projects.FindByName(r.Context(), projectName)
	if err != nil || p == nil {
		writeOCIErr(w, http.StatusNotFound, ErrCodeNameUnknown, err)
		return nil
	}
	rr, err := h.repos.FindByTriple(r.Context(), p.ID, repoType, repoName)
	if err != nil || rr == nil {
		writeOCIErr(w, http.StatusNotFound, ErrCodeNameUnknown, err)
		return nil
	}
	fullPath := projectName + "/" + repoType + "/" + repoName
	if image != "" {
		fullPath += "/" + image
	}
	return &resolvedRepo{
		project:  p,
		repo:     rr,
		image:    image,
		fullPath: fullPath,
	}
}

// canOnRepo wraps auth.Can for a repo target, resolving membership for
// non-super-admin, non-anonymous user actors via MembersRepo.
// Action is one of auth.ActionRepoRead / ActionUpdateRepo / ActionWipeRepo
// etc., but the /v2 blob surface only needs read + a write-intent we model
// with ActionUpdateRepo (write intent reuses the existing ActionUpdateRepo
// constant to avoid a new action just for /v2 blob pushes).
func (h *Handler) canOnRepo(ctx context.Context, actor auth.Actor, action auth.Action, rr *metadata.Repo) (bool, string) {
	ctx = auth.ResolveMembership(ctx, actor, h.members)
	target := auth.Target{
		Kind:       "repo",
		ProjectID:  rr.ProjectID,
		RepoID:     rr.ID,
		PublicRead: rr.PublicRead,
	}
	return auth.Can(ctx, actor, action, target)
}

// requireWriter checks the actor can push to rr; writes 401/403 + OCI
// envelope and returns false on denial. Anonymous actors always get 401.
func (h *Handler) requireWriter(w http.ResponseWriter, r *http.Request, rr *metadata.Repo) bool {
	actor, ok := auth.ActorFromContext(r.Context())
	if !ok || actor.Kind == auth.ActorKindAnonymous {
		h.challenge(w, r)
		return false
	}
	if ok, reason := h.canOnRepo(r.Context(), actor, auth.ActionUpdateRepo, rr); !ok {
		writeOCIErr(w, http.StatusForbidden, ErrCodeDenied, errors.New(reason))
		return false
	}
	return true
}

// requireReader checks the actor can GET/HEAD a blob under rr. Anonymous
// actors whose AnonymousReadOK upstream attached them are allowed iff the
// repo has PublicRead=true.
func (h *Handler) requireReader(w http.ResponseWriter, r *http.Request, rr *metadata.Repo) bool {
	actor, ok := auth.ActorFromContext(r.Context())
	if !ok {
		h.challenge(w, r)
		return false
	}
	if ok, reason := h.canOnRepo(r.Context(), actor, auth.ActionRepoRead, rr); !ok {
		writeOCIErr(w, http.StatusForbidden, ErrCodeDenied, errors.New(reason))
		return false
	}
	return true
}

// blobPostDispatch routes POST /v2/<name>/blobs/uploads/ between three
// shapes per OCI spec §4.2.1-§4.2.2:
//  1. mount+from  → cross-repo mount (mount.go)
//  2. body + digest query → monolithic upload
//  3. empty body → start a new chunked session
func (h *Handler) blobPostDispatch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("mount") != "" && q.Get("from") != "" {
		h.blobMount(w, r)
		return
	}
	if q.Get("digest") != "" {
		h.blobMonolithicPost(w, r)
		return
	}
	h.blobUploadPost(w, r)
}

// blobUploadPost starts a new chunked upload session.
// Response: 202 Accepted, Location: /v2/<name>/blobs/uploads/<uuid>, Range: 0-0.
func (h *Handler) blobUploadPost(w http.ResponseWriter, r *http.Request) {
	rr := h.resolveRepo(w, r)
	if rr == nil {
		return
	}
	if !h.requireWriter(w, r, rr.repo) {
		return
	}

	u := uuid.NewString()
	// 1h default session TTL; matches the blob_uploads 1h ttl and keeps
	// resumable uploads simple.
	if err := h.db.WriteTx(r.Context(), func(tx *sql.Tx) error {
		return h.sess.Create(r.Context(), tx, u, rr.repo.ID, time.Hour)
	}); err != nil {
		writeOCIErr(w, http.StatusInternalServerError, ErrCodeUnknown, err)
		return
	}
	// Create an empty tmp file now so PATCH's O_APPEND|O_WRONLY open
	// finds the path without racing on mkdir.
	tmpPath := h.uploadTmpPath(u)
	if err := os.MkdirAll(filepath.Dir(tmpPath), 0o750); err != nil {
		writeOCIErr(w, http.StatusInternalServerError, ErrCodeUnknown, err)
		return
	}
	if f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o640); err == nil {
		_ = f.Close()
	}

	w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/uploads/%s", rr.fullPath, u))
	w.Header().Set("Range", "0-0")
	w.Header().Set("Docker-Upload-UUID", u)
	w.Header().Set("Content-Length", "0")
	w.WriteHeader(http.StatusAccepted)
}

// blobUploadPatch appends a chunk to the tmp upload file.
// Response: 202 Accepted, Location, Range: 0-<bytes-1>.
func (h *Handler) blobUploadPatch(w http.ResponseWriter, r *http.Request) {
	rr := h.resolveRepo(w, r)
	if rr == nil {
		return
	}
	if !h.requireWriter(w, r, rr.repo) {
		return
	}
	u := chi.URLParam(r, "uuid")
	if !isUploadUUID(u) {
		writeOCIErr(w, http.StatusBadRequest, ErrCodeBlobUploadInvalid,
			errors.New("malformed upload uuid"))
		return
	}
	sess, err := h.sess.Lookup(r.Context(), u)
	if err != nil {
		writeOCIErr(w, http.StatusInternalServerError, ErrCodeUnknown, err)
		return
	}
	if sess == nil || sess.RepoID != rr.repo.ID {
		writeOCIErr(w, http.StatusNotFound, ErrCodeBlobUnknown, errors.New("upload session not found"))
		return
	}

	// Enforce per-chunk cap; io.LimitReader+1 pattern detects overflow.
	// Docker clients send chunks without Content-Length when
	// Transfer-Encoding: chunked, so we can't always pre-check by header.
	cap1 := h.chunkMaxBytes + 1
	n, err := appendChunk(h.uploadTmpPath(u), io.LimitReader(r.Body, cap1))
	if err != nil {
		writeAppendChunkError(w, err)
		return
	}
	if n > h.chunkMaxBytes {
		// Truncate back the overflow byte(s) we just wrote so we leave
		// the session in a clean state, then 413.
		_ = truncateFile(h.uploadTmpPath(u), sess.BytesSoFar)
		writeOCIErr(w, http.StatusRequestEntityTooLarge, ErrCodeSizeInvalid,
			fmt.Errorf("chunk exceeds %d bytes", h.chunkMaxBytes))
		return
	}

	// Per-session cap: bytes_so_far + n.
	if sess.BytesSoFar+n > h.sessionMaxBytes {
		_ = truncateFile(h.uploadTmpPath(u), sess.BytesSoFar)
		writeOCIErr(w, http.StatusRequestEntityTooLarge, ErrCodeSizeInvalid,
			fmt.Errorf("session exceeds %d bytes", h.sessionMaxBytes))
		return
	}

	if err := h.db.WriteTx(r.Context(), func(tx *sql.Tx) error {
		return h.sess.AppendBytes(r.Context(), tx, u, n)
	}); err != nil {
		writeOCIErr(w, http.StatusInternalServerError, ErrCodeUnknown, err)
		return
	}

	w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/uploads/%s", rr.fullPath, u))
	w.Header().Set("Range", fmt.Sprintf("0-%d", sess.BytesSoFar+n-1))
	w.Header().Set("Docker-Upload-UUID", u)
	w.Header().Set("Content-Length", "0")
	w.WriteHeader(http.StatusAccepted)
}

// blobUploadPut finalizes a chunked upload: optionally accepts a final
// chunk body, validates the claimed ?digest= against the full tmp file,
// inserts blob_uploads(digest) BEFORE cas rename, promotes tmp→CAS, and
// commits docker_blobs + session cleanup in one tx.
//
// The route is registered under the httpx.MirrorGuard middleware in
// Handler.Mount, so mirror-flagged repos reject upload attempts with 403
// repo.repo_is_mirror before this handler runs.
func (h *Handler) blobUploadPut(w http.ResponseWriter, r *http.Request) {
	rr := h.resolveRepo(w, r)
	if rr == nil {
		return
	}
	if !h.requireWriter(w, r, rr.repo) {
		return
	}
	u := chi.URLParam(r, "uuid")
	if !isUploadUUID(u) {
		writeOCIErr(w, http.StatusBadRequest, ErrCodeBlobUploadInvalid,
			errors.New("malformed upload uuid"))
		return
	}
	sess, err := h.sess.Lookup(r.Context(), u)
	if err != nil {
		writeOCIErr(w, http.StatusInternalServerError, ErrCodeUnknown, err)
		return
	}
	if sess == nil || sess.RepoID != rr.repo.ID {
		writeOCIErr(w, http.StatusNotFound, ErrCodeBlobUnknown, errors.New("upload session not found"))
		return
	}

	claim := r.URL.Query().Get("digest")
	if !validDigest(claim) {
		writeOCIErr(w, http.StatusBadRequest, ErrCodeDigestInvalid,
			fmt.Errorf("malformed digest %q", claim))
		return
	}

	// Append the final chunk (may be empty).
	tmpPath := h.uploadTmpPath(u)
	if r.ContentLength > h.chunkMaxBytes {
		writeOCIErr(w, http.StatusRequestEntityTooLarge, ErrCodeSizeInvalid,
			fmt.Errorf("final chunk exceeds %d bytes", h.chunkMaxBytes))
		return
	}
	n, err := appendChunk(tmpPath, io.LimitReader(r.Body, h.chunkMaxBytes+1))
	if err != nil {
		writeAppendChunkError(w, err)
		return
	}
	if n > h.chunkMaxBytes {
		_ = truncateFile(tmpPath, sess.BytesSoFar)
		writeOCIErr(w, http.StatusRequestEntityTooLarge, ErrCodeSizeInvalid,
			fmt.Errorf("final chunk exceeds %d bytes", h.chunkMaxBytes))
		return
	}

	// Hash the full tmp file to verify digest.
	actual, size, err := sha256OfFile(tmpPath)
	if err != nil {
		writeOCIErr(w, http.StatusInternalServerError, ErrCodeUnknown, err)
		return
	}
	if actual != claim {
		writeOCIErr(w, http.StatusBadRequest, ErrCodeDigestInvalid,
			fmt.Errorf("claimed=%s actual=%s", claim, actual))
		return
	}

	// Race close: register digest BEFORE cas rename so GC can
	// never delete a freshly-promoted blob.
	if h.blobUploads != nil {
		if err := h.blobUploads.Start(r.Context(), actual, time.Hour); err != nil {
			writeOCIErr(w, http.StatusInternalServerError, ErrCodeUnknown, err)
			return
		}
	}

	// Promote tmp → CAS via atomic rename.
	// CAS.PutFromPath leaves the source on error; remove the tmp file
	// ourselves on failure so repeated errors don't fill the volume.
	promoted, _, err := h.cas.PutFromPath(r.Context(), tmpPath)
	if err != nil {
		_ = os.Remove(tmpPath)
		writeOCIErr(w, http.StatusInternalServerError, ErrCodeUnknown, err)
		return
	}
	if promoted != actual {
		writeOCIErr(w, http.StatusInternalServerError, ErrCodeUnknown,
			fmt.Errorf("cas promoted=%s actual=%s", promoted, actual))
		return
	}

	// One writer tx: docker_blobs row + session cleanup.
	if err := h.db.WriteTx(r.Context(), func(tx *sql.Tx) error {
		if err := h.blobs.UpsertZeroRef(r.Context(), tx, actual, size); err != nil {
			return err
		}
		if err := h.blobs.Touch(r.Context(), tx, actual); err != nil {
			return err
		}
		return h.sess.Delete(r.Context(), tx, u)
	}); err != nil {
		writeOCIErr(w, http.StatusInternalServerError, ErrCodeUnknown, err)
		return
	}

	h.emitAudit(r, audit.EvtOCIBlobUploaded, actual, map[string]any{
		"repo": rr.fullPath,
		"size": size,
	})

	w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/%s", rr.fullPath, actual))
	w.Header().Set("Docker-Content-Digest", actual)
	w.Header().Set("Content-Length", "0")
	w.WriteHeader(http.StatusCreated)
}

// blobMonolithicPost implements POST /v2/<name>/blobs/uploads/?digest=...
// per OCI §4.2.1 — a single-request upload. Equivalent to POST+PUT rolled
// into one.
func (h *Handler) blobMonolithicPost(w http.ResponseWriter, r *http.Request) {
	rr := h.resolveRepo(w, r)
	if rr == nil {
		return
	}
	if !h.requireWriter(w, r, rr.repo) {
		return
	}

	claim := r.URL.Query().Get("digest")
	if !validDigest(claim) {
		writeOCIErr(w, http.StatusBadRequest, ErrCodeDigestInvalid,
			fmt.Errorf("malformed digest %q", claim))
		return
	}

	// Start an ephemeral session so all the PATCH/PUT code paths can be
	// shared. We don't actually write the DB row — the monolithic path
	// never observes a PATCH — and the tmp file goes under a fresh UUID.
	u := uuid.NewString()
	tmpPath := h.uploadTmpPath(u)
	if err := os.MkdirAll(filepath.Dir(tmpPath), 0o750); err != nil {
		writeOCIErr(w, http.StatusInternalServerError, ErrCodeUnknown, err)
		return
	}

	// Stream to disk with per-session cap.
	n, err := appendChunk(tmpPath, io.LimitReader(r.Body, h.sessionMaxBytes+1))
	if err != nil {
		_ = os.Remove(tmpPath)
		writeAppendChunkError(w, err)
		return
	}
	if n > h.sessionMaxBytes {
		_ = os.Remove(tmpPath)
		writeOCIErr(w, http.StatusRequestEntityTooLarge, ErrCodeSizeInvalid,
			fmt.Errorf("upload exceeds %d bytes", h.sessionMaxBytes))
		return
	}

	actual, size, err := sha256OfFile(tmpPath)
	if err != nil {
		_ = os.Remove(tmpPath)
		writeOCIErr(w, http.StatusInternalServerError, ErrCodeUnknown, err)
		return
	}
	if actual != claim {
		_ = os.Remove(tmpPath)
		writeOCIErr(w, http.StatusBadRequest, ErrCodeDigestInvalid,
			fmt.Errorf("claimed=%s actual=%s", claim, actual))
		return
	}

	if h.blobUploads != nil {
		if err := h.blobUploads.Start(r.Context(), actual, time.Hour); err != nil {
			_ = os.Remove(tmpPath)
			writeOCIErr(w, http.StatusInternalServerError, ErrCodeUnknown, err)
			return
		}
	}

	// Clean up tmp on CAS put failure (same pattern as chunked path).
	promoted, _, err := h.cas.PutFromPath(r.Context(), tmpPath)
	if err != nil {
		_ = os.Remove(tmpPath)
		writeOCIErr(w, http.StatusInternalServerError, ErrCodeUnknown, err)
		return
	}
	if promoted != actual {
		writeOCIErr(w, http.StatusInternalServerError, ErrCodeUnknown,
			fmt.Errorf("cas promoted=%s actual=%s", promoted, actual))
		return
	}

	if err := h.db.WriteTx(r.Context(), func(tx *sql.Tx) error {
		if err := h.blobs.UpsertZeroRef(r.Context(), tx, actual, size); err != nil {
			return err
		}
		return h.blobs.Touch(r.Context(), tx, actual)
	}); err != nil {
		writeOCIErr(w, http.StatusInternalServerError, ErrCodeUnknown, err)
		return
	}

	h.emitAudit(r, audit.EvtOCIBlobUploaded, actual, map[string]any{
		"repo":       rr.fullPath,
		"size":       size,
		"monolithic": true,
	})

	w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/%s", rr.fullPath, actual))
	w.Header().Set("Docker-Content-Digest", actual)
	w.Header().Set("Content-Length", "0")
	w.WriteHeader(http.StatusCreated)
}

// blobUploadStatus returns the Range header of an in-progress session.
// Response: 204 No Content, Range: 0-<bytes-1>.
func (h *Handler) blobUploadStatus(w http.ResponseWriter, r *http.Request) {
	rr := h.resolveRepo(w, r)
	if rr == nil {
		return
	}
	if !h.requireWriter(w, r, rr.repo) {
		return
	}
	u := chi.URLParam(r, "uuid")
	if !isUploadUUID(u) {
		writeOCIErr(w, http.StatusBadRequest, ErrCodeBlobUploadInvalid,
			errors.New("malformed upload uuid"))
		return
	}
	sess, err := h.sess.Lookup(r.Context(), u)
	if err != nil {
		writeOCIErr(w, http.StatusInternalServerError, ErrCodeUnknown, err)
		return
	}
	if sess == nil || sess.RepoID != rr.repo.ID {
		writeOCIErr(w, http.StatusNotFound, ErrCodeBlobUnknown, errors.New("upload session not found"))
		return
	}
	w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/uploads/%s", rr.fullPath, u))
	if sess.BytesSoFar == 0 {
		w.Header().Set("Range", "0-0")
	} else {
		w.Header().Set("Range", fmt.Sprintf("0-%d", sess.BytesSoFar-1))
	}
	w.Header().Set("Docker-Upload-UUID", u)
	w.WriteHeader(http.StatusNoContent)
}

// blobGet serves the blob contents via http.ServeContent (range support).
// Sets Docker-Content-Digest.
func (h *Handler) blobGet(w http.ResponseWriter, r *http.Request) {
	rr := h.resolveRepo(w, r)
	if rr == nil {
		return
	}
	if !h.requireReader(w, r, rr.repo) {
		return
	}
	digest := chi.URLParam(r, "digest")
	if !validDigest(digest) {
		writeOCIErr(w, http.StatusBadRequest, ErrCodeDigestInvalid,
			fmt.Errorf("malformed digest %q", digest))
		return
	}
	// http.ServeContent wants io.ReadSeeker; open the CAS file directly.
	path, err := h.casFilePath(digest)
	if err != nil {
		writeOCIErr(w, http.StatusBadRequest, ErrCodeDigestInvalid, err)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// Never echo the CAS filesystem path back to the client —
			// os.PathError.Error() would embed
			// /var/lib/omnirepo/blobs/sha256/<aa>/<digest>. Emit the
			// canonical OCI "blob unknown" instead.
			writeOCIErr(w, http.StatusNotFound, ErrCodeBlobUnknown, errors.New("blob unknown"))
			return
		}
		// Swallow the raw path-bearing error into the (logged)
		// X-Incident-Id path — the client sees only the generic code.
		slog.ErrorContext(r.Context(), "oci.blob_open_failed", "err", err, "digest", digest)
		writeOCIErr(w, http.StatusInternalServerError, ErrCodeUnknown, errors.New("internal error"))
		return
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		writeOCIErr(w, http.StatusInternalServerError, ErrCodeUnknown, err)
		return
	}
	w.Header().Set("Docker-Content-Digest", digest)
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeContent(w, r, "", fi.ModTime(), f)
}

// blobHead returns 200 with Content-Length + Docker-Content-Digest if blob
// exists, 404 otherwise.
func (h *Handler) blobHead(w http.ResponseWriter, r *http.Request) {
	rr := h.resolveRepo(w, r)
	if rr == nil {
		return
	}
	if !h.requireReader(w, r, rr.repo) {
		return
	}
	digest := chi.URLParam(r, "digest")
	if !validDigest(digest) {
		writeOCIErr(w, http.StatusBadRequest, ErrCodeDigestInvalid,
			fmt.Errorf("malformed digest %q", digest))
		return
	}
	size, exists, err := h.cas.Stat(r.Context(), digest)
	if err != nil {
		writeOCIErr(w, http.StatusInternalServerError, ErrCodeUnknown, err)
		return
	}
	if !exists {
		writeOCIErr(w, http.StatusNotFound, ErrCodeBlobUnknown, errors.New("blob unknown"))
		return
	}
	w.Header().Set("Docker-Content-Digest", digest)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", size))
	w.WriteHeader(http.StatusOK)
}

// blobDelete is only allowed at ref_count==0 across all repos. Non-zero
// → 405 MethodNotAllowed. CAS file is NOT removed here (live-data invariant:
// only GC deletes CAS bytes — see Pitfall 8 in repos.WipeDocker). Deleting
// the docker_blobs row at ref_count==0 hands it off to the next GC sweep.
func (h *Handler) blobDelete(w http.ResponseWriter, r *http.Request) {
	rr := h.resolveRepo(w, r)
	if rr == nil {
		return
	}
	// Write-intent required; gating via ActionUpdateRepo (admin of the
	// project containing the repo). Blob DELETE is a rare operation and
	// the OCI spec is silent on whether it requires more than normal
	// push auth — the must_haves mandate ref_count==0 which is the
	// actual safety property, so reuse the writer check here.
	if !h.requireWriter(w, r, rr.repo) {
		return
	}
	digest := chi.URLParam(r, "digest")
	if !validDigest(digest) {
		writeOCIErr(w, http.StatusBadRequest, ErrCodeDigestInvalid,
			fmt.Errorf("malformed digest %q", digest))
		return
	}

	b, err := h.blobs.Stat(r.Context(), digest)
	if err != nil {
		writeOCIErr(w, http.StatusInternalServerError, ErrCodeUnknown, err)
		return
	}
	if b == nil {
		writeOCIErr(w, http.StatusNotFound, ErrCodeBlobUnknown, errors.New("blob unknown"))
		return
	}
	if b.RefCount > 0 {
		// OCI Distribution conventionally uses 405 for "deletion is only
		// via GC when refs exist". Spec §errors supports MANIFEST_UNKNOWN
		// but for our blob-ref semantic we emit UNSUPPORTED so clients
		// don't retry.
		w.Header().Set("Allow", "GET, HEAD")
		writeOCIErr(w, http.StatusMethodNotAllowed, ErrCodeUnsupported,
			fmt.Errorf("blob has %d references; delete via GC", b.RefCount))
		return
	}

	if err := h.db.WriteTx(r.Context(), func(tx *sql.Tx) error {
		return h.blobs.Delete(r.Context(), tx, digest)
	}); err != nil {
		writeOCIErr(w, http.StatusInternalServerError, ErrCodeUnknown, err)
		return
	}

	h.emitAudit(r, audit.EvtOCIBlobDeleted, digest, map[string]any{
		"repo": rr.fullPath,
	})

	w.WriteHeader(http.StatusAccepted)
}

// emitAudit records a best-effort audit event with the request's actor.
// Called AFTER the writer tx commits so a transient audit failure never
// masks a successful state change.
func (h *Handler) emitAudit(r *http.Request, kind audit.EventKind, targetID string, details map[string]any) {
	if h.auditLogger == nil {
		return
	}
	e := audit.Event{
		Kind:       kind,
		IP:         r.RemoteAddr,
		UserAgent:  r.Header.Get("User-Agent"),
		TargetKind: "blob",
		TargetID:   targetID,
		Outcome:    "ok",
		Details:    details,
		OccurredAt: time.Now().UTC(),
	}
	if a, ok := auth.ActorFromContext(r.Context()); ok {
		switch a.Kind {
		case auth.ActorKindUser:
			id := a.ID
			e.ActorUserID = &id
		case auth.ActorKindAPIKey:
			id := a.APIKeyID
			e.ActorAPIKeyID = &id
		}
	}
	_ = h.auditLogger.Record(r.Context(), e)
}

// casFilePath mirrors the internal CAS layout so the GET handler can hand
// a *os.File to http.ServeContent. CAS.Get returns io.ReadCloser (not
// io.ReadSeeker) which ServeContent cannot range over.
func (h *Handler) casFilePath(digest string) (string, error) {
	if !validDigest(digest) {
		return "", fmt.Errorf("bad digest %q", digest)
	}
	hx := strings.TrimPrefix(digest, "sha256:")
	// CAS root is handled via h.cas; we don't own the path. Probe Stat
	// to validate the blob exists and ask the CAS where it lives by
	// reading it — but ServeContent needs the File. We accept the
	// coupling and rebuild the layout here, mirroring storage.cas.blobPath.
	return filepath.Join(h.dataRoot, "blobs", "sha256", hx[:2], hx), nil
}

// appendChunk opens path with O_APPEND|O_WRONLY and copies r into it.
// Returns the number of bytes written. Does NOT buffer the body in memory.
// path must exist (created by blobUploadPost).
func appendChunk(path string, r io.Reader) (int64, error) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o640)
	if err != nil {
		return 0, fmt.Errorf("open append %s: %w", path, err)
	}
	n, copyErr := io.Copy(f, r)
	syncErr := f.Sync()
	closeErr := f.Close()
	if copyErr != nil {
		return n, fmt.Errorf("append %s: %w", path, copyErr)
	}
	if syncErr != nil {
		return n, fmt.Errorf("fsync %s: %w", path, syncErr)
	}
	if closeErr != nil {
		return n, fmt.Errorf("close %s: %w", path, closeErr)
	}
	return n, nil
}

// truncateFile shrinks path to size bytes. Best-effort; swallows ENOENT.
func truncateFile(path string, size int64) error {
	if err := os.Truncate(path, size); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// writeAppendChunkError maps an appendChunk error to the right OCI response.
// ENOSPC (disk full) must surface as 507 INSUFFICIENT STORAGE so
// docker/crane back off instead of aggressively retrying and making things
// worse. All other errors become 500 UNKNOWN.
func writeAppendChunkError(w http.ResponseWriter, err error) {
	if errors.Is(err, syscall.ENOSPC) {
		writeOCIErr(w, http.StatusInsufficientStorage, ErrCodeSizeInvalid, err)
		return
	}
	writeOCIErr(w, http.StatusInternalServerError, ErrCodeUnknown, err)
}

// sha256OfFile reads path and returns its sha256 digest (`sha256:<hex>`)
// plus byte count. Callers should treat missing files as fs.ErrNotExist.
func sha256OfFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), n, nil
}

// validDigest returns true for a canonical sha256:<64-hex> digest string.
func validDigest(d string) bool {
	if !strings.HasPrefix(d, "sha256:") {
		return false
	}
	hx := d[len("sha256:"):]
	if len(hx) != sha256HexLen {
		return false
	}
	for i := 0; i < len(hx); i++ {
		c := hx[i]
		switch {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'f':
		default:
			return false
		}
	}
	return true
}

// Compile-time guard: handler.go wires blobPostDispatch / blobUploadPatch /
// blobUploadPut / blobUploadStatus / blobGet / blobHead / blobDelete into
// Mount. Those are the handler methods defined above.
var _ = json.Marshal // keep encoding/json import for future use if needed
