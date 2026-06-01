// Package git defines the GitServer interface and shared helpers for the
// two Smart-HTTP backends (gogit primary, gitkit fallback).
//
// Middleware consumes Handler(repoPath) once auth/membership/mutex
// gates have passed. The interface is intentionally minimal — backend
// selection is config-driven (server.git_backend) and the resolved on-disk
// path is the sole per-request input.
package git

import (
	"net/http"
	"os"

	gogitpkg "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
)

// GitServer is the common interface both backends satisfy.
//
// Handler returns an http.Handler that serves info/refs + git-upload-pack +
// git-receive-pack for the bare repo at repoPath. The handler is stateless;
// callers pass the resolved on-disk path
// (repoPath = "<DataRoot>/repos/<project>/git/<repo>.git").
type GitServer interface {
	Handler(repoPath string) http.Handler

	// BackendName returns "gogit" or "gitkit" for logging + conformance
	// parameterization.
	BackendName() string
}

// InitBare creates an empty bare repo at repoPath with HEAD symbolic-ref
// pointing at refs/heads/<initialBranch>. Called by the repo-create
// hook. Intermediate directories are created with 0o755.
//
// Uses go-git's PlainInit + WithDefaultBranch option rather than shelling
// to the `git` binary — keeps the gogit backend pure-Go (the image ships
// git(1) anyway as the gitkit fallback, but there's no reason to pay a
// subprocess cost here).
func InitBare(repoPath, initialBranch string) error {
	if err := os.MkdirAll(repoPath, 0o755); err != nil {
		return err
	}
	refName := plumbing.ReferenceName("refs/heads/" + initialBranch)
	_, err := gogitpkg.PlainInit(repoPath, true, gogitpkg.WithDefaultBranch(refName))
	return err
}
