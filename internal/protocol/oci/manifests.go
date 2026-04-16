// Package oci — /v2 manifest state machine (Phase 02-07).
//
// Invariants carried through from the plan action block:
//
//  1. Manifest body stored byte-for-byte as BLOB (Pitfall 5). GET
//     responds with the stored bytes; no re-marshal.
//  2. Manifest PUT runs in one writer tx that:
//       - inserts the docker_manifests row
//       - upserts the tag pointer, resolving the prior digest
//       - for tag overwrite: decrements refs on blobs only referenced by
//         the prior manifest (Pitfall 1 ref-delta)
//       - for each new referenced blob: blobs.IncRef + blobs.Touch
//       - for child manifest digests in an index: manifests.IncRef
//       - writes artifacts_fts row (D-40)
//       - (if auto_scan) enqueues a scans row
//  3. Manifest DELETE removes the row AND decrements per-referenced-blob
//     refs in one tx (Pitfall 7). Tag-form DELETE only unlinks the tag
//     unless it was the last reference, then it cascades.
//  4. Manifest body capped at 4 MiB → 413 MANIFEST_INVALID on overflow.
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
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/dxc-internal/omnirepo/internal/audit"
	"github.com/dxc-internal/omnirepo/internal/auth"
	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/scan"
)

// isDigestRef reports whether the /v2/.../manifests/<reference> URL segment
// is a digest (sha256:<hex>) vs. a tag name.
func isDigestRef(s string) bool { return validDigest(s) }

// manifestRefs extracts every "digest" string under the manifest body.
// For image manifest (v2): config.digest + layers[*].digest.
// For image index / manifest list: manifests[*].digest (child manifests).
// Returns a sorted-by-insertion, deduplicated slice and whether the body
// parsed as an index (so the caller knows to refcount on docker_manifests
// rather than docker_blobs).
func manifestRefs(body []byte) (refs []string, isIndex bool, err error) {
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, false, fmt.Errorf("manifest: parse: %w", err)
	}
	// Heuristic: an index body carries "manifests" array; a single-manifest
	// body carries "config" + "layers".
	seen := map[string]struct{}{}
	collect := func(d string) {
		if !strings.HasPrefix(d, "sha256:") {
			return
		}
		if _, ok := seen[d]; ok {
			return
		}
		seen[d] = struct{}{}
		refs = append(refs, d)
	}
	if mfs, ok := raw["manifests"].([]any); ok {
		isIndex = true
		for _, m := range mfs {
			mm, ok := m.(map[string]any)
			if !ok {
				continue
			}
			if d, ok := mm["digest"].(string); ok {
				collect(d)
			}
		}
		return refs, true, nil
	}
	if cfg, ok := raw["config"].(map[string]any); ok {
		if d, ok := cfg["digest"].(string); ok {
			collect(d)
		}
	}
	if layers, ok := raw["layers"].([]any); ok {
		for _, l := range layers {
			ll, ok := l.(map[string]any)
			if !ok {
				continue
			}
			if d, ok := ll["digest"].(string); ok {
				collect(d)
			}
		}
	}
	return refs, false, nil
}

