// Package oci — /v2/<name>/tags/list and /v2/<name>/tags/<tag> DELETE.
// Cursor pagination per OCI Distribution spec §tags-list.
package oci

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/vladoportos/omnirepo/internal/audit"
)

// tagsListResponse is the {"name","tags":[...]} envelope per spec.
type tagsListResponse struct {
	Name string   `json:"name"`
	Tags []string `json:"tags"`
}

// tagsList handles GET /v2/<name>/tags/list?n=N&last=TAG.
//
//   - n defaults to 100, clamped to [1,1000].
//   - last is the cursor (empty → start).
//   - Sorted lex asc.
//   - If a next page exists, the `Link` header advertises ?n&last=<lasttag>
//     per RFC 5988 with rel="next".
func (h *Handler) tagsList(w http.ResponseWriter, r *http.Request) {
	rr := h.resolveRepo(w, r, true)
	if rr == nil {
		return
	}
	if !h.requireReader(w, r, rr.repo) {
		return
	}
	if h.tags == nil {
		writeOCIErr(w, http.StatusNotFound, ErrCodeManifestUnk,
			errors.New("tags repo not wired"))
		return
	}

	q := r.URL.Query()
	limit := 100
	if n := q.Get("n"); n != "" {
		if v, err := strconv.Atoi(n); err == nil && v > 0 {
			limit = v
		}
	}
	if limit > 1000 {
		limit = 1000
	}
	after := q.Get("last")

	tags, err := h.tags.ListPaginated(r.Context(), rr.repo.ID, rr.image, limit, after)
	if err != nil {
		writeOCIErr(w, http.StatusInternalServerError, ErrCodeUnknown, err)
		return
	}
	resp := tagsListResponse{
		Name: rr.repo.Name,
		Tags: tags,
	}
	// We signal a next page when we returned exactly `limit` items. The
	// client re-requests with ?last=<tags[last]>.
	if len(tags) == limit && len(tags) > 0 {
		last := tags[len(tags)-1]
		next := url.Values{}
		next.Set("n", strconv.Itoa(limit))
		next.Set("last", last)
		// Link targets the same path with updated cursor.
		linkPath := fmt.Sprintf("/v2/%s/tags/list?%s", rr.fullPath, next.Encode())
		w.Header().Set("Link", fmt.Sprintf(`<%s>; rel="next"`, linkPath))
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

// tagDelete handles DELETE /v2/<name>/tags/<tag> (Docker-compat surface).
// Delegates to manifestDelete's tag-form logic: unlink tag, cascade to
// manifest delete when it was the last reference AND ref_count==0.
func (h *Handler) tagDelete(w http.ResponseWriter, r *http.Request) {
	rr := h.resolveRepo(w, r, true)
	if rr == nil {
		return
	}
	if !h.requireWriter(w, r, rr.repo) {
		return
	}
	if h.tags == nil || h.manifests == nil {
		writeOCIErr(w, http.StatusInternalServerError, ErrCodeUnknown,
			errors.New("tags/manifests repo not wired"))
		return
	}
	tag := chi.URLParam(r, "tag")
	if tag == "" {
		writeOCIErr(w, http.StatusBadRequest, ErrCodeTagInvalid,
			errors.New("missing tag"))
		return
	}
	ctx := r.Context()

	digest, err := h.tags.Resolve(ctx, rr.repo.ID, rr.image, tag)
	if err != nil {
		writeOCIErr(w, http.StatusInternalServerError, ErrCodeUnknown, err)
		return
	}
	if digest == "" {
		writeOCIErr(w, http.StatusNotFound, ErrCodeManifestUnk,
			fmt.Errorf("tag %q not found", tag))
		return
	}

	m, err := h.manifests.GetByDigest(ctx, rr.repo.ID, digest)
	if err != nil {
		writeOCIErr(w, http.StatusInternalServerError, ErrCodeUnknown, err)
		return
	}
	// Same class as manifestDelete. A malformed stored body must fail the
	// DELETE with MANIFEST_INVALID so ref-counts stay consistent rather than
	// silently skipping the decrement step (reapManifest re-parses to release
	// refs; validate here first).
	if m != nil {
		if _, _, refsErr := manifestRefs(m.Body); refsErr != nil {
			writeOCIErr(w, http.StatusBadRequest, ErrCodeManifestInvalid, refsErr)
			return
		}
	}

	err = h.db.WriteTx(ctx, func(tx *sql.Tx) error {
		if _, err := h.tags.Delete(ctx, tx, rr.repo.ID, rr.image, tag); err != nil {
			return err
		}
		if m == nil {
			// Manifest row already gone — just the tag pointer was stale.
			return nil
		}
		count, err := h.tags.CountForDigestTx(ctx, tx, rr.repo.ID, digest)
		if err != nil {
			return err
		}
		if count > 0 || m.RefCount > 0 {
			return nil
		}
		// Last reference → cascade to manifest delete (recursively reaping any
		// orphaned index children).
		return h.reapManifest(ctx, tx, rr.repo.ID, digest, m.Body)
	})
	if err != nil {
		writeOCIErr(w, http.StatusInternalServerError, ErrCodeUnknown, err)
		return
	}

	h.emitManifestAudit(r, audit.EvtOCITagDeleted, digest, "ok", map[string]any{
		"repo": rr.fullPath,
		"tag":  tag,
	})
	w.WriteHeader(http.StatusAccepted)
}
