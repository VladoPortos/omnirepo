// Package oci — pull-external orchestration (Phase 02-10, D-04, D-12, D-13).
//
// Two surfaces:
//
//  1. REST enqueue: POST /api/v1/projects/{name}/repos/docker/{repo}/pull-external.
//     Validates request, resolves + host-matches any supplied cred_id,
//     inserts a pending sync_jobs row with kind="pull_external", emits
//     oci.pull_external.started + upstream_cred.used audit events, and
//     returns 202 with the job id. The sync pool picks it up and runs
//     PullExternalHandler.
//
//  2. Job handler: PullExternalHandler.Handle. Decodes the payload, builds
//     the remote.Option list (anonymous OR Basic auth from cred / inline),
//     fetches manifest+config+layers via go-containerregistry/pkg/v1/remote,
//     streams every blob into CAS, then runs a single writer tx that
//     inserts the manifest row + refs + FTS + optional auto-scan via the
//     shared writeManifestWithRefcounts helper from manifests.go (02-07).
//
// Manifest bodies are stored byte-for-byte via RawManifest (Pitfall 5).
// Error strings are scrubbed of "Authorization:" substrings before landing
// in sync_jobs.last_error (T-02-10-02 via sanitizeUpstreamErr).
package oci

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/dxc-internal/omnirepo/internal/audit"
	"github.com/dxc-internal/omnirepo/internal/auth"
	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/storage"
)

// PullExternalJobKind is the sync_jobs.kind value that routes a job row to
// PullExternalHandler.Handle. Registered in app.Run via syncHandlers.
const PullExternalJobKind = "pull_external"

// DefaultPullExternalTimeout bounds a single pull-external job. Applied by
// the handler via context.WithTimeout. 30 minutes matches the longest
// realistic multi-GiB image pull over a slow link. Can be overridden via
// handler field at construction.
const DefaultPullExternalTimeout = 30 * time.Minute

// maxPullExternalRequestBodyBytes caps the REST enqueue JSON body. 64 KiB
// is generous for the field set below.
const maxPullExternalRequestBodyBytes = 64 * 1024

// PullExternalRequest is the REST POST body shape.
type PullExternalRequest struct {
	SrcImage   string `json:"src_image"`
	DstTag     string `json:"dst_tag,omitempty"`
	CredID     int64  `json:"cred_id,omitempty"`
	SrcUser    string `json:"src_username,omitempty"`
	SrcPass    string `json:"src_password,omitempty"`
}

// PullExternalJob is the payload_json shape persisted in sync_jobs. For v1
// simplicity the inline secret (when provided) is stored in cleartext and
// the row is hard-deleted after the job completes (pool MarkDone flips
// status='done'; operators can add a retention sweep later).
type PullExternalJob struct {
	SrcImage   string `json:"src_image"`
	DstTag     string `json:"dst_tag,omitempty"`
	CredID     int64  `json:"cred_id,omitempty"`
	InlineUser string `json:"inline_user,omitempty"`
	InlinePass string `json:"inline_pass,omitempty"`
}

// PullExternalDeps bundles what PullExternalHandler needs. Built in
// app.Run and wired onto the sync pool's handler map.
type PullExternalDeps struct {
	DB        *metadata.DB
	CAS       storage.CAS
	Blobs     *metadata.DockerBlobsRepo
	Manifests *metadata.DockerManifestsRepo
	Tags      *metadata.DockerTagsRepo
	Scans     *metadata.ScansRepo
	ScanKick  func()
	Repos     *metadata.ReposRepo
	Projects  *metadata.ProjectsRepo
	Creds     *metadata.UpstreamCredsRepo
	Audit     audit.Logger
	// Handler is the /v2 handler whose writeManifestWithRefcounts helper
	// the job reuses. The job needs the full Handler (not just the
	// manifests repo) so the refcount+FTS+auto-scan logic stays in one
	// place — identical to the OCI /v2 PUT path.
	OCI *Handler
	// Timeout bounds a single job. Zero → DefaultPullExternalTimeout.
	Timeout time.Duration
}