// computeMfDigest returns "sha256:" + hex(sha256(body)).
func computeMfDigest(body []byte) string {
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// manifestMediaType honors Content-Type; falls back to OCI manifest v1 if
// caller did not send one.
func manifestMediaType(r *http.Request) string {
	if ct := r.Header.Get("Content-Type"); ct != "" {
		return ct
	}
	return MediaTypeOCIManifest
}

// manifestPut handles PUT /v2/<name>/manifests/<reference>.
func (h *Handler) manifestPut(w http.ResponseWriter, r *http.Request) {
	rr := h.resolveRepo(w, r, true)
	if rr == nil {
		return
	}
	if !h.requireWriter(w, r, rr.repo) {
		return
	}
	if h.manifests == nil || h.tags == nil {
		writeOCIErr(w, http.StatusInternalServerError, ErrCodeUnknown,
			errors.New("manifests repo not wired"))
		return
	}
	reference := chi.URLParam(r, "reference")
	if reference == "" {
		writeOCIErr(w, http.StatusBadRequest, ErrCodeManifestInvalid,
			errors.New("missing reference"))
		return
	}

	// T-02-07-03: cap body at 4 MiB.
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, ManifestMaxBytes))
	if err != nil {
		writeOCIErr(w, http.StatusRequestEntityTooLarge, ErrCodeManifestInvalid,
			fmt.Errorf("manifest body exceeds %d bytes: %w", ManifestMaxBytes, err))
		return
	}
	if len(body) == 0 {
		writeOCIErr(w, http.StatusBadRequest, ErrCodeManifestInvalid,
			errors.New("empty manifest body"))
		return
	}

	mfDigest := computeMfDigest(body)

	// If reference is digest form, it MUST equal mfDigest (T-02-07-06).
	if isDigestRef(reference) && reference != mfDigest {
		writeOCIErr(w, http.StatusBadRequest, ErrCodeManifestInvalid,
			fmt.Errorf("reference digest %s != body sha256 %s", reference, mfDigest))
		return
	}

	// Parse refs BEFORE opening the tx so we can 404 on missing blobs
	// without holding the writer lock.
	refs, isIndex, err := manifestRefs(body)
	if err != nil {
		writeOCIErr(w, http.StatusBadRequest, ErrCodeManifestInvalid, err)
		return
	}

	// Validate referenced digests exist. For a normal manifest, every
	// ref is a blob digest. For an index, every ref is a child manifest
	// digest in the SAME repo.
	ctx := r.Context()
	if isIndex {
		for _, d := range refs {
			got, err := h.manifests.GetByDigest(ctx, rr.repo.ID, d)
			if err != nil {
				writeOCIErr(w, http.StatusInternalServerError, ErrCodeUnknown, err)
				return
			}
			if got == nil {
				writeOCIErr(w, http.StatusNotFound, ErrCodeManifestUnk,
					fmt.Errorf("child manifest %s not found in repo", d))
				return
			}
		}
	} else {
		for _, d := range refs {
			b, err := h.blobs.Stat(ctx, d)
			if err != nil {
				writeOCIErr(w, http.StatusInternalServerError, ErrCodeUnknown, err)
				return
			}
			if b == nil {
				writeOCIErr(w, http.StatusNotFound, ErrCodeBlobUnknown,
					fmt.Errorf("referenced blob %s missing", d))
				return
			}
		}
	}

	mediaType := manifestMediaType(r)
	repoPath := rr.fullPath
	var scanEnqueued bool

	err = h.db.WriteTx(ctx, func(tx *sql.Tx) error {
		enq, err := h.writeManifestWithRefcounts(
			ctx, tx, rr.repo.ID, repoPath, reference, mfDigest, mediaType, body,
			refs, isIndex, rr.repo.AutoScan,
		)
		if err != nil {
			return err
		}
		scanEnqueued = enq
		return nil
	})
	if err != nil {
		writeOCIErr(w, http.StatusInternalServerError, ErrCodeUnknown, err)
		return
	}

	// After tx commits, kick the scan pool so auto-scan starts ASAP.
	if scanEnqueued && h.scanKick != nil {
		h.scanKick()
	}

	h.emitManifestAudit(r, audit.EvtOCIManifestUploaded, mfDigest, "ok", map[string]any{
		"repo":      repoPath,
		"reference": reference,
		"media":     mediaType,
		"size":      len(body),
	})

	w.Header().Set("Location", fmt.Sprintf("/v2/%s/manifests/%s", repoPath, mfDigest))
	w.Header().Set("Docker-Content-Digest", mfDigest)
	w.Header().Set("Content-Length", "0")
	w.WriteHeader(http.StatusCreated)
}

// incRefs IncRefs every ref in the same writer tx. For index bodies, refs
// are child manifest digests; for regular manifests, refs are blob digests.
func (h *Handler) incRefs(ctx context.Context, tx *sql.Tx, repoID int64, refs []string, isIndex bool) error {
	if isIndex {
		for _, d := range refs {
			if err := h.manifests.IncRef(ctx, tx, repoID, d); err != nil {
				return err
			}
		}
		return nil
	}
	for _, d := range refs {
		if err := h.blobs.IncRef(ctx, tx, d); err != nil {
			return err
		}
		if err := h.blobs.Touch(ctx, tx, d); err != nil {
			return err
		}
	}
	return nil
}

