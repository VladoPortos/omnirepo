// Package oci — pull-external orchestration.
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
//     shared writeManifestWithRefcounts helper from manifests.go.
//
// Manifest bodies are stored byte-for-byte via RawManifest (Pitfall 5).
// Error strings are scrubbed of "Authorization:" substrings before landing
// in sync_jobs.last_error (via sanitizeUpstreamErr).
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
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/vladoportos/omnirepo/internal/audit"
	"github.com/vladoportos/omnirepo/internal/auth"
	"github.com/vladoportos/omnirepo/internal/httpx"
	"github.com/vladoportos/omnirepo/internal/jobs"
	"github.com/vladoportos/omnirepo/internal/metadata"
	"github.com/vladoportos/omnirepo/internal/storage"
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
	SrcImage string `json:"src_image"`
	DstTag   string `json:"dst_tag,omitempty"`
	CredID   int64  `json:"cred_id,omitempty"`
	SrcUser  string `json:"src_username,omitempty"`
	SrcPass  string `json:"src_password,omitempty"`
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
	DB       *metadata.DB
	CAS      storage.CAS
	Blobs    *metadata.DockerBlobsRepo
	ScanKick func()
	Repos    *metadata.ReposRepo
	Projects *metadata.ProjectsRepo
	Creds    *metadata.UpstreamCredsRepo
	Audit    audit.Logger
	// SyncJobs is the sync_jobs repo the handler uses to emit throttled
	// byte-level progress. Nil-safe: when unwired the handler still runs
	// but progress writes are silently skipped (ProgressWriter.Set
	// short-circuits on a nil repo).
	SyncJobs *metadata.SyncJobsRepo
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

// Handle implements jobs.Handler for kind="pull_external".
//
// Accepts jobID so the handler can emit throttled sync_jobs progress via
// ProgressWriter. The shim in internal/app/app.go passes j.ID; older unit
// tests that call Handle directly may pass 0 — the ProgressWriter is
// nil-safe in that case.
func (p *PullExternalHandler) Handle(ctx context.Context, payload string, projectID, repoID, jobID int64) (retErr error) {
	timeout := p.deps.Timeout
	if timeout <= 0 {
		timeout = DefaultPullExternalTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var job PullExternalJob

	// Terminal failure audit. The lifecycle is started (emitted at enqueue by
	// the HTTP handler) → finished (emitted on success by persistManifest);
	// without this a job that errors out leaves a "started" with no resolution
	// in the trail. Best-effort + nil-safe, and installed BEFORE the payload
	// unmarshal so even a malformed job is recorded. The failure REASON is
	// deliberately NOT included here: it lands upstream-sanitized in
	// sync_jobs.last_error (correlatable via sync_job_id), so no raw error or
	// credential-bearing string can reach the audit log. The write runs on a
	// detached, short-deadline context so a pull that failed because ctx was
	// cancelled still records, without the write itself being able to hang
	// handler return.
	defer func() {
		if retErr == nil || p.deps.Audit == nil {
			return
		}
		auditCtx, auditCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer auditCancel()
		_ = p.deps.Audit.Record(auditCtx, audit.Event{
			Kind:       audit.EvtOCIPullExternalFailed,
			TargetKind: "repo",
			TargetID:   strconv.FormatInt(repoID, 10),
			Details: map[string]any{
				"src":     job.SrcImage,
				"dst_tag": job.DstTag,
				"job_id":  jobID,
			},
		})
	}()

	if err := json.Unmarshal([]byte(payload), &job); err != nil {
		return fmt.Errorf("pull_external: payload: %w", err)
	}
	srcRef, err := name.ParseReference(job.SrcImage)
	if err != nil {
		return fmt.Errorf("pull_external: parse ref %q: %w", job.SrcImage, err)
	}

	// progress is shared across image+index flows. Construct once per job
	// so the throttle state persists across every layer of every child
	// manifest when an index is being imported.
	progress := jobs.NewProgressWriter(p.deps.SyncJobs, jobID)
	defer func() { _ = progress.Flush(ctx) }()

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
		return httpx.SanitizeUpstreamErr(fmt.Errorf("pull_external: remote.Get: %w", err))
	}

	if desc.MediaType.IsIndex() {
		return p.handleIndex(ctx, desc, srcRef, dstRepo, repoPath, dstTag, progress)
	}
	return p.handleImage(ctx, desc, srcRef, dstRepo, repoPath, dstTag, progress)
}

