// Package git — Plan 11-06 (GITMIRROR-02/-05/-06/-08) git mirror sync
// handler.
//
// Mirrors an external Git repository via go-git v6 over HTTPS + Basic auth.
//
//   - First-sync:  gogit.PlainOpen(repoPath) returns ErrRepositoryNotExists
//     ⇒ gogit.PlainCloneContext with Mirror:true (which forces Bare:true
//     internally) using the shared *http.Client configured via
//     client.WithHTTPClient (Pitfall E: CA bundle / proxy / TLS live on
//     the shared client — client.WithInsecureSkipTLS / WithCABundle /
//     WithProxyURL are no-ops when WithHTTPClient is set).
//   - Re-sync:     repo.FetchContext with Force:true + Prune:true +
//     Tags:plumbing.AllTags. NoErrAlreadyUpToDate is NOT a failure — we
//     still walk refs and count zero updates.
//   - Post-fetch:  walk refs via repo.References() → []metadata.GitRef,
//     atomically rewrite git_refs under one writer tx via ReplaceAllTx
//     (GITMIRROR-06). Diff against prior rows to compute refs_updated.
//   - LFS detect:  scan HEAD tree for .gitattributes blobs containing
//     "filter=lfs"; emit EvtMirrorSyncLFSDetected audit-only (D-08).
//   - Audit:       EvtSyncStarted at the top; EvtSyncFinished with
//     refs_updated + objects_received + duration_ms at the bottom;
//     EvtSyncFailed via h.fail on any hard error.
//   - Progress:    step-based per D-10 ("cloning"|"fetching"|"pruning_refs"
//     |"regenerating_index"|"done"), total_bytes always 0 since go-git
//     does not surface a reliable total.
//
// D-07 locks "all refs, no filter" — SyncPayload has no Filter field.
// D-08 locks audit-only LFS surfacing — no UI badge in v1.4.
//
// Threat register mitigations:
//   - T-11-06-01 (cred leak via config): client.WithHTTPAuth does NOT embed
//     :password@ in the stored remote URL; integration test grep-asserts
//     this post-clone.
//   - T-11-06-04 (error leak):            httpx.SanitizeUpstreamErr wraps
//     every go-git return path before it reaches the caller.
//   - T-11-06-05 (hostile upstream cert): CA bundle configured on the
//     shared *http.Client transport by the app wiring, not here.
package git

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	gogit "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/client"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/go-git/go-git/v6/plumbing/storer"
	gogithttp "github.com/go-git/go-git/v6/plumbing/transport/http"

	"github.com/dxc-internal/omnirepo/internal/audit"
	"github.com/dxc-internal/omnirepo/internal/config"
	"github.com/dxc-internal/omnirepo/internal/httpx"
	"github.com/dxc-internal/omnirepo/internal/jobs"
	"github.com/dxc-internal/omnirepo/internal/metadata"
)

// SyncJobKind is the sync_jobs.kind value routed to SyncHandler.Handle.
// Plan 11-05 registered this string in the /sync allow-list; plan 11-06
// wires it to the handler below via phase3_sync.wireSync (Task 2c).
const SyncJobKind = "git_sync"

// SyncPayload is the JSON shape stored on the sync_jobs row for git_sync.
// Per D-07 there is NO Filter — git mirrors always pull every ref.
// SyncPayload.CredID is optional; anonymous upstreams (public GitHub HTTPS)
// work without a credential.
type SyncPayload struct {
	UpstreamURL string `json:"upstream_url"`
	CredID      *int64 `json:"cred_id,omitempty"`
}

// SyncDeps bundles the handler's dependencies. All fields are required
// unless documented otherwise.
type SyncDeps struct {
	DB       *metadata.DB
	Repos    *metadata.ReposRepo
	Projects *metadata.ProjectsRepo
	Refs     *metadata.GitRefsRepo
	Creds    *metadata.UpstreamCredsRepo
	Audit    audit.Logger
	// HTTPClient is the shared http.Client the app wires; it carries
	// TLS/CA bundle/proxy/timeout. Pitfall E: configure those on THIS
	// client — the go-git client.WithCABundle / WithInsecureSkipTLS /
	// WithProxyURL options are ignored once WithHTTPClient is set.
	HTTPClient *http.Client
	DataRoot   string
	Cfg        config.SyncConfig
	// SyncJobs supplies the progress writer sink. Nil in unit tests that
	// don't exercise progress.
	SyncJobs *metadata.SyncJobsRepo
}

