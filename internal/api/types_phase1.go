package api

import "encoding/json"

// Phase 1 hand-written request/response types that are NOT yet covered by
// the generated types_gen.go (or use different shapes the handlers rely on).
// Types that overlap with the OpenAPI-generated types have been removed;
// handlers now use the generated versions directly.

// CreateUserRequest is the body of POST /api/v1/admin/users.
// Kept here because the generated UserCreate uses the same shape but a
// different Go name; we alias for backward compat until handler migration.
type CreateUserRequest = UserCreate

// CreateUserResponse aliases the generated UserCreateResponse.
type CreateUserResponse = UserCreateResponse

// CreateProjectRequest is the body of POST /api/v1/projects.
type CreateProjectRequest struct {
	Name          string `json:"name"`
	DescriptionMD string `json:"description_md"`
}

// CreateRepoRequest is the body of POST /api/v1/projects/{name}/repos.
//
// Phase 8 Plan 01 (MIRROR-01..07): optional mirror fields. IsMirror is only
// valid when Type ∈ {deb,rpm,pypi,helm}; MirrorUpstreamURL must be http(s)
// with a host; MirrorFilter must JSON-decode into the protocol's SyncFilter
// shape. MirrorCredID (if set) must reference an upstream_creds row in the
// same project as the repo being created. See handleCreateRepo for the
// validation branch order.
type CreateRepoRequest struct {
	Name            string  `json:"name"`
	Type            string  `json:"type"`
	DescriptionMD   string  `json:"description_md"`
	AutoScan        *bool   `json:"auto_scan"`
	BlockOnSeverity *string `json:"block_on_severity"`
	PublicRead      *bool   `json:"public_read"`

	IsMirror          bool            `json:"is_mirror,omitempty"`
	MirrorUpstreamURL string          `json:"mirror_upstream_url,omitempty"`
	MirrorFilter      json.RawMessage `json:"mirror_filter,omitempty"`
	MirrorCredID      *int64          `json:"mirror_cred_id,omitempty"`
	ScanOnSync        bool            `json:"scan_on_sync,omitempty"`
}
