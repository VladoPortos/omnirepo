// Package api — git browse API.
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
	"github.com/go-git/go-git/v6/plumbing/filemode"
	"github.com/go-git/go-git/v6/plumbing/object"

	"github.com/vladoportos/omnirepo/internal/auth"
)

// errStopWalk halts an object walk once the caller has collected enough
// results. It is returned from ForEach callbacks and matched with
// errors.Is so real iterator errors are never silently swallowed.
var errStopWalk = errors.New("git-browse: stop walk")

// maxBlobSize is the maximum file size served by the blob endpoint.
const maxBlobSize = 5 * 1024 * 1024 // 5 MB

// maxBlameSize is the maximum file size for blame.
const maxBlameSize = 1 * 1024 * 1024 // 1 MB

// mountGitBrowse installs the git browse endpoints. Routes that
// previously captured `{ref}` as a single chi path segment now use a
// catch-all so branch/tag names containing `/` (e.g. `feature/x`,
// `release/v1.2`) route correctly. The handler calls splitRefAndPath
// to try progressively longer ref prefixes against the real repo refs.
func (d Deps) mountGitBrowse(r chi.Router) {
	r.Get("/projects/{name}/repos/git/{repo}/refs", d.handleGitRefs)
	r.Get("/projects/{name}/repos/git/{repo}/tree/*", d.handleGitTree)
	r.Get("/projects/{name}/repos/git/{repo}/blob/*", d.handleGitBlob)
	r.Get("/projects/{name}/repos/git/{repo}/commits/*", d.handleGitCommits)
	r.Get("/projects/{name}/repos/git/{repo}/commit/{sha}", d.handleGitCommit)
	r.Get("/projects/{name}/repos/git/{repo}/blame/*", d.handleGitBlame)
	r.Get("/projects/{name}/repos/git/{repo}/compare/{spec}", d.handleGitCompare)
}

// splitRefAndPath pulls a (ref, path) pair out of the chi "*" catch-all
// segment for handlers that take both a ref and an optional tree path.
// Branch/tag names may contain `/` (e.g. `feature/x`, `release/v1.2`), so
// a simple first-segment split is wrong. We iterate from the longest
// possible ref (the whole catch-all) down to the first segment and
// return the first split whose ref resolves in the repo. If none
// resolves we fall back to the shortest (first segment) split — the
// caller then returns a 404 with "ref not found" for better DX than
// returning a misleading path error.
func splitRefAndPath(repo *gogitpkg.Repository, catchAll string) (ref, path string) {
	catchAll = strings.Trim(catchAll, "/")
	if catchAll == "" {
		return "", ""
	}
	segments := strings.Split(catchAll, "/")
	for i := len(segments); i >= 1; i-- {
		candidate := strings.Join(segments[:i], "/")
		if _, err := resolveRef(repo, candidate); err == nil {
			rest := strings.Join(segments[i:], "/")
			return candidate, rest
		}
	}
	return segments[0], strings.Join(segments[1:], "/")
}

// refOnlyFromCatchAll returns the whole catch-all segment as the ref
// name (no path component). Used by handlers like /commits where the
// URL suffix is the ref itself.
func refOnlyFromCatchAll(catchAll string) string {
	return strings.Trim(catchAll, "/")
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
	// table stores every ref including the symbolic HEAD pointer and full
	// ref names ("refs/heads/main"); the OpenAPI contract for this
	// endpoint (GitRef schema) only defines branch/tag entries with a
	// `sha` field, and the `ref` path parameter on /tree, /blob, /commits
	// is a single URL segment, so we:
	//   1. Filter symbolic rows (HEAD) at SQL level — they are not in the
	//      documented enum and leak a ref-path value ("refs/heads/main")
	//      where the spec promises a SHA.
	//   2. Strip the `refs/heads/` / `refs/tags/` prefix from the emitted
	//      `name` so the client uses "main" / "v1.0" etc. as the path
	//      parameter (previously the client passed "refs/heads/main" as a
	//      multi-segment path which chi 404'd).
	//   3. Rename `target` → `sha` so the wire shape matches the spec.
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
		item.Name = shortRefName(item.Name, item.Type)
		items = append(items, item)
	}

	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// shortRefName strips the "refs/heads/" or "refs/tags/" prefix from a full
