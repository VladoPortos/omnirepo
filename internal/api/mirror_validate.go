// Package api — Phase 8 Plan 01 mirror-repo request-shape validators.
//
// These helpers enforce the 5 validation branches for the mirror fields on
// POST /api/v1/projects/{name}/repos and PATCH
// /api/v1/projects/{name}/repos/{type}/{repo}:
//
//   A. mirror_type_unsupported — is_mirror=true only with deb/rpm/pypi/helm
//   B. mirror_url_invalid      — upstream URL must be http(s) with a host
//   C. mirror_filter_invalid   — filter JSON must decode into the protocol
//                                 SyncFilter shape
//   D. mirror_cred_wrong_project — mirror_cred_id must belong to the same
//                                   project as the repo (T-08-01-07)
//   E. mirror_url_immutable    — PATCH cannot change is_mirror or
//                                 mirror_upstream_url
//
// Validation is JSON-shape-only here; the repo-layer migration ensures
// mirror_filter_json round-trips as TEXT. We deliberately do NOT import the
// protocol packages — a copy of the SyncFilter shape per protocol lives
// here (5–6 fields total) so the api package stays cycle-free.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"

	"github.com/dxc-internal/omnirepo/internal/metadata"
)

// mirrorSupportedTypes enumerates repo types eligible for the is_mirror flag
// (D-01). Raw/S3/Git/Docker are out of scope for v1.1 per the design spec.
var mirrorSupportedTypes = map[string]struct{}{
	"deb":  {},
	"rpm":  {},
	"pypi": {},
	"helm": {},
}

// validateMirrorUpstreamURL enforces http(s) scheme + non-empty host.
// Rejects file://, javascript:, ftp://, bare paths, and missing hosts
// (T-08-01-03).
func validateMirrorUpstreamURL(raw string) bool {
	if raw == "" {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	if u.Host == "" {
		return false
	}
	return true
}

// debFilterShape mirrors internal/protocol/deb.SyncFilter. Keeps the api
// package cycle-free. PascalCase JSON keys match Go's default encoding of
// the source struct (no json tags).
type debFilterShape struct {
	Names      []string `json:"Names,omitempty"`
	Globs      []string `json:"Globs,omitempty"`
	Suites     []string `json:"Suites,omitempty"`
	Components []string `json:"Components,omitempty"`
	Arches     []string `json:"Arches,omitempty"`
}

// rpmFilterShape mirrors internal/protocol/rpm.SyncFilter.
type rpmFilterShape struct {
	Names []string `json:"Names,omitempty"`
	Globs []string `json:"Globs,omitempty"`
}

// pypiFilterShape mirrors internal/protocol/pypi.SyncFilter.
type pypiFilterShape = rpmFilterShape

// helmFilterShape mirrors internal/protocol/helm.SyncFilter.
type helmFilterShape = rpmFilterShape

// validateMirrorFilter JSON-decodes the filter against the appropriate
// protocol-specific shape and rejects unknown keys. Empty or null filters
// are legal (sync-everything).
func validateMirrorFilter(repoType string, filter json.RawMessage) bool {
	// An empty filter (null or {}) is legal for every protocol.
	if len(filter) == 0 {
		return true
	}
	dec := json.NewDecoder(bytes.NewReader(filter))
	dec.DisallowUnknownFields()
	switch repoType {
	case "deb":
		var f debFilterShape
		return dec.Decode(&f) == nil
	case "rpm":
		var f rpmFilterShape
		return dec.Decode(&f) == nil
	case "pypi":
		var f pypiFilterShape
		return dec.Decode(&f) == nil
	case "helm":
		var f helmFilterShape
		return dec.Decode(&f) == nil
	default:
		return false
	}
}

// mirrorCredOwnership asserts credID belongs to ownerProjectID.
// Returns (ok, exists):
//   - ok=true, exists=true:   cred exists and is owned by projectID
//   - ok=false, exists=true:  cross-project (T-08-01-07)
//   - ok=false, exists=false: cred id is missing or repo unavailable
//
// Handlers render the 400 mirror_cred_wrong_project envelope from
// !ok, regardless of which sub-case triggered — leaking
// existence/not-existence across projects would be an info-disclosure
// vector.
func mirrorCredOwnership(ctx context.Context, upstreamCreds *metadata.UpstreamCredsRepo, projectID, credID int64) (ok bool, exists bool) {
	if upstreamCreds == nil {
		return false, false
	}
	ownerID, err := upstreamCreds.GetProjectID(ctx, credID)
	if err != nil {
		return false, false
	}
	if ownerID != projectID {
		return false, true
	}
	return true, true
}
