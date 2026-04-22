// Package git — Plan 11-07 Task 2 (GITMIRROR-04 / D-12) LFS refusal handler.
//
// Git LFS is not supported by OmniRepo v1.4. Because go-git has no LFS
// client, a silent fall-through on /info/lfs/objects/batch would either:
//
//   - 404 for dev repos (the backend has no LFS routes at all), or
//   - on mirror repos, let LFS clients attempt to fetch objects directly
//     from the upstream LFS host — violating the runtime air-gap
//     invariant (Pitfall 4 / T-11-07-02: LFS pointer bypass).
//
// D-12 resolution: return 501 Not Implemented with envelope code
// `lfs.not_supported` on the LFS batch endpoint for BOTH dev and mirror
// repos. Applying the gate only to mirrors would leave a silent-success
// path on dev repos where a pointer file slipped through receive-pack
// could start routing clients back to an upstream LFS host on clone.
//
// The route is mounted at /info/lfs/objects/batch — chi's pattern trie
// matches this literal path before the /* catch-all so the LFS refusal
// wins for this path while /info/refs and every other Smart-HTTP endpoint
// continue through to the backend.
package git

import (
	"net/http"

	"github.com/dxc-internal/omnirepo/internal/httperr"
)

// rejectLFS returns 501 lfs.not_supported for any LFS batch API request.
// Method-agnostic: POST is the spec-defined verb, but HEAD/GET probes
// and any other method also return 501 since the feature is globally
// unsupported in v1.4.
func (h *Handler) rejectLFS(w http.ResponseWriter, r *http.Request) {
	err := httperr.Validation(
		"lfs.not_supported",
		"Git LFS is not supported. Mirrors store only pointer files; clients must disable LFS (GIT_LFS_SKIP_SMUDGE=1) or source LFS objects separately.",
		httperr.WithStatus(http.StatusNotImplemented), // 501
	)
	httperr.Write(w, r, err)
}
