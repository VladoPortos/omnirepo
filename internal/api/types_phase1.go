package api

// Request/response types for the D-36 Phase 1 admin REST surface. Every
// handler in admin_phase1.go speaks exactly these shapes; they are the public
// contract consumed by both Phase 5's UI and curl-based tests.

// LoginRequest is the body of POST /api/v1/auth/login.
type LoginRequest struct {
	Login    string `json:"login"`
	Password string `json:"password"`
}

// LoginResponse is returned on successful login. The session cookie is set
// separately by the handler; this body is informational (matches the shape
// the Phase 5 UI reads to decide whether to redirect to /change-password).
type LoginResponse struct {
	Login              string `json:"login"`
	IsSuperAdmin       bool   `json:"is_super_admin"`
	MustChangePassword bool   `json:"must_change_password"`
}

// ChangePasswordRequest is the body of POST /api/v1/auth/change-password.
type ChangePasswordRequest struct {
	Current string `json:"current"`
	New     string `json:"new"`
}

// MeResponse is the body of GET /api/v1/me.
type MeResponse struct {
	ID                 int64  `json:"id"`
	Login              string `json:"login"`
	Email              string `json:"email"`
	IsSuperAdmin       bool   `json:"is_super_admin"`
	MustChangePassword bool   `json:"must_change_password"`
}

// CreateUserRequest is the body of POST /api/v1/admin/users.
type CreateUserRequest struct {
	Login string `json:"login"`
	Email string `json:"email"`
}

// CreateUserResponse is returned on successful user creation. OneTimePassword
// is revealed ONCE (TEN-08).
type CreateUserResponse struct {
	Login           string `json:"login"`
	OneTimePassword string `json:"one_time_password"`
}

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
