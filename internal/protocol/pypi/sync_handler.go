// Package pypi — Phase 03 Plan 06 SYNC-05 sync handler.
//
// PyPI idempotency uses FindByFilename (not FindByDigest) because the
// pypi_files schema constraint is UNIQUE(repo_id, filename) and PEP 691
// responses sometimes omit the SHA256 entirely (D-15). The handler does
// still verify the digest when upstream supplied one.
package pypi

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"sync/atomic"

	"github.com/dxc-internal/omnirepo/internal/audit"
	"github.com/dxc-internal/omnirepo/internal/config"
	"github.com/dxc-internal/omnirepo/internal/httpx"
	"github.com/dxc-internal/omnirepo/internal/jobs"
	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/protocol/regen"
	"github.com/dxc-internal/omnirepo/internal/storage"
)

// SyncJobKind is the sync_jobs.kind value routed to SyncHandler.Handle.
const SyncJobKind = "pypi_sync"

// SyncPayload is the job payload shape (D-13).
type SyncPayload struct {
	UpstreamURL string      `json:"upstream_url"`
	CredID      *int64      `json:"cred_id,omitempty"`
	Filter      *SyncFilter `json:"filter,omitempty"`
}

// SyncDeps bundles dependencies for the PyPI sync handler.
type SyncDeps struct {
	DB         *metadata.DB
	Path       storage.PathStore
	PyPIFiles  *metadata.PyPIFilesRepo
	Repos      *metadata.ReposRepo
	Projects   *metadata.ProjectsRepo
	Creds      *metadata.UpstreamCredsRepo
	Scans      *metadata.ScansRepo
	Audit      audit.Logger
	Coalescer  *regen.Registry
	HTTPClient *http.Client
	RepoRoot   string
	Cfg        config.SyncConfig
	// Phase 8 Plan 02 (M2.6): sync-jobs repo for throttled byte-level
	// progress emit. Nil-safe — if unwired, progress.Set is a no-op.
	SyncJobs *metadata.SyncJobsRepo
}

// SyncHandler is the sync-pool handler for kind="pypi_sync".
type SyncHandler struct{ deps SyncDeps }

// NewSyncHandler constructs a handler.
func NewSyncHandler(deps SyncDeps) *SyncHandler { return &SyncHandler{deps: deps} }