// ref name so clients can pass it back as a single URL path segment (chi
// doesn't route multi-segment parameters). Unknown prefixes are returned
// untouched so custom ref namespaces still surface usefully.
func shortRefName(full, kind string) string {
	switch kind {
	case "branch":
		return strings.TrimPrefix(full, "refs/heads/")
	case "tag":
		return strings.TrimPrefix(full, "refs/tags/")
	default:
		return full
	}
}

func (d Deps) handleGitTree(w http.ResponseWriter, r *http.Request) {
	repo, ok := d.resolveGitRepo(w, r)
	if !ok {
		return
	}

	ref, pathParam := splitRefAndPath(repo, chi.URLParam(r, "*"))

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
	prefix := strings.Trim(pathParam, "/")
	if prefix != "" {
		tree, err = tree.Tree(prefix)
		if err != nil {
			writeJSONError(w, r, http.StatusNotFound, ErrNotFound, "path not found")
			return
		}
	}

	// GitTreeEntry (OpenAPI): name, path, type (blob|tree|commit), size, sha.
	type treeEntry struct {
		Name string `json:"name"`
		Path string `json:"path"`
		Type string `json:"type"`
		Size int64  `json:"size"`
		SHA  string `json:"sha"`
	}

	entries := make([]treeEntry, 0, len(tree.Entries))
	for _, e := range tree.Entries {
		var typ string
		var size int64
		switch {
		case e.Mode.IsFile():
			typ = "blob"
			if blob, err := repo.BlobObject(e.Hash); err == nil {
				size = blob.Size
			}
		case e.Mode == filemode.Submodule:
			// Gitlinks are commit pointers to external repos.
			typ = "commit"
		default:
			typ = "tree"
		}
		childPath := e.Name
		if prefix != "" {
			childPath = prefix + "/" + e.Name
		}
		entries = append(entries, treeEntry{
			Name: e.Name,
			Path: childPath,
			Type: typ,
			Size: size,
			SHA:  e.Hash.String(),
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{"items": entries})
}

func (d Deps) handleGitBlob(w http.ResponseWriter, r *http.Request) {
	repo, ok := d.resolveGitRepo(w, r)
	if !ok {
		return
	}

	ref, pathParam := splitRefAndPath(repo, chi.URLParam(r, "*"))
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

	// GitFileContent (OpenAPI): name, path, sha, size, encoding, content.
	// Binary files are base64-encoded and the encoding field signals it;
	// text is returned as-is under encoding="utf-8".
	resp := map[string]any{
		"name":     file.Name,
		"path":     pathParam,
		"sha":      file.Hash.String(),
		"size":     file.Size,
		"encoding": "utf-8",
		"content":  string(content),
	}
	if isBinary {
		resp["encoding"] = "base64"
		resp["content"] = base64.StdEncoding.EncodeToString(content)
	}

	writeJSON(w, http.StatusOK, resp)
}

func (d Deps) handleGitCommits(w http.ResponseWriter, r *http.Request) {
	repo, ok := d.resolveGitRepo(w, r)
	if !ok {
		return
	}

	ref := refOnlyFromCatchAll(chi.URLParam(r, "*"))
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

	// GitCommit (OpenAPI): sha, message, author_*, committer_*, parent_shas.
	type commitItem struct {
		SHA            string   `json:"sha"`
		Message        string   `json:"message"`
		AuthorName     string   `json:"author_name"`
		AuthorEmail    string   `json:"author_email"`
		AuthorDate     string   `json:"author_date"`
		CommitterName  string   `json:"committer_name"`
		CommitterEmail string   `json:"committer_email"`
		CommitterDate  string   `json:"committer_date"`
		ParentSHAs     []string `json:"parent_shas"`
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
			SHA:            c.Hash.String(),
			Message:        strings.TrimSpace(c.Message),
			AuthorName:     c.Author.Name,
			AuthorEmail:    c.Author.Email,
			AuthorDate:     c.Author.When.UTC().Format("2006-01-02T15:04:05Z"),
			CommitterName:  c.Committer.Name,
			CommitterEmail: c.Committer.Email,
			CommitterDate:  c.Committer.When.UTC().Format("2006-01-02T15:04:05Z"),
			ParentSHAs:     parentSHAs(c),
		})
		return nil
	}); err != nil && !errors.Is(err, errStopWalk) {
		writeJSONError(w, r, http.StatusInternalServerError, "git.commits_walk_failed", "Failed to walk commits.")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": nextCursor})
}

