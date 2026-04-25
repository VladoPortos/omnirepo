package auth

import (
	"fmt"
	"regexp"

	"github.com/dxc-internal/omnirepo/internal/httpx"
)

// ProjectNameRegex is the D-26 project-name slug: must start with [a-z0-9],
// then up to 62 more chars from [a-z0-9._-]. Total length ≤ 63.
var ProjectNameRegex = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)

// LoginRegex is the login-name slug: case-insensitive start char, then up to
// 62 more chars from [a-zA-Z0-9._-]. Logins may contain uppercase; project
// names may not.
var LoginRegex = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,62}$`)

// ProjectNameValid returns nil when name is a legal OmniRepo project slug
// (matches ProjectNameRegex) AND does NOT collide with any reserved top-level
// route prefix (FOUND-10). The reserved set lives in internal/httpx so router
// and validator share one source of truth.
func ProjectNameValid(name string) error {
	if !ProjectNameRegex.MatchString(name) {
		return fmt.Errorf("invalid project name %q: must match %s", name, ProjectNameRegex.String())
	}
	if httpx.IsReserved(name) {
		return fmt.Errorf("invalid project name %q: reserved prefix", name)
	}
	return nil
}

// RepoNameValid returns nil when name is a legal OmniRepo repo slug. Repos
// share the same regex as projects (see ProjectNameRegex) but are not subject
// to the reserved-prefix check — repo names live under /projects/<p>/repos/...,
// never at the top level, so "docker", "api", etc. are all fine. A separate
// function is kept so validation error messages surface the right resource
// kind ("invalid repo name ..." instead of "invalid project name ...").
func RepoNameValid(name string) error {
	if !ProjectNameRegex.MatchString(name) {
		return fmt.Errorf("invalid repo name %q: must match %s", name, ProjectNameRegex.String())
	}
	return nil
}

// LoginValid returns nil when login matches LoginRegex. Reserved-prefix
// rejection does not apply to logins — they never appear as top-level URL
// segments. Reserved names ARE still rejected downstream by project create
// endpoints when a login's personal project would collide, but that's a
// handler concern.
func LoginValid(login string) error {
	if !LoginRegex.MatchString(login) {
		return fmt.Errorf("invalid login %q: must match %s", login, LoginRegex.String())
	}
	return nil
}

// PasswordMinLen is the floor for any user-chosen password. Setup,
// self-service change, and admin force-reset all enforce this — without
// a single source of truth the bootstrap path validated 8 chars while
// the change-password and admin-reset paths accepted "abc" (wt4 F-04.2).
const PasswordMinLen = 8

// PasswordValid returns nil when pw meets the policy floor. Centralized
// so every entry point that sets a password (POST /setup/superadmin,
// POST /auth/change-password, PATCH /admin/users/{login}.new_password,
// PUT /admin/users/{login}/password reset flows) shares one rule. The
// message is shaped to match what setup already returned so existing
// clients see no behavioural delta.
func PasswordValid(pw string) error {
	if len(pw) < PasswordMinLen {
		return fmt.Errorf("password must be at least %d characters", PasswordMinLen)
	}
	return nil
}
