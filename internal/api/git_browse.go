// Package api — git browse API (Phase 05-04, D-11, D-32).
//
// Endpoints use go-git v6 to walk bare repositories on disk. All
// endpoints require project membership.
//
// GET /api/v1/projects/{name}/repos/git/{repo}/refs
// GET /api/v1/projects/{name}/repos/git/{repo}/tree/{ref}/{path...}
// GET /api/v1/projects/{name}/repos/git/{repo}/blob/{ref}/{path...}
// GET /api/v1/projects/{name}/repos/git/{repo}/commits/{ref}
// GET /api/v1/projects/{name}/repos/git/{repo}/commit/{sha}
// GET /api/v1/projects/{name}/repos/git/{repo}/blame/{ref}/{path...}
// GET /api/v1/projects/{name}/repos/git/{repo}/compare/{base}...{head}
package api

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"
	gogitpkg "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"

	"github.com/dxc-internal/omnirepo/internal/auth"
)

// errStopWalk halts an object walk once the caller has collected enough
// results. It is returned from ForEach callbacks and matched with
// errors.Is so real iterator errors are never silently swallowed.
var errStopWalk = errors.New("git-browse: stop walk")

// maxBlobSize is the maximum file size served by the blob endpoint (T-05-04-02).
const maxBlobSize = 5 * 1024 * 1024 // 5 MB

// maxBlameSize is the maximum file size for blame (T-05-04-02).
const maxBlameSize = 1 * 1024 * 1024 // 1 MB

// mountGitBrowse installs the git browse endpoints.
func (d Deps) mountGitBrowse(r chi.Router) {
	r.Get("/projects/{name}/repos/git/{repo}/refs", d.handleGitRefs)
	r.Get("/projects/{name}/repos/git/{repo}/tree/{ref}/*", d.handleGitTree)
	r.Get("/projects/{name}/repos/git/{repo}/tree/{ref}", d.handleGitTree)
	r.Get("/projects/{name}/repos/git/{repo}/blob/{ref}/*", d.handleGitBlob)
	r.Get("/projects/{name}/repos/git/{repo}/blob/{ref}", d.handleGitBlob)
	r.Get("/projects/{name}/repos/git/{repo}/commits/{ref}", d.handleGitCommits)
	r.Get("/projects/{name}/repos/git/{repo}/commit/{sha}", d.handleGitCommit)
	r.Get("/projects/{name}/repos/git/{repo}/blame/{ref}/*", d.handleGitBlame)
	r.Get("/projects/{name}/repos/git/{repo}/compare/{spec}", d.handleGitCompare)
}

// resolveGitRepo validates project membership and returns the opened go-git repo.
func (d Deps) resolveGitRepo(w http.ResponseWriter, r *http.Request) (*gogitpkg.Repository, bool) {
	actor, ok := auth.ActorFromContext(r.Context())
	if !ok {
		writeJSONError(w, r, http.StatusUnauthorized, ErrUnauthenticated, "")
		return nil, false
	}

	projectName := chi.URLParam(r, "name")
	repoName := chi.URLParam(r, "repo")

	p, err := d.Projects.FindByName(r.Context(), projectName)
	if err != nil {
		writeJSONError(w, r, http.StatusNotFound, ErrNotFound, "project not found")
		return nil, false
	}

	if !d.actorIsProjectMember(r.Context(), actor, p.ID) {
		writeJSONError(w, r, http.StatusForbidden, ErrForbidden, "not a project member")
		return nil, false
	}

	rr, err := d.Repos.FindByTriple(r.Context(), p.ID, "git", repoName)
	if err != nil {
		writeJSONError(w, r, http.StatusNotFound, ErrNotFound, "repo not found")
		return nil, false
	}
	_ = rr

	repoPath := filepath.Join(d.DataRoot, "repos", projectName, "git", repoName+".git")
	repo, err := gogitpkg.PlainOpen(repoPath)
	if err != nil {
		writeJSONError(w, r, http.StatusNotFound, ErrNotFound, "git repo not accessible")
		return nil, false
	}

	return repo, true
}

// resolveRef resolves a ref name (branch, tag, or SHA) to a commit hash.
func resolveRef(repo *gogitpkg.Repository, ref string) (*plumbing.Hash, error) {
	// Try as a full reference first.
	for _, prefix := range []string{"refs/heads/", "refs/tags/", ""} {
		refName := plumbing.ReferenceName(prefix + ref)
		r, err := repo.Storer.Reference(refName)
		if err == nil {
			h := r.Hash()
			return &h, nil
		}
	}

	// Try as a raw hash.
	h := plumbing.NewHash(ref)
	if h.IsZero() {
		return nil, fmt.Errorf("ref %q not found", ref)
	}
	return &h, nil
}