// decRefs mirrors incRefs for DELETE / tag-overwrite paths.
//
// WR-05: a previous iteration silently swallowed ErrRefCountUnderflow and
// kept going. That masks real bugs — if DecRef returns underflow, the
// caller's bookkeeping is inconsistent (manifest references more refs than
// the refcount table records), which is exactly the class of silent data
// corruption that orphans or double-deletes blobs. Fail the tx instead so
// the operation is retried with fresh state or the problem surfaces to
// operators.
func (h *Handler) decRefs(ctx context.Context, tx *sql.Tx, repoID int64, refs []string, isIndex bool) error {
	if isIndex {
		for _, d := range refs {
			if err := h.manifests.DecRef(ctx, tx, repoID, d); err != nil {
				return err
			}
		}
		return nil
	}
	for _, d := range refs {
		if err := h.blobs.DecRef(ctx, tx, d); err != nil {
			return err
		}
	}
	return nil
}

// resolveManifestRef returns the manifest digest for a /v2/.../manifests/<ref>
// reference. `ref` is either a digest or a tag name.
func (h *Handler) resolveManifestRef(ctx context.Context, repoID int64, ref string) (digest string, found bool, err error) {
	if isDigestRef(ref) {
		m, err := h.manifests.GetByDigest(ctx, repoID, ref)
		if err != nil {
			return "", false, err
		}
		if m == nil {
			return "", false, nil
		}
		return ref, true, nil
	}
	d, err := h.tags.Resolve(ctx, repoID, ref)
	if err != nil {
		return "", false, err
	}
	if d == "" {
		return "", false, nil
	}
	return d, true, nil
}

// manifestGet handles GET /v2/<name>/manifests/<ref>. Serves stored body
// byte-for-byte with Docker-Content-Digest + Content-Type set from the
// stored media_type (Pitfall 5).
func (h *Handler) manifestGet(w http.ResponseWriter, r *http.Request) {
	h.manifestGetOrHead(w, r, true)
}

// manifestHead is the HEAD variant: headers only, no body.
func (h *Handler) manifestHead(w http.ResponseWriter, r *http.Request) {
	h.manifestGetOrHead(w, r, false)
}

func (h *Handler) manifestGetOrHead(w http.ResponseWriter, r *http.Request, writeBody bool) {
	rr := h.resolveRepo(w, r, true)
	if rr == nil {
		return
	}
	if !h.requireReader(w, r, rr.repo) {
		return
	}
	if h.manifests == nil || h.tags == nil {
		writeOCIErr(w, http.StatusNotFound, ErrCodeManifestUnk,
			errors.New("manifests repo not wired"))
		return
	}
	reference := chi.URLParam(r, "reference")
	if reference == "" {
		writeOCIErr(w, http.StatusBadRequest, ErrCodeManifestInvalid,
			errors.New("missing reference"))
		return
	}
	ctx := r.Context()
	digest, found, err := h.resolveManifestRef(ctx, rr.repo.ID, reference)
	if err != nil {
		writeOCIErr(w, http.StatusInternalServerError, ErrCodeUnknown, err)
		return
	}
	if !found {
		writeOCIErr(w, http.StatusNotFound, ErrCodeManifestUnk,
			fmt.Errorf("reference %q not found", reference))
		return
	}

	// 02-09 severity gate hook. No-op when nil. When the gate returns
	// *ErrBlockedByScan, write the documented JSON envelope so callers can
	// parse {error,severity,cve_count,scan_id}; otherwise fall back to the
	// generic OCI 403 DENIED envelope.
	if h.severityGate != nil {
		if err := h.severityGate(ctx, rr.repo.ID, digest); err != nil {
			if blocked, ok := IsBlockedByScan(err); ok {
				WriteBlockedResponse(w, blocked)
				return
			}
			writeOCIErr(w, http.StatusForbidden, ErrCodeDenied, err)
			return
		}
	}

	m, err := h.manifests.GetByDigest(ctx, rr.repo.ID, digest)
	if err != nil {
		writeOCIErr(w, http.StatusInternalServerError, ErrCodeUnknown, err)
		return
	}
	if m == nil {
		writeOCIErr(w, http.StatusNotFound, ErrCodeManifestUnk,
			fmt.Errorf("manifest %s not found", digest))
		return
	}
	w.Header().Set("Content-Type", m.MediaType)
	w.Header().Set("Docker-Content-Digest", digest)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(m.Body)))
	w.WriteHeader(http.StatusOK)
	if writeBody {
		_, _ = w.Write(m.Body)
	}
}

