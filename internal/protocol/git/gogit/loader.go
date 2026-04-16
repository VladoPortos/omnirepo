// Package gogit is the primary Git Smart-HTTP backend — a production
// promotion of the Phase 1 spike (internal/protocol/git/spike/) per D-28.
//
// This file defines ProductionLoader, the transport.Loader used by Plan 10
// middleware to resolve /git/<project>/<repo>.git URL paths to storage.Storer
// instances rooted at the on-disk bare repo. The spike's in-memory
// SimpleLoader is replaced here with a metadata-backed lookup.
package gogit

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/go-git/go-billy/v6/osfs"
	"github.com/go-git/go-git/v6/plumbing/transport"
	"github.com/go-git/go-git/v6/storage"

	"github.com/dxc-internal/omnirepo/internal/metadata"
)

// ProductionLoader resolves /git/<project>/<repo>.git URLs against the
// metadata store and returns a storage.Storer rooted at the bare repo
// under DataRoot.
//
// It delegates to transport.NewFilesystemLoader for the actual filesystem
// open so go-git handles the .git vs bare detection, HEAD reading, and
// object-layer wiring. We just compute the absolute path.
type ProductionLoader struct {
	DataRoot string
	Projects *metadata.ProjectsRepo
	Repos    *metadata.ReposRepo
}

// NewProductionLoader constructs a loader rooted at dataRoot.
func NewProductionLoader(dataRoot string, projects *metadata.ProjectsRepo, repos *metadata.ReposRepo) *ProductionLoader {
	return &ProductionLoader{
		DataRoot: dataRoot,
		Projects: projects,
		Repos:    repos,
	}
}

// Load implements transport.Loader. URL path must be of the shape
// "/git/<project>/<repo>.git" or "<project>/<repo>.git" (leading slash
// optional). Anything else returns transport.ErrRepositoryNotFound.
//
// Security (T-04-08-01): the filesystem path is derived from the DB row
// (project.Name + repo.Name), NEVER by concatenating the client URL.
// Project and repo name validators (Phase 1) reject path-traversal slugs
// at creation time.
func (l *ProductionLoader) Load(u *url.URL) (storage.Storer, error) {
	proj, repoName, err := parseRepoURL(u.Path)
	if err != nil {
		return nil, err
	}
	ctx := context.Background()
	p, err := l.Projects.FindByName(ctx, proj)
	if err != nil {
		return nil, transport.ErrRepositoryNotFound
	}
	r, err := l.Repos.FindByTriple(ctx, p.ID, "git", repoName)
	if err != nil {
		return nil, transport.ErrRepositoryNotFound
	}
	// Absolute path: <DataRoot>/repos/<project>/git/<repo>.git
	path := filepath.Join(l.DataRoot, "repos", p.Name, "git", r.Name+".git")
	fs := osfs.New("")
	return transport.NewFilesystemLoader(fs, true).Load(&url.URL{Path: path})
}

// ResolveRepoPath returns the absolute on-disk path for a (project, repo)
// pair. Exported so Plan 10 middleware can resolve the repoPath it needs
// to pass to GitServer.Handler without re-doing the filesystem join logic.
func (l *ProductionLoader) ResolveRepoPath(projectName, repoName string) string {
	return filepath.Join(l.DataRoot, "repos", projectName, "git", repoName+".git")
}

// parseRepoURL extracts (project, repo) from "/git/<project>/<repo>.git"
// or "<project>/<repo>.git" (leading slash + optional /git prefix).
func parseRepoURL(p string) (project, repo string, err error) {
	p = strings.TrimPrefix(p, "/")
	p = strings.TrimPrefix(p, "git/")
	if !strings.HasSuffix(p, ".git") {
		return "", "", fmt.Errorf("git URL missing .git suffix: %q", p)
	}
	p = strings.TrimSuffix(p, ".git")
	parts := strings.Split(p, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", errors.New("git URL must be <project>/<repo>.git")
	}
	return parts[0], parts[1], nil
}

// Compile-time assertion.
var _ transport.Loader = (*ProductionLoader)(nil)