func (d Deps) handleGitRefs(w http.ResponseWriter, r *http.Request) {
	actor, ok := auth.ActorFromContext(r.Context())
	if !ok {
		writeJSONError(w, r, http.StatusUnauthorized, ErrUnauthenticated, "")
		return
	}

	projectName := chi.URLParam(r, "name")
	repoName := chi.URLParam(r, "repo")

	p, err := d.Projects.FindByName(r.Context(), projectName)
	if err != nil {
		writeJSONError(w, r, http.StatusNotFound, ErrNotFound, "project not found")
		return
	}
	if !d.actorIsProjectMember(r.Context(), actor, p.ID) {
		writeJSONError(w, r, http.StatusForbidden, ErrForbidden, "not a project member")
		return
	}
	rr, err := d.Repos.FindByTriple(r.Context(), p.ID, "git", repoName)
	if err != nil {
		writeJSONError(w, r, http.StatusNotFound, ErrNotFound, "repo not found")
		return
	}

	// Query from git_refs table (populated by post-ReceivePack hook). The
	// table stores every ref including the symbolic HEAD pointer; the
	// OpenAPI contract for this endpoint (GitRef schema) only defines
	// branch/tag entries with a `sha` field, so we both filter out
	// `symbolic` rows AND emit the target column under the `sha` name so
	// the wire shape matches the spec (previously the handler emitted
	// `target` and included HEAD, which broke the React git detail page
	// with `TypeError: Cannot read properties of undefined (reading
	// 'slice')`).
	gitRefs := d.DB.Reader
	rows, err := gitRefs.QueryContext(r.Context(), `
		SELECT name, target, type FROM git_refs
		WHERE repo_id=? AND type IN ('branch','tag')
		ORDER BY name
	`, rr.ID)
	if err != nil {
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
		return
	}
	defer func() { _ = rows.Close() }()

	type refItem struct {
		Name string `json:"name"`
		Type string `json:"type"`
		SHA  string `json:"sha"`
	}
	items := make([]refItem, 0)
	for rows.Next() {
		var item refItem
		if err := rows.Scan(&item.Name, &item.SHA, &item.Type); err != nil {
			writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
			return
		}
		items = append(items, item)
	}

	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (d Deps) handleGitTree(w http.ResponseWriter, r *http.Request) {
	repo, ok := d.resolveGitRepo(w, r)
	if !ok {
		return
	}

	ref := chi.URLParam(r, "ref")
	pathParam := chi.URLParam(r, "*")

	hash, err := resolveRef(repo, ref)
	if err != nil {
		writeJSONError(w, r, http.StatusNotFound, ErrNotFound, "ref not found")
		return
	}

	commit, err := repo.CommitObject(*hash)
	if err != nil {
		writeJSONError(w, r, http.StatusNotFound, ErrNotFound, "commit not found")
		return
	}

	tree, err := commit.Tree()
	if err != nil {
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
		return
	}

	// Navigate to subdirectory if path specified.
	if pathParam != "" {
		tree, err = tree.Tree(pathParam)
		if err != nil {
			writeJSONError(w, r, http.StatusNotFound, ErrNotFound, "path not found")
			return
		}
	}

	type treeEntry struct {
		Name string `json:"name"`
		Type string `json:"type"` // "file" or "dir"
		Size int64  `json:"size"`
	}

	entries := make([]treeEntry, 0, len(tree.Entries))
	for _, e := range tree.Entries {
		typ := "file"
		if e.Mode.IsFile() {
			typ = "file"
		} else {
			typ = "dir"
		}
		var size int64
		if typ == "file" {
			if blob, err := repo.BlobObject(e.Hash); err == nil {
				size = blob.Size
			}
		}
		entries = append(entries, treeEntry{
			Name: e.Name,
			Type: typ,
			Size: size,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{"items": entries})
}

func (d Deps) handleGitBlob(w http.ResponseWriter, r *http.Request) {
	repo, ok := d.resolveGitRepo(w, r)
	if !ok {
		return
	}

	ref := chi.URLParam(r, "ref")
	pathParam := chi.URLParam(r, "*")
	if pathParam == "" {
		writeJSONError(w, r, http.StatusBadRequest, ErrValidationFailed, "path required")
		return
	}

	hash, err := resolveRef(repo, ref)
	if err != nil {
		writeJSONError(w, r, http.StatusNotFound, ErrNotFound, "ref not found")
		return
	}

	commit, err := repo.CommitObject(*hash)
	if err != nil {
		writeJSONError(w, r, http.StatusNotFound, ErrNotFound, "commit not found")
		return
	}

	file, err := commit.File(pathParam)
	if err != nil {
		writeJSONError(w, r, http.StatusNotFound, ErrNotFound, "file not found")
		return
	}

	if file.Size > maxBlobSize {
		writeJSONError(w, r, http.StatusRequestEntityTooLarge, "too_large",
			fmt.Sprintf("file exceeds %d byte limit", maxBlobSize))
		return
	}

	reader, err := file.Reader()
	if err != nil {
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
		return
	}
	defer func() { _ = reader.Close() }()

	content, err := io.ReadAll(reader)
	if err != nil {
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
		return
	}

	// Detect binary by checking for null bytes in first 8KB.
	isBinary := false
	checkLen := len(content)
	if checkLen > 8192 {
		checkLen = 8192
	}
	for _, b := range content[:checkLen] {
		if b == 0 {
			isBinary = true
			break
		}
	}

	resp := map[string]any{
		"name":      file.Name,
		"size":      file.Size,
		"is_binary": isBinary,
	}
	if isBinary {
		resp["content"] = base64.StdEncoding.EncodeToString(content)
		resp["encoding"] = "base64"
	} else {
		resp["content"] = string(content)
		resp["encoding"] = "utf-8"
	}

	writeJSON(w, http.StatusOK, resp)
}

func (d Deps) handleGitCommits(w http.ResponseWriter, r *http.Request) {
	repo, ok := d.resolveGitRepo(w, r)
	if !ok {
		return
	}

	ref := chi.URLParam(r, "ref")
	hash, err := resolveRef(repo, ref)
	if err != nil {
		writeJSONError(w, r, http.StatusNotFound, ErrNotFound, "ref not found")
		return
	}

	pp := ParsePaginationParams(r)
	cursor := r.URL.Query().Get("cursor")

	iter, err := repo.Log(&gogitpkg.LogOptions{From: *hash})
	if err != nil {
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "")
		return
	}

	type commitItem struct {
		SHA     string `json:"sha"`
		Author  string `json:"author"`
		Email   string `json:"email"`
		Date    string `json:"date"`
		Message string `json:"message"`
	}

	// If cursor is set, skip commits until we pass the cursor SHA.
	pastCursor := cursor == ""
	items := make([]commitItem, 0, pp.Limit)
	var nextCursor string
	if err := iter.ForEach(func(c *object.Commit) error {
		if !pastCursor {
			if c.Hash.String() == cursor {
				pastCursor = true
			}
			return nil // skip
		}
		if len(items) >= pp.Limit {
			// We have one extra — use it as the next cursor indicator.
			nextCursor = c.Hash.String()
			return errStopWalk
		}
		items = append(items, commitItem{
			SHA:     c.Hash.String(),
			Author:  c.Author.Name,
			Email:   c.Author.Email,
			Date:    c.Author.When.UTC().Format("2006-01-02T15:04:05Z"),
			Message: strings.TrimSpace(c.Message),
		})
		return nil
	}); err != nil && !errors.Is(err, errStopWalk) {
		writeJSONError(w, r, http.StatusInternalServerError, "git.commits_walk_failed", "Failed to walk commits.")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": nextCursor})
}

func (d Deps) handleGitCommit(w http.ResponseWriter, r *http.Request) {
	repo, ok := d.resolveGitRepo(w, r)
	if !ok {
		return
	}

	sha := chi.URLParam(r, "sha")
	h := plumbing.NewHash(sha)
	if h.IsZero() {
		writeJSONError(w, r, http.StatusBadRequest, ErrValidationFailed, "invalid sha")
		return
	}

	commit, err := repo.CommitObject(h)
	if err != nil {
		writeJSONError(w, r, http.StatusNotFound, ErrNotFound, "commit not found")
		return
	}

	type fileChange struct {
		Name   string `json:"name"`
		Action string `json:"action"` // "add", "modify", "delete"
	}

	// Get changed files via parent diff.
	changes := make([]fileChange, 0)
	if commit.NumParents() > 0 {
		parent, err := commit.Parent(0)
		if err == nil {
			parentTree, _ := parent.Tree()
			commitTree, _ := commit.Tree()
			if parentTree != nil && commitTree != nil {
				diff, _ := parentTree.Diff(commitTree)
				for _, ch := range diff {
					action := "modify"
					name := ch.To.Name
					if ch.From.Name == "" {
						action = "add"
					} else if ch.To.Name == "" {
						action = "delete"
						name = ch.From.Name
					}
					changes = append(changes, fileChange{Name: name, Action: action})
				}
			}
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"sha":     commit.Hash.String(),
		"author":  commit.Author.Name,
		"email":   commit.Author.Email,
		"date":    commit.Author.When.UTC().Format("2006-01-02T15:04:05Z"),
		"message": strings.TrimSpace(commit.Message),
		"changes": changes,
	})
}

func (d Deps) handleGitBlame(w http.ResponseWriter, r *http.Request) {
	repo, ok := d.resolveGitRepo(w, r)
	if !ok {
		return
	}

	ref := chi.URLParam(r, "ref")
	pathParam := chi.URLParam(r, "*")
	if pathParam == "" {
		writeJSONError(w, r, http.StatusBadRequest, ErrValidationFailed, "path required")
		return
	}

	hash, err := resolveRef(repo, ref)
	if err != nil {
		writeJSONError(w, r, http.StatusNotFound, ErrNotFound, "ref not found")
		return
	}

	commit, err := repo.CommitObject(*hash)
	if err != nil {
		writeJSONError(w, r, http.StatusNotFound, ErrNotFound, "commit not found")
		return
	}

	// Check file size for blame limit.
	file, err := commit.File(pathParam)
	if err != nil {
		writeJSONError(w, r, http.StatusNotFound, ErrNotFound, "file not found")
		return
	}
	if file.Size > maxBlameSize {
		writeJSONError(w, r, http.StatusRequestEntityTooLarge, "too_large",
			fmt.Sprintf("file exceeds %d byte blame limit", maxBlameSize))
		return
	}

	result, err := gogitpkg.Blame(commit, pathParam)
	if err != nil {
		writeJSONError(w, r, http.StatusInternalServerError, ErrInternal, "blame failed")
		return
	}

	type blameLine struct {
		SHA    string `json:"sha"`
		Author string `json:"author"`
		Date   string `json:"date"`
		Line   string `json:"line"`
	}

	lines := make([]blameLine, 0, len(result.Lines))
	for _, l := range result.Lines {
		lines = append(lines, blameLine{
			SHA:    l.Hash.String(),
			Author: l.Author,
			Date:   l.Date.UTC().Format("2006-01-02T15:04:05Z"),
			Line:   l.Text,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{"lines": lines})
}

func (d Deps) handleGitCompare(w http.ResponseWriter, r *http.Request) {
	repo, ok := d.resolveGitRepo(w, r)
	if !ok {
		return
	}

	spec := chi.URLParam(r, "spec")
	parts := strings.SplitN(spec, "...", 2)
	if len(parts) != 2 {
		writeJSONError(w, r, http.StatusBadRequest, ErrValidationFailed, "spec must be base...head")
		return
	}

	baseHash, err := resolveRef(repo, parts[0])
	if err != nil {
		writeJSONError(w, r, http.StatusNotFound, ErrNotFound, "base ref not found")
		return
	}
	headHash, err := resolveRef(repo, parts[1])
	if err != nil {
		writeJSONError(w, r, http.StatusNotFound, ErrNotFound, "head ref not found")
		return
	}

	baseCommit, err := repo.CommitObject(*baseHash)
	if err != nil {
		writeJSONError(w, r, http.StatusNotFound, ErrNotFound, "base commit not found")
		return
	}
	headCommit, err := repo.CommitObject(*headHash)
	if err != nil {
		writeJSONError(w, r, http.StatusNotFound, ErrNotFound, "head commit not found")
		return
	}

	baseTree, _ := baseCommit.Tree()
	headTree, _ := headCommit.Tree()

	type diffEntry struct {
		Name   string `json:"name"`
		Action string `json:"action"`
	}

	diffs := make([]diffEntry, 0)
	if baseTree != nil && headTree != nil {
		changes, err := baseTree.Diff(headTree)
		if err == nil {
			for _, ch := range changes {
				action := "modify"
				name := ch.To.Name
				if ch.From.Name == "" {
					action = "add"
				} else if ch.To.Name == "" {
					action = "delete"
					name = ch.From.Name
				}
				diffs = append(diffs, diffEntry{Name: name, Action: action})
			}
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"base":    baseCommit.Hash.String(),
		"head":    headCommit.Hash.String(),
		"changes": diffs,
	})
}