// manifestDelete handles DELETE /v2/<name>/manifests/<ref>.
//
// Semantics per must_haves + Pitfall 7:
//   - Reference is a digest: remove manifest row AND decrement per-referenced-
//     blob ref_counts in one tx. Also removes every tag pointing at the
//     digest in this repo (OCI Distribution digest-form delete semantics).
//   - Reference is a tag: tag deletion only. If it was the last tag pointing
//     at the digest AND the digest's own manifest ref_count is 0, cascade
//     into a full manifest delete (ref decrements + row removal).
func (h *Handler) manifestDelete(w http.ResponseWriter, r *http.Request) {
	rr := h.resolveRepo(w, r, true)
	if rr == nil {
		return
	}
	if !h.requireWriter(w, r, rr.repo) {
		return
	}
	if h.manifests == nil || h.tags == nil {
		writeOCIErr(w, http.StatusInternalServerError, ErrCodeUnknown,
			errors.New("manifests repo not wired"))
		return
	}
	reference := chi.URLParam(r, "reference")
	if reference == "" {
		writeOCIErr(w, http.StatusBadRequest, ErrCodeManifestInvalid,
			errors.New("missing reference"))
		return
	}
	ctx := r.Context()

	var targetDigest string
	var tagForm bool
	if isDigestRef(reference) {
		targetDigest = reference
	} else {
		tagForm = true
		d, err := h.tags.Resolve(ctx, rr.repo.ID, reference)
		if err != nil {
			writeOCIErr(w, http.StatusInternalServerError, ErrCodeUnknown, err)
			return
		}
		if d == "" {
			writeOCIErr(w, http.StatusNotFound, ErrCodeManifestUnk,
				fmt.Errorf("tag %q not found", reference))
			return
		}
		targetDigest = d
	}

	m, err := h.manifests.GetByDigest(ctx, rr.repo.ID, targetDigest)
	if err != nil {
		writeOCIErr(w, http.StatusInternalServerError, ErrCodeUnknown, err)
		return
	}
	if m == nil {
		writeOCIErr(w, http.StatusNotFound, ErrCodeManifestUnk,
			fmt.Errorf("manifest %s not found", targetDigest))
		return
	}
	// WR-04: a parse failure here used to be swallowed, leaving refs=nil
	// and dropping the ref-decrement step entirely — orphaning blobs so GC
	// could never reclaim them. Fail the DELETE with MANIFEST_INVALID so
	// the caller knows the stored body is broken and must be manually
	// remediated rather than silently leaking bytes.
	refs, isIndex, refsErr := manifestRefs(m.Body)
	if refsErr != nil {
		writeOCIErr(w, http.StatusBadRequest, ErrCodeManifestInvalid, refsErr)
		return
	}

	err = h.db.WriteTx(ctx, func(tx *sql.Tx) error {
		if tagForm {
			// Unlink the tag only.
			if _, err := h.tags.Delete(ctx, tx, rr.repo.ID, reference); err != nil {
				return err
			}
			// If another tag still points at digest, or the manifest itself
			// is referenced (ref_count > 0 via index parents), stop.
			count, err := h.tags.CountForDigestTx(ctx, tx, rr.repo.ID, targetDigest)
			if err != nil {
				return err
			}
			if count > 0 || m.RefCount > 0 {
				return nil
			}
			// Last reference — cascade into full delete.
		} else {
			// Digest-form delete: also unlink every tag pointing at digest.
			if _, err := tx.ExecContext(ctx,
				`DELETE FROM docker_tags WHERE repo_id=? AND digest=?`,
				rr.repo.ID, targetDigest,
			); err != nil {
				return fmt.Errorf("manifest delete: unlink tags: %w", err)
			}
		}

		// Decrement refs on blobs/child manifests, then delete the row
		// and the FTS entry.
		if err := h.decRefs(ctx, tx, rr.repo.ID, refs, isIndex); err != nil {
			return err
		}
		if err := h.manifests.Delete(ctx, tx, rr.repo.ID, targetDigest); err != nil {
			return err
		}
		return metadata.IndexArtifactDelete(ctx, tx, rr.repo.ID, targetDigest)
	})
	if err != nil {
		writeOCIErr(w, http.StatusInternalServerError, ErrCodeUnknown, err)
		return
	}

	if tagForm {
		h.emitManifestAudit(r, audit.EvtOCITagDeleted, targetDigest, "ok", map[string]any{
			"repo": rr.fullPath,
			"tag":  reference,
		})
	} else {
		h.emitManifestAudit(r, audit.EvtOCIManifestDeleted, targetDigest, "ok", map[string]any{
			"repo":      rr.fullPath,
			"reference": reference,
		})
	}
	w.WriteHeader(http.StatusAccepted)
}