// SyncHandler is the sync-pool handler for kind="git_sync".
type SyncHandler struct{ deps SyncDeps }

// NewSyncHandler constructs a handler. The returned value is safe for
// concurrent use by the sync pool — it holds no mutable per-run state.
func NewSyncHandler(d SyncDeps) *SyncHandler { return &SyncHandler{deps: d} }

// Handle runs one git_sync job. The payload argument is the JSON
// SyncPayload stored on the sync_jobs row. Errors returned here propagate
// to the pool retry/terminal-failed logic per the existing sync-kind
// contract used by helm/rpm/deb/pypi handlers.
//
// The overall shape intentionally mirrors helm.SyncHandler.Handle so
// operators reviewing audit events see consistent field names +
// EvtSyncStarted/Finished/Failed taxonomy across protocols.
func (h *SyncHandler) Handle(ctx context.Context, payload string, projectID, repoID, jobID int64) error {
	// Respect the sync-config upstream timeout; same default as helm.
	timeout := h.deps.Cfg.UpstreamHTTPTimeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	// Clone/fetch over a large repo can take longer than a single HTTP
	// request; we still cap it rather than leak an unbounded goroutine.
	ctx, cancel := context.WithTimeout(ctx, timeout*10)
	defer cancel()

	startedAt := time.Now()

	var pl SyncPayload
	if err := json.Unmarshal([]byte(payload), &pl); err != nil {
		return h.fail(ctx, repoID, pl, startedAt, fmt.Errorf("git_sync: payload: %w", err))
	}
	if pl.UpstreamURL == "" {
		return h.fail(ctx, repoID, pl, startedAt, errors.New("git_sync: upstream_url required"))
	}

	repo, err := h.deps.Repos.FindByID(ctx, repoID)
	if err != nil || repo == nil {
		return h.fail(ctx, repoID, pl, startedAt, fmt.Errorf("git_sync: load repo %d: %w", repoID, err))
	}
	proj, err := h.deps.Projects.FindByID(ctx, repo.ProjectID)
	if err != nil || proj == nil {
		return h.fail(ctx, repoID, pl, startedAt, fmt.Errorf("git_sync: load project %d: %w", repo.ProjectID, err))
	}

	// Credential decrypt + Authorizer construction. Anonymous upstreams
	// skip this whole block — pl.CredID is nil for public repos.
	var authMethod *gogithttp.BasicAuth
	if pl.CredID != nil {
		user, pw, _, _, lerr := h.deps.Creds.Lookup(ctx, projectID, *pl.CredID)
		if lerr != nil {
			return h.fail(ctx, repoID, pl, startedAt, httpx.SanitizeUpstreamErr(fmt.Errorf("git_sync: cred lookup: %w", lerr)))
		}
		// GITMIRROR-05: HTTPS+PAT only — the password field carries the
		// personal access token for GitHub/GitLab/Bitbucket. Empty user is
		// acceptable for some PAT-only providers, but we still record a
		// BasicAuth{} so the transport sends the Authorization header.
		authMethod = &gogithttp.BasicAuth{Username: user, Password: pw}
		if h.deps.Audit != nil {
			_ = h.deps.Audit.Record(ctx, audit.Event{
				Kind:       audit.EvtUpstreamCredUsed,
				TargetKind: "upstream_cred",
				TargetID:   strconv.FormatInt(*pl.CredID, 10),
				Details: map[string]any{
					"cred_id":      *pl.CredID,
					"upstream_url": pl.UpstreamURL,
					"job_kind":     SyncJobKind,
				},
			})
		}
	}

	if h.deps.Audit != nil {
		_ = h.deps.Audit.Record(ctx, audit.Event{
			Kind:       audit.EvtSyncStarted,
			TargetKind: "repo",
			TargetID:   strconv.FormatInt(repoID, 10),
			Details: map[string]any{
				"upstream_url": pl.UpstreamURL,
				"job_kind":     SyncJobKind,
				"cred_id":      pl.CredID,
			},
		})
	}

	// Pass an explicit nil to NewProgressWriter when SyncJobs is unwired
	// (helm/rpm/etc. always wire it in production; tests that don't
	// exercise progress leave it nil). This avoids the typed-nil
	// interface trap — passing (*metadata.SyncJobsRepo)(nil) to an
	// interface parameter yields a non-nil interface value, which would
	// later panic inside SyncJobsRepo.SetProgress's pointer dereference.
	var progressRepo jobs.SyncProgressRepo
	if h.deps.SyncJobs != nil {
		progressRepo = h.deps.SyncJobs
	}
	progress := jobs.NewProgressWriter(progressRepo, jobID)
	defer func() { _ = progress.Flush(ctx) }()

	repoPath := filepath.Join(h.deps.DataRoot, "repos", proj.Name, "git", repo.Name+".git")

	// Shared go-git transport options.
	clientOpts := []client.Option{client.WithHTTPClient(h.deps.HTTPClient)}
	if authMethod != nil {
		clientOpts = append(clientOpts, client.WithHTTPAuth(authMethod))
	}

	sink := &gitProgressSink{pw: progress, ctx: ctx, step: "cloning"}

	// First-sync detection.
	var gitRepo *gogit.Repository
	opened, openErr := gogit.PlainOpen(repoPath)
	switch {
	case openErr == nil:
		// Re-sync: repo already exists.
		sink.step = "fetching"
		_ = progress.Set(ctx, "fetching", 0, 0)
		gitRepo = opened
		ferr := gitRepo.FetchContext(ctx, &gogit.FetchOptions{
			RemoteName:    "origin",
			ClientOptions: clientOpts,
			Progress:      sink,
			Force:         true,
			Prune:         true,
			Tags:          plumbing.AllTags,
		})
		// NoErrAlreadyUpToDate is NOT a failure — we still walk refs.
		if ferr != nil && !errors.Is(ferr, gogit.NoErrAlreadyUpToDate) {
			return h.fail(ctx, repoID, pl, startedAt, httpx.SanitizeUpstreamErr(ferr))
		}
	case errors.Is(openErr, gogit.ErrRepositoryNotExists):
		// First-sync: ensure parent dir exists (PlainInit inside
		// PlainCloneContext only creates the leaf), then clone.
		if err := os.MkdirAll(filepath.Dir(repoPath), 0o755); err != nil {
			return h.fail(ctx, repoID, pl, startedAt, fmt.Errorf("git_sync: mkdir parent: %w", err))
		}
		_ = progress.Set(ctx, "cloning", 0, 0)
		cloned, cerr := gogit.PlainCloneContext(ctx, repoPath, &gogit.CloneOptions{
			URL:           pl.UpstreamURL,
			Bare:          true,
			Mirror:        true,
			ClientOptions: clientOpts,
			Progress:      sink,
			Tags:          plumbing.AllTags,
		})
		if cerr != nil {
			return h.fail(ctx, repoID, pl, startedAt, httpx.SanitizeUpstreamErr(cerr))
		}
		gitRepo = cloned
	default:
		return h.fail(ctx, repoID, pl, startedAt, fmt.Errorf("git_sync: open bare repo %q: %w", repoPath, openErr))
	}

	// Post-fetch refs walk.
	_ = progress.Set(ctx, "pruning_refs", 0, 0)
	newRefs, walkErr := walkMirrorRefs(gitRepo)
	if walkErr != nil {
		return h.fail(ctx, repoID, pl, startedAt, fmt.Errorf("git_sync: walk refs: %w", walkErr))
	}

	// Diff against prior state for refs_updated.
	priorRefs, _ := h.deps.Refs.List(ctx, repoID)
	refsUpdated := diffRefCount(priorRefs, newRefs)

	// GITMIRROR-06: atomic refs rewrite under a single writer tx.
	_ = progress.Set(ctx, "regenerating_index", int64(len(newRefs)), 0)
	if werr := h.deps.DB.WriteTx(ctx, func(tx *sql.Tx) error {
		return h.deps.Refs.ReplaceAllTx(ctx, tx, repoID, newRefs)
	}); werr != nil {
		return h.fail(ctx, repoID, pl, startedAt, fmt.Errorf("git_sync: rewrite git_refs: %w", werr))
	}

	// LFS detection (D-08). Non-fatal — sync succeeds regardless.
	lfsPaths := detectLFSInHEAD(gitRepo, 10)
	if len(lfsPaths) > 0 && h.deps.Audit != nil {
		_ = h.deps.Audit.Record(ctx, audit.Event{
			Kind:       audit.EvtMirrorSyncLFSDetected,
			TargetKind: "repo",
			TargetID:   strconv.FormatInt(repoID, 10),
			Details: map[string]any{
				"repo_id":       repoID,
				"project":       proj.Name,
				"sample_paths":  lfsPaths,
				"upstream_url":  pl.UpstreamURL,
			},
		})
	}

	// Terminal progress flush so UI pollers see "done" even if the final
	// refs-rewrite throttle-suppressed the previous emit.
	_ = progress.Set(ctx, "done", int64(len(newRefs)), 0)

	// GITMIRROR-08: EvtSyncFinished with refs_updated + objects_received.
	if h.deps.Audit != nil {
		_ = h.deps.Audit.Record(ctx, audit.Event{
			Kind:       audit.EvtSyncFinished,
			TargetKind: "repo",
			TargetID:   strconv.FormatInt(repoID, 10),
			Details: map[string]any{
				"upstream_url":     pl.UpstreamURL,
				"job_kind":         SyncJobKind,
				"cred_id":          pl.CredID,
				"refs_updated":     refsUpdated,
				"objects_received": sink.objectsReceived,
				"duration_ms":      time.Since(startedAt).Milliseconds(),
			},
		})
	}
	return nil
}