// Handle runs one pypi_sync job.
//
// Phase 8 Plan 02 / M2.6: jobID threads through so the handler can emit
// byte-level progress via jobs.ProgressWriter. totalBytes is pre-computed
// from summed PEP 691 file.size (D-11) so the progress bar has a stable
// denominator throughout the sync.
func (h *SyncHandler) Handle(ctx context.Context, payload string, projectID, repoID, jobID int64) error {
	timeout := h.deps.Cfg.UpstreamHTTPTimeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var pl SyncPayload
	if err := json.Unmarshal([]byte(payload), &pl); err != nil {
		return fmt.Errorf("pypi_sync: payload: %w", err)
	}
	if pl.UpstreamURL == "" {
		return fmt.Errorf("pypi_sync: upstream_url required")
	}
	if pl.Filter == nil {
		pl.Filter = &SyncFilter{}
	}

	repo, err := h.deps.Repos.FindByID(ctx, repoID)
	if err != nil || repo == nil {
		return fmt.Errorf("pypi_sync: repo %d: %w", repoID, err)
	}
	proj, err := h.deps.Projects.FindByID(ctx, repo.ProjectID)
	if err != nil || proj == nil {
		return fmt.Errorf("pypi_sync: project %d: %w", repo.ProjectID, err)
	}

	var creds AuthCreds
	if pl.CredID != nil {
		user, pw, tok, host, lerr := h.deps.Creds.Lookup(ctx, projectID, *pl.CredID)
		if lerr != nil {
			return httpx.SanitizeUpstreamErr(fmt.Errorf("pypi_sync: cred lookup: %w", lerr))
		}
		u, perr := url.Parse(pl.UpstreamURL)
		if perr != nil {
			return fmt.Errorf("pypi_sync: parse upstream_url: %w", perr)
		}
		if host != u.Host {
			return httpx.SanitizeUpstreamErr(
				fmt.Errorf("cred_host_mismatch: cred=%s upstream=%s", host, u.Host))
		}
		creds = AuthCreds{User: user, Password: pw, Token: tok}
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

	startedAt := time.Now()
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

	maxParallel := h.deps.Cfg.MaxParallelDownloadsPerJob
	if maxParallel < 1 {
		maxParallel = 4
	}

	projects, parseErr := ParseUpstreamSimpleIndex(ctx, h.deps.HTTPClient, pl.UpstreamURL, creds)
	if parseErr != nil {
		return h.fail(ctx, repoID, pl, startedAt, httpx.SanitizeUpstreamErr(parseErr))
	}

	// Phase 8 Plan 02 / M2.6: collect all project/file pairs first so we
	// can emit byte-level progress with a stable totalBytes denominator
	// (sum of PEP 691 file.size entries). Filter + idempotency checks run
	// in the collect pass so already-present files don't inflate total.
	type fileToFetch struct {
		project string
		file    UpstreamFile
	}
	var (
		toFetch         []fileToFetch
		totalBytes      int64
		downloadErrors  []error
	)
	for _, project := range projects {
		if err := ctx.Err(); err != nil {
			break
		}
		if !pl.Filter.AcceptProject(project) {
			continue
		}
		files, perr := ParseUpstreamProject(ctx, h.deps.HTTPClient, pl.UpstreamURL, project, creds)
		if perr != nil {
			downloadErrors = append(downloadErrors, perr)
			continue
		}
		for _, f := range files {
			if !pl.Filter.FilterFile(f, project) {
				continue
			}
			// F-07.4 (wt3): upstream pypi.org still lists legacy .egg /
			// .exe / .msi entries for some projects. pip hasn't installed
			// from any of them via simple/ since 2017, and the inline
			// sync-path version parser below only strips .gz/.tar/.zip —
			// so a .egg filename tail ends up as the "version" column.
			// Skip here before we ever enqueue the download so the mirror
			// stays clean (sdist parse invariant in parse.go:202).
			if !isInstallableExt(f.Filename) {
				continue
			}
			// F-07.6 (post-v1.4): the filename becomes the last path
			// segment under {proj}/pypi/{repo}/packages/ and the key the
			// simple-index uses for href generation. Reject hostile shapes
			// (path separators, control chars, quotes) before they touch
			// PathStore or the DB.
			if !isSafeMirrorFilename(f.Filename) {
				continue
			}
			// Idempotency by filename — matches pypi_files UNIQUE(repo_id, filename) (D-15).
			if existing, ferr := h.deps.PyPIFiles.FindByFilename(ctx, repoID, f.Filename); ferr == nil && existing != nil {
				continue
			}
			toFetch = append(toFetch, fileToFetch{project: project, file: f})
			totalBytes += f.Size
		}
	}

	progress := jobs.NewProgressWriter(h.deps.SyncJobs, jobID)
	defer func() { _ = progress.Flush(ctx) }()

	sem := make(chan struct{}, maxParallel)
	var (
		mu              sync.Mutex
		filesAdded      int64
		bytesDownload   int64
		accumulatedDone int64
		wg              sync.WaitGroup
	)

	for _, tf := range toFetch {
		tf := tf
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			step := fmt.Sprintf("pulling %s", tf.file.Filename)
			size, derr := h.fetchAndCommit(ctx, proj.Name, repo, tf.project, tf.file, creds, progress, step, &accumulatedDone, totalBytes)
			mu.Lock()
			defer mu.Unlock()
			if derr != nil {
				downloadErrors = append(downloadErrors, derr)
				return
			}
			if size > 0 {
				filesAdded++
				bytesDownload += size
				if filesAdded%50 == 0 && h.deps.Audit != nil {
					_ = h.deps.Audit.Record(ctx, audit.Event{
						Kind:       audit.EvtSyncProgress,
						TargetKind: "repo",
						TargetID:   strconv.FormatInt(repoID, 10),
						Details: map[string]any{
							"files_added":      filesAdded,
							"bytes_downloaded": bytesDownload,
							"upstream_url":     pl.UpstreamURL,
						},
					})
				}
			}
		}()
	}
	wg.Wait()

	if len(downloadErrors) > 0 {
		// F-09.8 (Codex batch-09 cross-cutting): per-file commits flipped
		// metadata_state=dirty; without a failure-path Kick the PEP 503
		// simple index pages stay behind the DB until the next successful
		// sync. Regen is idempotent — always safe.
		if h.deps.Coalescer != nil {
			h.deps.Coalescer.Get(repoID).Kick()
		}
		return h.fail(ctx, repoID, pl, startedAt, httpx.SanitizeUpstreamErr(downloadErrors[0]))
	}

	_ = progress.Set(ctx, "done", atomic.LoadInt64(&accumulatedDone), totalBytes)

	// D-03 closure: persist per-job file count once so the UI pill can
	// render "Sync complete · N files · X MB". Safe to read filesAdded
	// without the lock here — wg.Wait() above synced all goroutines.
	if h.deps.SyncJobs != nil {
		_ = h.deps.SyncJobs.SetFilesSynced(ctx, jobID, filesAdded)
	}

	// end-of-batch kick: single regen trigger after the whole sync batch.
	if h.deps.Coalescer != nil {
		h.deps.Coalescer.Get(repoID).Kick()
	}

	if h.deps.Audit != nil {
		_ = h.deps.Audit.Record(ctx, audit.Event{
			Kind:       audit.EvtSyncFinished,
			TargetKind: "repo",
			TargetID:   strconv.FormatInt(repoID, 10),
			Details: map[string]any{
				"files_added":      filesAdded,
				"bytes_downloaded": bytesDownload,
				"duration_ms":      time.Since(startedAt).Milliseconds(),
				"upstream_url":     pl.UpstreamURL,
				"cred_id":          pl.CredID,
			},
		})
	}
	return nil
}