// PullExternalHandler is the sync-pool job handler for kind="pull_external".
type PullExternalHandler struct {
	deps PullExternalDeps
}

// NewPullExternalHandler returns a handler bound to deps.
func NewPullExternalHandler(deps PullExternalDeps) *PullExternalHandler {
	return &PullExternalHandler{deps: deps}
}

// SanitizeUpstreamErrExported exposes sanitizeUpstreamErr for external tests
// that cannot reach into the unexported surface.
func SanitizeUpstreamErrExported(err error) error { return sanitizeUpstreamErr(err) }

// authRegex scrubs Authorization header bytes out of error strings.
// Matches "Authorization: <anything-to-EOL-or-newline>".
var authRegex = regexp.MustCompile(`(?i)Authorization:\s*[^\r\n"']*`)

// sanitizeUpstreamErr strips Authorization: headers from err.Error(). Returns
// a plain errors.New with the scrubbed text so the original error's wrap
// chain is deliberately dropped — upstream libraries sometimes retain the
// credential bytes inside nested %w chains.
func sanitizeUpstreamErr(err error) error {
	if err == nil {
		return nil
	}
	scrubbed := authRegex.ReplaceAllString(err.Error(), "Authorization: REDACTED")
	return errors.New(scrubbed)
}

// Handle implements jobs.Handler for kind="pull_external".
func (p *PullExternalHandler) Handle(ctx context.Context, payload string, projectID, repoID int64) error {
	timeout := p.deps.Timeout
	if timeout <= 0 {
		timeout = DefaultPullExternalTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var job PullExternalJob
	if err := json.Unmarshal([]byte(payload), &job); err != nil {
		return fmt.Errorf("pull_external: payload: %w", err)
	}
	srcRef, err := name.ParseReference(job.SrcImage)
	if err != nil {
		return fmt.Errorf("pull_external: parse ref %q: %w", job.SrcImage, err)
	}

	// Resolve dst repo early so we can fail fast if it vanished between
	// enqueue and lease.
	dstRepo, err := p.deps.Repos.FindByID(ctx, repoID)
	if err != nil {
		return fmt.Errorf("pull_external: resolve dst repo %d: %w", repoID, err)
	}
	if dstRepo == nil {
		return fmt.Errorf("pull_external: dst repo %d not found", repoID)
	}
	dstProject, err := p.deps.Projects.FindByID(ctx, dstRepo.ProjectID)
	if err != nil {
		return fmt.Errorf("pull_external: resolve dst project %d: %w", dstRepo.ProjectID, err)
	}
	repoPath := ""
	if dstProject != nil {
		repoPath = dstProject.Name + "/" + dstRepo.Type + "/" + dstRepo.Name
	}

	// Build remote options.
	opts := []remote.Option{remote.WithContext(ctx)}
	var usedCredID int64
	switch {
	case job.CredID != 0:
		user, pw, tok, host, lerr := p.deps.Creds.Lookup(ctx, projectID, job.CredID)
		if lerr != nil {
			return fmt.Errorf("pull_external: cred lookup: %w", lerr)
		}
		if host != srcRef.Context().RegistryStr() {
			return fmt.Errorf("cred_host_mismatch: cred=%s src=%s",
				host, srcRef.Context().RegistryStr())
		}
		// Prefer password auth when present; fall back to token as Bearer.
		if pw != "" {
			opts = append(opts, remote.WithAuth(&authn.Basic{Username: user, Password: pw}))
		} else if tok != "" {
			opts = append(opts, remote.WithAuth(&authn.Bearer{Token: tok}))
		}
		usedCredID = job.CredID
	case job.InlineUser != "" || job.InlinePass != "":
		opts = append(opts, remote.WithAuth(&authn.Basic{
			Username: job.InlineUser,
			Password: job.InlinePass,
		}))
	}

	// Emit upstream_cred.used here (not at enqueue time): the job might be
	// retried; the most useful audit signal is actual upstream contact.
	if usedCredID != 0 && p.deps.Audit != nil {
		_ = p.deps.Audit.Record(ctx, audit.Event{
			Kind:       audit.EvtUpstreamCredUsed,
			TargetKind: "upstream_cred",
			TargetID:   strconv.FormatInt(usedCredID, 10),
			Details: map[string]any{
				"cred_id": usedCredID,
				"repo":    repoPath,
				"src":     job.SrcImage,
			},
		})
	}

	// Determine dst tag: explicit DstTag wins; otherwise use source tag; if
	// source ref is a digest-only reference, use the digest as the tag
	// (callers should normally supply DstTag in that case).
	dstTag := job.DstTag
	if dstTag == "" {
		if t, ok := srcRef.(name.Tag); ok {
			dstTag = t.TagStr()
		} else if d, ok := srcRef.(name.Digest); ok {
			dstTag = d.DigestStr()
		}
	}

	// Try image first; if it's actually an index, fall back to remote.Index.
	desc, err := remote.Get(srcRef, opts...)
	if err != nil {
		return sanitizeUpstreamErr(fmt.Errorf("pull_external: remote.Get: %w", err))
	}

	if desc.MediaType.IsIndex() {
		return p.handleIndex(ctx, desc, srcRef, opts, dstRepo, repoPath, dstTag)
	}
	return p.handleImage(ctx, desc, srcRef, opts, dstRepo, repoPath, dstTag)
}

// handleImage imports a single-image manifest.
func (p *PullExternalHandler) handleImage(
	ctx context.Context,
	desc *remote.Descriptor,
	srcRef name.Reference,
	opts []remote.Option,
	dstRepo *metadata.Repo,
	repoPath string,
	dstTag string,
) error {
	img, err := desc.Image()
	if err != nil {
		return sanitizeUpstreamErr(fmt.Errorf("pull_external: image: %w", err))
	}
	if err := p.streamImageBlobs(ctx, img); err != nil {
		return err
	}
	raw, err := img.RawManifest()
	if err != nil {
		return sanitizeUpstreamErr(fmt.Errorf("pull_external: raw manifest: %w", err))
	}
	mt, err := img.MediaType()
	if err != nil {
		return sanitizeUpstreamErr(fmt.Errorf("pull_external: media type: %w", err))
	}
	return p.commitManifest(ctx, dstRepo, repoPath, dstTag, raw, string(mt), srcRef, false)
}

// handleIndex imports a manifest list / image index, walking every child
// manifest and recursively writing them into the dst repo.
func (p *PullExternalHandler) handleIndex(
	ctx context.Context,
	desc *remote.Descriptor,
	srcRef name.Reference,
	opts []remote.Option,
	dstRepo *metadata.Repo,
	repoPath string,
	dstTag string,
) error {
	idx, err := desc.ImageIndex()
	if err != nil {
		return sanitizeUpstreamErr(fmt.Errorf("pull_external: index: %w", err))
	}
	mf, err := idx.IndexManifest()
	if err != nil {
		return sanitizeUpstreamErr(fmt.Errorf("pull_external: index manifest: %w", err))
	}
	// Pull every child image and commit its manifest BEFORE the index,
	// because the index commit requires each child digest to already exist
	// in docker_manifests (incRefs for indexes uses manifests.IncRef).
	for _, childDesc := range mf.Manifests {
		childImg, err := idx.Image(childDesc.Digest)
		if err != nil {
			return sanitizeUpstreamErr(fmt.Errorf("pull_external: child image %s: %w",
				childDesc.Digest, err))
		}
		if err := p.streamImageBlobs(ctx, childImg); err != nil {
			return err
		}
		childRaw, err := childImg.RawManifest()
		if err != nil {
			return sanitizeUpstreamErr(fmt.Errorf("pull_external: child raw: %w", err))
		}
		childMT, err := childImg.MediaType()
		if err != nil {
			return sanitizeUpstreamErr(fmt.Errorf("pull_external: child mt: %w", err))
		}
		// Child manifests go in WITHOUT a tag reference — only the index
		// carries the user-visible tag. Pass reference="" so the helper
		// skips the tag upsert for the child.
		if err := p.commitManifest(ctx, dstRepo, repoPath, "", childRaw, string(childMT), srcRef, true); err != nil {
			return err
		}
	}
	// Now the index body itself.
	raw, err := idx.RawManifest()
	if err != nil {
		return sanitizeUpstreamErr(fmt.Errorf("pull_external: index raw: %w", err))
	}
	mt, err := idx.MediaType()
	if err != nil {
		return sanitizeUpstreamErr(fmt.Errorf("pull_external: index mt: %w", err))
	}
	return p.commitManifest(ctx, dstRepo, repoPath, dstTag, raw, string(mt), srcRef, false)
}

// streamImageBlobs downloads every layer + the config blob into CAS. The
// CAS promotes each blob atomically by its sha256 so concurrent pulls
// don't fight over the same on-disk file.
func (p *PullExternalHandler) streamImageBlobs(ctx context.Context, img v1.Image) error {
	layers, err := img.Layers()
	if err != nil {
		return sanitizeUpstreamErr(fmt.Errorf("pull_external: layers: %w", err))
	}
	for _, l := range layers {
		if err := p.streamLayer(ctx, l); err != nil {
			return err
		}
	}
	cfg, err := img.RawConfigFile()
	if err != nil {
		return sanitizeUpstreamErr(fmt.Errorf("pull_external: raw config: %w", err))
	}
	if _, _, err := p.deps.CAS.Put(ctx, bytes.NewReader(cfg)); err != nil {
		return fmt.Errorf("pull_external: cas put config: %w", err)
	}
	return nil
}

func (p *PullExternalHandler) streamLayer(ctx context.Context, l v1.Layer) error {
	rc, err := l.Compressed()
	if err != nil {
		return sanitizeUpstreamErr(fmt.Errorf("pull_external: layer compressed: %w", err))
	}
	defer func() { _ = rc.Close() }()
	if _, _, err := p.deps.CAS.Put(ctx, rc); err != nil {
		return fmt.Errorf("pull_external: cas put layer: %w", err)
	}
	// Drain any remaining bytes (go-containerregistry sometimes leaves
	// unread tail when we close early; harmless here but keep the stream
	// symmetric).
	_, _ = io.Copy(io.Discard, rc)
	return nil
}

// commitManifest runs one writer tx wrapping the shared helper. When
// isChild is true, it also calls UpsertZeroRef for every referenced blob
// BEFORE the helper runs — child manifests arrive with their blob rows not
// yet in docker_blobs (the blob exists in CAS but isn't tracked for
// refcounts). The helper's incRefs will bump them.
func (p *PullExternalHandler) commitManifest(
	ctx context.Context,
	dstRepo *metadata.Repo,
	repoPath string,
	reference string,
	body []byte,
	mediaType string,
	srcRef name.Reference,
	isChild bool,
) error {
	// Validate body digest + parse refs OUTSIDE the writer tx.
	mfDigest := computeMfDigest(body)
	refs, isIndex, err := manifestRefs(body)
	if err != nil {
		return fmt.Errorf("pull_external: parse refs: %w", err)
	}

	var scanEnqueued bool
	txErr := p.deps.DB.WriteTx(ctx, func(tx *sql.Tx) error {
		// For non-index manifests we must register every layer+config blob
		// as a zero-ref docker_blobs row first (the CAS already has the
		// bytes). The helper's incRefs will bump each refcount.
		if !isIndex {
			for _, d := range refs {
				size, exists, serr := p.deps.CAS.Stat(ctx, d)
				if serr != nil {
					return fmt.Errorf("pull_external: cas stat %s: %w", d, serr)
				}
				if !exists {
					return fmt.Errorf("pull_external: blob %s absent from CAS post-stream", d)
				}
				if err := p.deps.Blobs.UpsertZeroRef(ctx, tx, d, size); err != nil {
					return err
				}
			}
		}
		enq, err := p.deps.OCI.writeManifestWithRefcounts(
			ctx, tx, dstRepo.ID, repoPath, "", reference, mfDigest, mediaType, body,
			refs, isIndex, dstRepo.AutoScan && !isChild,
		)
		if err != nil {
			return err
		}
		scanEnqueued = enq
		return nil
	})
	if txErr != nil {
		return txErr
	}
	if scanEnqueued && p.deps.ScanKick != nil {
		p.deps.ScanKick()
	}
	// Best-effort post-commit audit (only at the top-level manifest; child
	// manifests in an index don't emit their own "finished" event).
	if !isChild && p.deps.Audit != nil {
		_ = p.deps.Audit.Record(ctx, audit.Event{
			Kind:       audit.EvtOCIPullExternalFinished,
			TargetKind: "manifest",
			TargetID:   mfDigest,
			Details: map[string]any{
				"repo":      repoPath,
				"reference": reference,
				"src":       srcRef.String(),
				"media":     mediaType,
				"size":      len(body),
			},
		})
	}
	return nil
}

// -----------------------------------------------------------------------------
// REST enqueue handler
// -----------------------------------------------------------------------------

// PullExternalREST is the REST enqueue handler. Registered via
// api/oci_actions.go (out-of-tree from the /v2 router). It lives on the OCI
// Handler so it can reuse the same project/repo resolution + auth helpers.
type PullExternalREST struct {
	h       *Handler
	creds   *metadata.UpstreamCredsRepo
	jobs    *metadata.SyncJobsRepo
	kick    func()
}

// NewPullExternalREST constructs the REST handler. kick is the sync-pool
// Kick callback so newly-enqueued jobs dispatch immediately without waiting
// for the next poll.
func NewPullExternalREST(h *Handler, creds *metadata.UpstreamCredsRepo, jobs *metadata.SyncJobsRepo, kick func()) *PullExternalREST {
	return &PullExternalREST{h: h, creds: creds, jobs: jobs, kick: kick}
}

// Handle implements http.Handler for POST
// /api/v1/projects/{name}/repos/docker/{repo}/pull-external.
func (p *PullExternalREST) Handle(w http.ResponseWriter, r *http.Request) {
	actor, ok := auth.ActorFromContext(r.Context())
	if !ok || actor.Kind == auth.ActorKindAnonymous {
		writeActionErr(w, http.StatusUnauthorized, "unauthenticated", "")
		return
	}
	projectName := chi.URLParam(r, "name")
	repoName := chi.URLParam(r, "repo")
	if projectName == "" || repoName == "" {
		writeActionErr(w, http.StatusBadRequest, "validation_failed", "missing project or repo")
		return
	}
	proj, err := p.h.projects.FindByName(r.Context(), projectName)
	if err != nil || proj == nil {
		writeActionErr(w, http.StatusNotFound, "not_found", "project")
		return
	}
	rr, err := p.h.repos.FindByTriple(r.Context(), proj.ID, "docker", repoName)
	if err != nil || rr == nil {
		writeActionErr(w, http.StatusNotFound, "not_found", "repo")
		return
	}
	// Authorization: must be able to write to the repo.
	if ok, reason := p.h.canOnRepo(r.Context(), actor, auth.ActionUpdateRepo, rr); !ok {
		writeActionErr(w, http.StatusForbidden, "forbidden", reason)
		return
	}

	var req PullExternalRequest
	r.Body = http.MaxBytesReader(w, r.Body, maxPullExternalRequestBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeActionErr(w, http.StatusBadRequest, "validation_failed", "invalid JSON: "+err.Error())
		return
	}
	if req.SrcImage == "" {
		writeActionErr(w, http.StatusBadRequest, "validation_failed", "src_image required")
		return
	}
	srcRef, err := name.ParseReference(req.SrcImage)
	if err != nil {
		writeActionErr(w, http.StatusBadRequest, "validation_failed", "src_image: "+err.Error())
		return
	}

	// cred_host_mismatch check — reject at REST time so operator gets a
	// synchronous error rather than discovering it via a failed job.
	if req.CredID != 0 {
		if p.creds == nil {
			writeActionErr(w, http.StatusInternalServerError, "internal", "creds not wired")
			return
		}
		_, _, _, credHost, lerr := p.creds.Lookup(r.Context(), proj.ID, req.CredID)
		if lerr != nil {
			if errors.Is(lerr, metadata.ErrNotFound) || errors.Is(lerr, metadata.ErrForeignProject) {
				writeActionErr(w, http.StatusNotFound, "not_found", "cred")
				return
			}
			slog.ErrorContext(r.Context(), "oci.pull_external.creds.lookup",
				"project", proj.ID, "cred_id", req.CredID, "err", lerr)
			writeActionErr(w, http.StatusInternalServerError, "internal", "")
			return
		}
		if credHost != srcRef.Context().RegistryStr() {
			writeActionErr(w, http.StatusBadRequest, "cred_host_mismatch",
				fmt.Sprintf("cred host %q != src host %q", credHost, srcRef.Context().RegistryStr()))
			return
		}
	}

	// Build payload. Inline creds are carried in cleartext for v1 (the row
	// is deleted after the job completes). If the operator wants at-rest
	// encryption, they should use a stored cred_id instead.
	jobPayload := PullExternalJob{
		SrcImage:   req.SrcImage,
		DstTag:     req.DstTag,
		CredID:     req.CredID,
		InlineUser: req.SrcUser,
		InlinePass: req.SrcPass,
	}
	buf, err := json.Marshal(&jobPayload)
	if err != nil {
		slog.ErrorContext(r.Context(), "oci.pull_external.marshal", "err", err)
		writeActionErr(w, http.StatusInternalServerError, "internal", "marshal")
		return
	}

	var jobID int64
	err = p.h.db.WriteTx(r.Context(), func(tx *sql.Tx) error {
		id, err := p.jobs.Enqueue(r.Context(), tx, PullExternalJobKind, proj.ID, rr.ID, string(buf))
		if err != nil {
			return err
		}
		jobID = id
		return nil
	})
	if err != nil {
		// WR-03: server-side log captures the sql/path detail; client gets a
		// stable code with empty detail.
		slog.ErrorContext(r.Context(), "oci.pull_external.enqueue",
			"repo", rr.ID, "err", err)
		writeActionErr(w, http.StatusInternalServerError, "internal", "enqueue")
		return
	}

	// Kick the pool so the job dispatches immediately.
	if p.kick != nil {
		p.kick()
	}

	// Best-effort audit. started; used is emitted by the job when the
	// cred is actually consumed against upstream.
	if p.h.auditLogger != nil {
		ev := audit.Event{
			Kind:       audit.EvtOCIPullExternalStarted,
			TargetKind: "repo",
			TargetID:   projectName + "/docker/" + repoName,
			Details: map[string]any{
				"src":     req.SrcImage,
				"dst_tag": req.DstTag,
				"job_id":  jobID,
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

	writeActionOK(w, http.StatusAccepted, map[string]any{"job_id": jobID})
}

// writeActionErr writes a small application/json error envelope. Kept local
// so this file doesn't pull internal/api into the oci package. Named to
// avoid collision with cosign.go's writeJSONErr which uses a different
// signature.
func writeActionErr(w http.ResponseWriter, status int, code, detail string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": code, "detail": detail})
}

// writeActionOK writes an arbitrary JSON body at status.
func writeActionOK(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