// handleImage imports a single-image manifest.
func (p *PullExternalHandler) handleImage(
	ctx context.Context,
	desc *remote.Descriptor,
	srcRef name.Reference,
	dstRepo *metadata.Repo,
	repoPath string,
	dstTag string,
	progress *jobs.ProgressWriter,
) error {
	img, err := desc.Image()
	if err != nil {
		return httpx.SanitizeUpstreamErr(fmt.Errorf("pull_external: image: %w", err))
	}
	// Single-image pull: compute totalBytes from manifest layers + config,
	// then stream with byte-level progress ("layer N of M · accum/total").
	// We pre-walk Size() once (cheap — layer structs cache remote-descriptor
	// sizes from the manifest response) to get a stable denominator.
	layers, lerr := img.Layers()
	if lerr != nil {
		return httpx.SanitizeUpstreamErr(fmt.Errorf("pull_external: layers: %w", lerr))
	}
	var totalBytes int64
	for _, l := range layers {
		if sz, err2 := l.Size(); err2 == nil {
			totalBytes += sz
		}
	}
	if cb, err2 := img.RawConfigFile(); err2 == nil {
		totalBytes += int64(len(cb))
	}
	var accumulatedDone int64
	if err := p.streamImageBlobs(ctx, img, progress, &accumulatedDone, totalBytes, ""); err != nil {
		return err
	}
	raw, err := img.RawManifest()
	if err != nil {
		return httpx.SanitizeUpstreamErr(fmt.Errorf("pull_external: raw manifest: %w", err))
	}
	mt, err := img.MediaType()
	if err != nil {
		return httpx.SanitizeUpstreamErr(fmt.Errorf("pull_external: media type: %w", err))
	}
	// End-of-pull progress ping so UI renders "done · total/total".
	_ = progress.Set(ctx, "done", accumulatedDone, totalBytes)
	return p.commitManifest(ctx, dstRepo, repoPath, dstTag, raw, string(mt), srcRef, false)
}