// parentSHAs returns the commit's parent hashes as a non-nil []string so the
// JSON envelope always emits `"parent_shas": []` for root commits rather
// than a missing or null key — the TS interface declares it required.
func parentSHAs(c *object.Commit) []string {
	out := make([]string, 0, len(c.ParentHashes))
	for _, h := range c.ParentHashes {
		out = append(out, h.String())
	}
	return out
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

	// Build the GitDiff response: sha, message, stats, files[{path,status,patch}].
	// For root commits (no parent) we diff against an empty tree so every
	// file shows as added with its full content as a patch.
	var parentTree, commitTree *object.Tree
	if commit.NumParents() > 0 {
		if parent, perr := commit.Parent(0); perr == nil {
			parentTree, _ = parent.Tree()
		}
	}
	commitTree, _ = commit.Tree()

	files, stats := diffTrees(parentTree, commitTree)

	writeJSON(w, http.StatusOK, map[string]any{
		"sha":     commit.Hash.String(),
		"message": strings.TrimSpace(commit.Message),
		"stats":   stats,
		"files":   files,
	})
}

// diffFile mirrors the GitDiffFile schema in openapi.yaml:
//
//	{ path: string, status: string, patch: string }
//
// status values: "added" | "modified" | "deleted" | "renamed".
type diffFile struct {
	Path   string `json:"path"`
	Status string `json:"status"`
	Patch  string `json:"patch"`
}

// diffStats mirrors the inline GitDiff.stats object:
//
//	{ additions: int, deletions: int, files_changed: int }
type diffStats struct {
	Additions    int `json:"additions"`
	Deletions    int `json:"deletions"`
	FilesChanged int `json:"files_changed"`
}

// diffTrees returns the per-file GitDiff slice plus aggregate stats.
// Either side may be nil; a nil `from` means "diff against the empty
// tree" (root commit case) and a nil `to` means "delete everything"
// (unused in practice — callers always pass a commit tree as `to`).
func diffTrees(from, to *object.Tree) ([]diffFile, diffStats) {
	files := make([]diffFile, 0)
	var agg diffStats
	if to == nil {
		return files, agg
	}
	var changes object.Changes
	if from == nil {
		// Enumerate the whole target tree as additions.
		_ = to.Files().ForEach(func(f *object.File) error {
			patch, additions := blobAsAddPatch(f)
			files = append(files, diffFile{
				Path:   f.Name,
				Status: "added",
				Patch:  patch,
			})
			agg.Additions += additions
			agg.FilesChanged++
			return nil
		})
		return files, agg
	}
	var err error
	changes, err = from.Diff(to)
	if err != nil {
		return files, agg
	}
	for _, ch := range changes {
		status := "modified"
		path := ch.To.Name
		switch {
		case ch.From.Name == "" && ch.To.Name != "":
			status = "added"
		case ch.To.Name == "" && ch.From.Name != "":
			status = "deleted"
			path = ch.From.Name
		case ch.From.Name != ch.To.Name:
			status = "renamed"
		}
		patch, adds, dels := changePatch(ch)
		files = append(files, diffFile{
			Path:   path,
			Status: status,
			Patch:  patch,
		})
		agg.Additions += adds
		agg.Deletions += dels
		agg.FilesChanged++
	}
	return files, agg
}

// changePatch renders a unified diff for one object.Change and returns
// (patch, additions, deletions). Errors degrade to an empty patch with
// zero stats — a missing patch is preferable to failing the whole
// commit-detail response.
func changePatch(ch *object.Change) (string, int, int) {
	p, err := ch.Patch()
	if err != nil || p == nil {
		return "", 0, 0
	}
	adds, dels := 0, 0
	for _, fp := range p.FilePatches() {
		for _, ch := range fp.Chunks() {
			switch ch.Type() {
			case 1: // Add
				adds += lineCount(ch.Content())
			case 2: // Delete
				dels += lineCount(ch.Content())
			}
		}
	}
	return p.String(), adds, dels
}

