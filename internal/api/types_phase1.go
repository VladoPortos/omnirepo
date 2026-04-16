package api

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
type CreateRepoRequest struct {
	Name            string  `json:"name"`
	Type            string  `json:"type"`
	DescriptionMD   string  `json:"description_md"`
	AutoScan        *bool   `json:"auto_scan"`
	BlockOnSeverity *string `json:"block_on_severity"`
	PublicRead      *bool   `json:"public_read"`
}