// writeManifestWithRefcounts runs the manifest-insert writer-tx body used by
// both the OCI /v2 manifestPut handler and the REST pull-external / promote
// paths. It expects to be invoked inside an open WriteTx callback.
//
// Parameters:
//   - repoID: destination repo.
//   - repoPath: "<project>/<type>/<repo>" for FTS + audit.
//   - reference: either a tag name (to point at mfDigest) or a digest string
//     (reference == mfDigest). Digest references skip tag upsert.
//   - mfDigest: sha256:<hex> of body (MUST equal computeMfDigest(body) — caller
//     is responsible for validation).
//   - mediaType: Content-Type stored with the manifest.
//   - body: raw manifest bytes (byte-identical roundtrip per Pitfall 5).
//   - refs: parsed refs (layers+config for image manifests, child manifests
//     for indexes). Caller is responsible for calling manifestRefs(body).
//   - isIndex: true when body is an image index / manifest list.
//   - autoScan: when true AND h.scans is wired, enqueues a pending scan row
//     in the same tx. Returns scanEnqueued=true so caller can kick the pool
//     after commit.
//
// Behaviour matches the 02-07 manifestPut tx body:
//  1. Insert manifest row (idempotent; errors if bytes differ at same digest).
//  2. IncRef every ref (blobs for manifests, manifests for indexes).
//  3. Tag overwrite ref-delta: upsert tag; if prior digest existed and differs,
//     decrement refs on the prior manifest's refs (Pitfall 1).
//  4. FTS artifact index: delete-then-insert by (repo_id, digest).
//  5. Optional auto-scan enqueue.
func (h *Handler) writeManifestWithRefcounts(
	ctx context.Context,
	tx *sql.Tx,
	repoID int64,
	repoPath string,
	reference string,
	mfDigest string,
	mediaType string,
	body []byte,
	refs []string,
	isIndex bool,
	autoScan bool,
) (scanEnqueued bool, err error) {
	if err := h.manifests.Insert(ctx, tx, repoID, mfDigest, mediaType, body); err != nil {
		return false, err
	}
	if err := h.incRefs(ctx, tx, repoID, refs, isIndex); err != nil {
		return false, err
	}
	if reference != "" && !isDigestRef(reference) {
		priorDigest, err := h.tags.Upsert(ctx, tx, repoID, reference, mfDigest)
		if err != nil {
			return false, err
		}
		if priorDigest != "" && priorDigest != mfDigest {
			priorManifest, err := h.manifests.GetByDigestTx(ctx, tx, repoID, priorDigest)
			if err != nil {
				return false, err
			}
			if priorManifest != nil {
				priorRefs, priorIsIndex, perr := manifestRefs(priorManifest.Body)
				if perr != nil {
					priorRefs = nil
				}
				if err := h.decRefs(ctx, tx, repoID, priorRefs, priorIsIndex); err != nil {
					return false, err
				}
			}
		}
	}
	if err := metadata.IndexArtifactDelete(ctx, tx, repoID, mfDigest); err != nil {
		return false, err
	}
	ftsVersion := reference
	if ftsVersion == "" {
		ftsVersion = mfDigest
	}
	if err := metadata.IndexArtifact(ctx, tx, repoID, repoPath, ftsVersion, mfDigest); err != nil {
		return false, err
	}
	if autoScan && h.scans != nil {
		// P-1: skip enqueue for manifests Trivy can't scan (attestation
		// manifests, image indexes, empty-layer manifests). This keeps
		// junk "attestation_manifest" scan rows out of the UI for buildx
		// pushes, which enqueue one scan per child manifest by default.
		// The handler has the same skip as defense-in-depth; this side
		// just prevents the row from ever being created.
		if ok, _ := scan.IsScannableManifest(body); ok {
			if _, err := h.scans.Enqueue(ctx, tx, repoID, "docker", mfDigest); err != nil {
				return false, err
			}
			scanEnqueued = true
		}
	}
	return scanEnqueued, nil
}

// emitManifestAudit mirrors emitAudit (blobs.go) but uses "manifest" as the
// target kind so activity feeds can separate blob-push noise from the
// logical manifest upload event.
func (h *Handler) emitManifestAudit(r *http.Request, kind audit.EventKind, targetID, outcome string, details map[string]any) {
	if h.auditLogger == nil {
		return
	}
	e := audit.Event{
		Kind:       kind,
		IP:         r.RemoteAddr,
		UserAgent:  r.Header.Get("User-Agent"),
		TargetKind: "manifest",
		TargetID:   targetID,
		Outcome:    outcome,
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