// blobAsAddPatch renders a root-commit "all-adds" patch for a single file.
// Returns (patch, additions). Binary files emit a short marker rather
// than the blob body so the JSON payload stays sane.
func blobAsAddPatch(f *object.File) (string, int) {
	bin, _ := f.IsBinary()
	if bin {
		return fmt.Sprintf("Binary files /dev/null and b/%s differ\n", f.Name), 0
	}
	contents, err := f.Contents()
	if err != nil {
		return "", 0
	}
	var b strings.Builder
	fmt.Fprintf(&b, "diff --git a/%s b/%s\n", f.Name, f.Name)
	b.WriteString("new file\n")
	b.WriteString("--- /dev/null\n")
	fmt.Fprintf(&b, "+++ b/%s\n", f.Name)
	lines := strings.Split(strings.TrimRight(contents, "\n"), "\n")
	fmt.Fprintf(&b, "@@ -0,0 +1,%d @@\n", len(lines))
	adds := 0
	for _, ln := range lines {
		b.WriteString("+")
		b.WriteString(ln)
		b.WriteString("\n")
		adds++
	}
	return b.String(), adds
}

// lineCount returns the number of \n-delimited lines in s. Empty strings
// count as 0; a trailing newline does not count an extra empty line.
func lineCount(s string) int {
	if s == "" {
		return 0
	}
	n := strings.Count(s, "\n")
	if !strings.HasSuffix(s, "\n") {
		n++
	}
	return n
}

func (d Deps) handleGitBlame(w http.ResponseWriter, r *http.Request) {
	repo, ok := d.resolveGitRepo(w, r)
	if !ok {
		return
	}

	ref, pathParam := splitRefAndPath(repo, chi.URLParam(r, "*"))
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

	// GitBlame (OpenAPI): {path, lines:[{line_number, sha, author, date, content}]}.
	type blameLine struct {
		LineNumber int    `json:"line_number"`
		SHA        string `json:"sha"`
		Author     string `json:"author"`
		Date       string `json:"date"`
		Content    string `json:"content"`
	}

	lines := make([]blameLine, 0, len(result.Lines))
	for i, l := range result.Lines {
		lines = append(lines, blameLine{
			LineNumber: i + 1,
			SHA:        l.Hash.String(),
			Author:     l.AuthorName,
			Date:       l.Date.UTC().Format("2006-01-02T15:04:05Z"),
			Content:    l.Text,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"path":  pathParam,
		"lines": lines,
	})
}

func (d Deps) handleGitCompare(w http.ResponseWriter, r *http.Request) {
	repo, ok := d.resolveGitRepo(w, r)
	if !ok {
		return
	}

	// Accept both "base...head" (GitHub three-dot spelling) and the
	// legacy two-dot spelling. chi treats "..." and ".." the same in a
	// single URL segment, so we detect whichever form the client used.
	// Reject dot-runs of 4+ (e.g. "main....feature")
	// at the edge so they don't silently parse as a valid spec with a
	// bogus head like ".feature" and return a confusing 404.
	spec := chi.URLParam(r, "spec")
	var parts []string
	switch {
	case strings.Contains(spec, "...."):
		// Too many dots in a row — not a valid spec.
	case strings.Contains(spec, "..."):
		parts = strings.SplitN(spec, "...", 2)
	case strings.Contains(spec, ".."):
		parts = strings.SplitN(spec, "..", 2)
	}
	if len(parts) != 2 ||
		parts[0] == "" || parts[1] == "" ||
		strings.HasPrefix(parts[0], ".") || strings.HasPrefix(parts[1], ".") ||
		strings.HasSuffix(parts[0], ".") || strings.HasSuffix(parts[1], ".") {
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

	files, stats := diffTrees(baseTree, headTree)

	// Compare renders as a GitDiff (same schema as commit detail) with the
	// head commit's SHA and message so UIs can reuse the same component.
	writeJSON(w, http.StatusOK, map[string]any{
		"sha":     headCommit.Hash.String(),
		"message": strings.TrimSpace(headCommit.Message),
		"stats":   stats,
		"files":   files,
	})
}