// handleIndex imports a manifest list / image index, walking every child
// manifest and recursively writing them into the dst repo.
func (p *PullExternalHandler) handleIndex(
	ctx context.Context,
	desc *remote.Descriptor,
	srcRef name.Reference,
	dstRepo *metadata.Repo,
	repoPath string,
	dstTag string,
	progress *jobs.ProgressWriter,
) error {
	idx, err := desc.ImageIndex()
	if err != nil {
		return httpx.SanitizeUpstreamErr(fmt.Errorf("pull_external: index: %w", err))
	}
	mf, err := idx.IndexManifest()
	if err != nil {
		return httpx.SanitizeUpstreamErr(fmt.Errorf("pull_external: index manifest: %w", err))
	}
	// Compute total bytes across ALL child images up-front so the progress
	// bar has a stable denominator for the whole multi-arch pull. Each
	// child's layer Size() + config size sums into indexTotal; sizes are
	// advisory (upstream-reported, not validated here).
	var indexTotal int64
	for _, cd := range mf.Manifests {
		ci, err := idx.Image(cd.Digest)
		if err != nil {
			continue
		}
		if layers, err2 := ci.Layers(); err2 == nil {
			for _, l := range layers {
				if sz, err3 := l.Size(); err3 == nil {
					indexTotal += sz
				}
			}
		}
		if cb, err2 := ci.RawConfigFile(); err2 == nil {
			indexTotal += int64(len(cb))
		}
	}

	// accumulatedDone is read+written by the CountingReader OnRead callback
	// from inside streamImageBlobs — single-goroutine loop, no lock needed.
	var accumulatedDone int64
	// Pull every child image and commit its manifest BEFORE the index,
	// because the index commit requires each child digest to already exist
	// in docker_manifests (incRefs for indexes uses manifests.IncRef).
	for i, childDesc := range mf.Manifests {
		childImg, err := idx.Image(childDesc.Digest)
		if err != nil {
			return httpx.SanitizeUpstreamErr(fmt.Errorf("pull_external: child image %s: %w",
				childDesc.Digest, err))
		}
		imageStep := fmt.Sprintf("image %d of %d", i+1, len(mf.Manifests))
		_ = progress.Set(ctx, imageStep, accumulatedDone, indexTotal)
		if err := p.streamImageBlobs(ctx, childImg, progress, &accumulatedDone, indexTotal, imageStep); err != nil {
			return err
		}
		childRaw, err := childImg.RawManifest()
		if err != nil {
			return httpx.SanitizeUpstreamErr(fmt.Errorf("pull_external: child raw: %w", err))
		}
		childMT, err := childImg.MediaType()
		if err != nil {
			return httpx.SanitizeUpstreamErr(fmt.Errorf("pull_external: child mt: %w", err))
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
		return httpx.SanitizeUpstreamErr(fmt.Errorf("pull_external: index raw: %w", err))
	}
	mt, err := idx.MediaType()
	if err != nil {
		return httpx.SanitizeUpstreamErr(fmt.Errorf("pull_external: index mt: %w", err))
	}
	return p.commitManifest(ctx, dstRepo, repoPath, dstTag, raw, string(mt), srcRef, false)
}

// streamImageBlobs downloads every layer + the config blob into CAS. The
// CAS promotes each blob atomically by its sha256 so concurrent pulls
// don't fight over the same on-disk file.
//
// Wraps each layer's compressed ReadCloser with jobs.CountingReader so
// ProgressWriter receives per-read byte counts (advancing *accumDone).
// When imagePrefix is non-empty (index child
// images pass "image 2 of 3"), the emitted step is
// "<imagePrefix> · layer N of M"; otherwise just "layer N of M".
// A totalBytes == 0 is valid (caller couldn't compute total) — in that
// case progress still emits with total=0 and the UI renders step text.
func (p *PullExternalHandler) streamImageBlobs(
	ctx context.Context,
	img v1.Image,
	progress *jobs.ProgressWriter,
	accumDone *int64,
	totalBytes int64,
	imagePrefix string,
) error {
	layers, err := img.Layers()
	if err != nil {
		return httpx.SanitizeUpstreamErr(fmt.Errorf("pull_external: layers: %w", err))
	}
	for i, l := range layers {
		layerStep := fmt.Sprintf("layer %d of %d", i+1, len(layers))
		if imagePrefix != "" {
			layerStep = imagePrefix + " · " + layerStep
		}
		if err := p.streamLayer(ctx, l, progress, accumDone, totalBytes, layerStep); err != nil {
			return err
		}
	}
	cfg, err := img.RawConfigFile()
	if err != nil {
		return httpx.SanitizeUpstreamErr(fmt.Errorf("pull_external: raw config: %w", err))
	}
	// Config blob: wrap bytes.Reader with CountingReader too so the tiny
	// config download is reflected in progress. For single-goroutine
	// correctness, the OnRead callback mutates *accumDone directly.
	cfgStep := "config"
	if imagePrefix != "" {
		cfgStep = imagePrefix + " · config"
	}
	cr := &jobs.CountingReader{R: bytes.NewReader(cfg), OnRead: func(n int) {
		if accumDone != nil {
			*accumDone += int64(n)
			_ = progress.Set(ctx, cfgStep, *accumDone, totalBytes)
		}
	}}
	if _, _, err := p.deps.CAS.Put(ctx, cr); err != nil {
		return fmt.Errorf("pull_external: cas put config: %w", err)
	}
	return nil
}

func (p *PullExternalHandler) streamLayer(
	ctx context.Context,
	l v1.Layer,
	progress *jobs.ProgressWriter,
	accumDone *int64,
	totalBytes int64,
	step string,
) error {
	rc, err := l.Compressed()
	if err != nil {
		return httpx.SanitizeUpstreamErr(fmt.Errorf("pull_external: layer compressed: %w", err))
	}
	defer func() { _ = rc.Close() }()
	cr := &jobs.CountingReader{R: rc, OnRead: func(n int) {
		if accumDone != nil {
			*accumDone += int64(n)
			_ = progress.Set(ctx, step, *accumDone, totalBytes)
		}
	}}
	if _, _, err := p.deps.CAS.Put(ctx, cr); err != nil {
		return fmt.Errorf("pull_external: cas put layer: %w", err)
	}
	// Drain any remaining bytes (go-containerregistry sometimes leaves
	// unread tail when we close early; harmless here but keep the stream
	// symmetric).
	_, _ = io.Copy(io.Discard, cr)
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
	h     *Handler
	creds *metadata.UpstreamCredsRepo
	jobs  *metadata.SyncJobsRepo
	kick  func()
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
		// Server-side log captures the sql/path detail; client gets a
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
		switch actor.Kind {
		case auth.ActorKindUser:
			id := actor.ID
			ev.ActorUserID = &id
		case auth.ActorKindAPIKey:
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