func (h *SyncHandler) fail(ctx context.Context, repoID int64, pl SyncPayload, started time.Time, err error) error {
	if h.deps.Audit != nil {
		_ = h.deps.Audit.Record(ctx, audit.Event{
			Kind:       audit.EvtSyncFailed,
			TargetKind: "repo",
			TargetID:   strconv.FormatInt(repoID, 10),
			Details: map[string]any{
				"last_error":   truncateErr(err.Error()),
				"upstream_url": pl.UpstreamURL,
				"duration_ms":  time.Since(started).Milliseconds(),
			},
		})
	}
	return err
}

func (h *SyncHandler) fetchAndCommit(ctx context.Context, projectName string, repo *metadata.Repo, normalizedProject string, f UpstreamFile, creds AuthCreds, progress *jobs.ProgressWriter, step string, accumulatedDone *int64, totalBytes int64) (int64, error) {
	// Re-check FindByFilename inside the goroutine for race safety.
	if existing, ferr := h.deps.PyPIFiles.FindByFilename(ctx, repo.ID, f.Filename); ferr == nil && existing != nil {
		return 0, nil
	}

	// F-07.5 (post-v1.4): route through the canonical PEP 427 / PEP 625
	// parsers in parse.go rather than inline string-splitting. The pre-
	// v1.4 inline sdist split used LastIndex("-") which mis-attributed
	// dashed pre-release suffixes (`foo-1.0.0-rc1.tar.gz` → version="rc1")
	// and polluted the simple-index grouping.
	//
	// Parse runs BEFORE the download so a malformed upstream filename
	// fails fast without wasting bandwidth or leaving an orphaned blob
	// on disk (Codex review flagged the post-Put parse-failure path as
	// a storage leak). isInstallableExt already filters the legacy
	// bdist forms the inline split used to trip on, so reaching here
	// with an unparseable filename means the upstream is malformed.
	//
	// Heuristic caveat (Q1, deferred): parseSdistFilename splits at the
	// first `-` followed by a digit. A hypothetical name ending in
	// `-<digit>...` (e.g. `f-2do-1.0.0.tar.gz`) would mis-attribute the
	// `-2do` segment to the version. Not observed on real PyPI in 2026
	// and a full fix requires embedding a PEP 440 validator — tracked
	// for a future follow-up.
	var (
		kind    string
		version string
	)
	if strings.HasSuffix(strings.ToLower(f.Filename), ".whl") {
		kind = "wheel"
		_, v, perr := parseWheelFilename(f.Filename)
		if perr != nil {
			return 0, fmt.Errorf("pypi_sync: %w", perr)
		}
		version = v
	} else {
		kind = "sdist"
		_, v, perr := parseSdistFilename(f.Filename)
		if perr != nil {
			return 0, fmt.Errorf("pypi_sync: %w", perr)
		}
		version = v
	}

	body, size, dgst, err := downloadAndHashWithProgress(ctx, h.deps.HTTPClient, f.URL, creds, progress, step, accumulatedDone, totalBytes)
	if err != nil {
		return 0, fmt.Errorf("pypi_sync: download %s: %w", f.Filename, err)
	}
	digest := "sha256:" + dgst
	if f.SHA256 != "" && !strings.EqualFold(strings.ToLower(f.SHA256), dgst) {
		return 0, fmt.Errorf("pypi_sync: digest mismatch on %s", f.Filename)
	}

	storageKey := strings.Join([]string{projectName, "pypi", repo.Name, "packages", f.Filename}, "/")
	if _, err := h.deps.Path.Put(ctx, storageKey, openBytesReader(body)); err != nil {
		return 0, fmt.Errorf("pypi_sync: store %s: %w", f.Filename, err)
	}

	if err := h.deps.DB.WriteTx(ctx, func(tx *sql.Tx) error {
		if _, err := h.deps.PyPIFiles.Insert(ctx, tx, &metadata.PyPIFile{
			RepoID:            repo.ID,
			ProjectNormalized: normalizedProject,
			Version:           version,
			Filename:          f.Filename,
			Kind:              kind,
			RequiresPython:    f.RequiresPython,
			SizeBytes:         size,
			Digest:            digest,
		}); err != nil {
			return err
		}
		if err := metadata.IndexPyPIDelete(ctx, tx, repo.ID, normalizedProject, version, f.RequiresPython); err != nil {
			return err
		}
		if err := metadata.IndexPyPI(ctx, tx, repo.ID, normalizedProject, version, f.RequiresPython, ""); err != nil {
			return err
		}
		if repo.AutoScan && h.deps.Scans != nil {
			if _, err := h.deps.Scans.Enqueue(ctx, tx, repo.ID, "pypi", f.Filename); err != nil {
				return err
			}
		}
		return h.deps.Repos.SetMetadataState(ctx, tx, repo.ID, metadata.MetadataStateDirty)
	}); err != nil {
		abs := filepath.Join(h.deps.RepoRoot, filepath.FromSlash(storageKey))
		_ = os.Remove(abs)
		return 0, fmt.Errorf("pypi_sync: commit %s: %w", f.Filename, err)
	}
	return size, nil
}