// fail records EvtSyncFailed + returns the (sanitized) error for pool
// retry logic. Mirrors helm.SyncHandler.fail shape.
func (h *SyncHandler) fail(ctx context.Context, repoID int64, pl SyncPayload, startedAt time.Time, err error) error {
	if h.deps.Audit != nil {
		_ = h.deps.Audit.Record(ctx, audit.Event{
			Kind:       audit.EvtSyncFailed,
			TargetKind: "repo",
			TargetID:   strconv.FormatInt(repoID, 10),
			Details: map[string]any{
				"upstream_url": pl.UpstreamURL,
				"job_kind":     SyncJobKind,
				"last_error":   truncateGitErr(err.Error()),
				"duration_ms":  time.Since(startedAt).Milliseconds(),
			},
		})
	}
	return err
}

// walkMirrorRefs walks every reference in the bare repo and returns the
// equivalent metadata.GitRef slice. Ref type classification mirrors
// ClassifyRef (HEAD→symbolic, refs/heads/*→branch, refs/tags/*→tag,
// everything else → other) so receive-pack + mirror-sync produce
// schema-compatible rows.
func walkMirrorRefs(repo *gogit.Repository) ([]metadata.GitRef, error) {
	iter, err := repo.References()
	if err != nil {
		return nil, fmt.Errorf("iter references: %w", err)
	}
	defer iter.Close()

	seen := make(map[string]bool)
	var refs []metadata.GitRef
	err = iter.ForEach(func(r *plumbing.Reference) error {
		name := string(r.Name())
		seen[name] = true
		var target string
		switch r.Type() {
		case plumbing.HashReference:
			target = r.Hash().String()
		case plumbing.SymbolicReference:
			target = string(r.Target())
		default:
			return nil // skip invalid
		}
		refs = append(refs, metadata.GitRef{
			Name:   name,
			Target: target,
			Type:   metadata.GitRefType(ClassifyRef(name)),
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk refs: %w", err)
	}

	// Mirror the receive-pack walker's HEAD fallback: some storers skip
	// HEAD in IterReferences. Explicitly resolve once.
	if !seen["HEAD"] {
		if headRef, herr := repo.Storer.Reference(plumbing.HEAD); herr == nil && headRef != nil {
			var target string
			if headRef.Type() == plumbing.SymbolicReference {
				target = string(headRef.Target())
			} else {
				target = headRef.Hash().String()
			}
			refs = append(refs, metadata.GitRef{
				Name:   "HEAD",
				Target: target,
				Type:   metadata.GitRefSymbolic,
			})
		}
	}

	return refs, nil
}

// diffRefCount counts how many refs changed between prior and next. A ref
// is "changed" if it is newly present, newly absent, or still present but
// with a different target. Used for the refs_updated audit detail per
// GITMIRROR-08.
func diffRefCount(prior, next []metadata.GitRef) int {
	priorMap := make(map[string]string, len(prior))
	for _, r := range prior {
		priorMap[r.Name] = r.Target
	}
	seen := make(map[string]bool, len(next))
	changed := 0
	for _, r := range next {
		seen[r.Name] = true
		if oldTarget, ok := priorMap[r.Name]; !ok || oldTarget != r.Target {
			changed++
		}
	}
	for _, r := range prior {
		if !seen[r.Name] {
			changed++
		}
	}
	return changed
}

// detectLFSInHEAD walks the HEAD commit's tree for .gitattributes files
// containing filter=lfs. Returns up to maxSamples matching paths. Bounded
// cost: we short-circuit the tree walk via storer.ErrStop once maxSamples
// hits are collected.
//
// D-08: audit-only — no UI surfacing in v1.4. An empty return means either
// no LFS, no HEAD (e.g. empty mirror), or a transient read failure — all
// three are indistinguishable by design since detection is best-effort.
func detectLFSInHEAD(repo *gogit.Repository, maxSamples int) []string {
	if maxSamples <= 0 {
		return nil
	}
	head, err := repo.Head()
	if err != nil {
		return nil
	}
	commit, err := repo.CommitObject(head.Hash())
	if err != nil {
		return nil
	}
	tree, err := commit.Tree()
	if err != nil {
		return nil
	}
	var out []string
	_ = tree.Files().ForEach(func(f *object.File) error {
		if filepath.Base(f.Name) != ".gitattributes" {
			return nil
		}
		contents, ferr := f.Contents()
		if ferr != nil {
			return nil
		}
		if strings.Contains(contents, "filter=lfs") {
			out = append(out, f.Name)
			if len(out) >= maxSamples {
				return storer.ErrStop
			}
		}
		return nil
	})
	return out
}

// gitProgressSink implements sideband.Progress (io.Writer) and maps
// go-git's progress text ("Receiving objects: …", "Resolving deltas: …")
// onto the step-based progress convention per D-10. Best-effort parse —
// malformed lines are silently skipped.
//
// Why text-scrape rather than a first-class go-git stats API: go-git v6
// does not expose a FetchStats return value at the Repository.FetchContext
// boundary, and the clone path likewise returns only the *Repository +
// error. The sideband text is the only structured hook go-git gives
// callers for live progress (D-10 step-based is therefore "best-effort
// objects_received / bytes_received" on top of a coarse step label).
type gitProgressSink struct {
	pw              *jobs.ProgressWriter
	ctx             context.Context
	step            string
	objectsReceived int64
}

// Write is called by go-git's sideband demuxer with each progress line.
// We extract the "X/Y" pair from lines like
// "Receiving objects:  42% (500/1200), 1.2 MiB | 512.00 KiB/s" and emit
// a progress row with step="fetching" + done=X + total=0 (per D-10).
func (p *gitProgressSink) Write(b []byte) (int, error) {
	line := string(b)
	if i := strings.Index(line, "Receiving objects:"); i >= 0 {
		p.step = "fetching"
		if j := strings.Index(line[i:], "("); j >= 0 {
			rest := line[i+j+1:]
			if k := strings.Index(rest, "/"); k >= 0 {
				var got int64
				if _, err := fmt.Sscanf(rest[:k], "%d", &got); err == nil {
					p.objectsReceived = got
					_ = p.pw.Set(p.ctx, p.step, got, 0)
				}
			}
		}
	} else if i := strings.Index(line, "Resolving deltas:"); i >= 0 {
		p.step = "fetching"
		// Keep the currently-tracked objects_received; the deltas-resolve
		// phase happens after receiving so objectsReceived is stable.
		_ = p.pw.Set(p.ctx, p.step, p.objectsReceived, 0)
	}
	return len(b), nil
}

// truncateGitErr caps last_error at 1024 bytes (same budget as helm).
// Prevents a hostile upstream from stuffing multi-MB error strings into
// sync_jobs.last_error via a crafted 500 body.
func truncateGitErr(s string) string {
	const max = 1024
	if len(s) <= max {
		return s
	}
	return s[:max] + "...[truncated]"
}