// downloadAndHashWithProgress GETs urlStr with creds and hashes the body
// inline. When progress is non-nil, the body is wrapped with
// jobs.CountingReader so every non-zero read advances *accumulatedDone
// (atomic under parallel downloads) and triggers a throttled progress.Set.
func downloadAndHashWithProgress(ctx context.Context, client *http.Client, urlStr string, creds AuthCreds, progress *jobs.ProgressWriter, step string, accumulatedDone *int64, total int64) ([]byte, int64, string, error) {
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return nil, 0, "", fmt.Errorf("build req: %w", err)
	}
	applyCreds(req, creds)
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, "", fmt.Errorf("get %s: %w", urlStr, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, 0, "", fmt.Errorf("%s -> %d", urlStr, resp.StatusCode)
	}
	hasher := sha256.New()
	var reader io.Reader = io.LimitReader(io.TeeReader(resp.Body, hasher), 2*1024*1024*1024)
	if progress != nil && accumulatedDone != nil {
		reader = &jobs.CountingReader{R: reader, OnRead: func(n int) {
			done := atomic.AddInt64(accumulatedDone, int64(n))
			_ = progress.Set(ctx, step, done, total)
		}}
	}
	body, err := io.ReadAll(reader)
	if err != nil {
		return nil, 0, "", fmt.Errorf("read %s: %w", urlStr, err)
	}
	return body, int64(len(body)), hex.EncodeToString(hasher.Sum(nil)), nil
}

func openBytesReader(b []byte) io.Reader { return &bytesReader{b: b} }

type bytesReader struct {
	b []byte
	i int
}

func (r *bytesReader) Read(p []byte) (int, error) {
	if r.i >= len(r.b) {
		return 0, io.EOF
	}
	n := copy(p, r.b[r.i:])
	r.i += n
	return n, nil
}

func truncateErr(s string) string {
	const max = 1024
	if len(s) <= max {
		return s
	}
	return s[:max] + "...[truncated]"
}
