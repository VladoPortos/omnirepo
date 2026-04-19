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

	"github.com/dxc-internal/omnirepo/internal/audit"
	"github.com/dxc-internal/omnirepo/internal/config"
	"github.com/dxc-internal/omnirepo/internal/httpx"
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
}

// SyncHandler is the sync-pool handler for kind="pypi_sync".
type SyncHandler struct{ deps SyncDeps }

// NewSyncHandler constructs a handler.
func NewSyncHandler(deps SyncDeps) *SyncHandler { return &SyncHandler{deps: deps} }

// Handle runs one pypi_sync job.
func (h *SyncHandler) Handle(ctx context.Context, payload string, projectID, repoID int64) error {
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
	sem := make(chan struct{}, maxParallel)
	var (
		mu             sync.Mutex
		filesAdded     int64
		bytesDownload  int64
		downloadErrors []error
		wg             sync.WaitGroup
	)

	projects, parseErr := ParseUpstreamSimpleIndex(ctx, h.deps.HTTPClient, pl.UpstreamURL, creds)
	if parseErr != nil {
		return h.fail(ctx, repoID, pl, startedAt, httpx.SanitizeUpstreamErr(parseErr))
	}
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
			// Idempotency by filename — matches pypi_files UNIQUE(repo_id, filename) (D-15).
			if existing, ferr := h.deps.PyPIFiles.FindByFilename(ctx, repoID, f.Filename); ferr == nil && existing != nil {
				continue
			}
			f := f
			project := project
			sem <- struct{}{}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				size, derr := h.fetchAndCommit(ctx, proj.Name, repo, project, f, creds)
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
	}
	wg.Wait()

	if len(downloadErrors) > 0 {
		return h.fail(ctx, repoID, pl, startedAt, httpx.SanitizeUpstreamErr(downloadErrors[0]))
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

func (h *SyncHandler) fetchAndCommit(ctx context.Context, projectName string, repo *metadata.Repo, normalizedProject string, f UpstreamFile, creds AuthCreds) (int64, error) {
	// Re-check FindByFilename inside the goroutine for race safety.
	if existing, ferr := h.deps.PyPIFiles.FindByFilename(ctx, repo.ID, f.Filename); ferr == nil && existing != nil {
		return 0, nil
	}
	body, size, dgst, err := downloadAndHash(ctx, h.deps.HTTPClient, f.URL, creds)
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

	kind := "wheel"
	version := ""
	if strings.HasSuffix(f.Filename, ".whl") {
		// Filename: name-version-...
		parts := strings.SplitN(f.Filename, "-", 3)
		if len(parts) >= 2 {
			version = parts[1]
		}
	} else {
		kind = "sdist"
		// For sdists: name-version.tar.gz
		base := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(f.Filename, ".gz"), ".tar"), ".zip")
		idx := strings.LastIndex(base, "-")
		if idx > 0 {
			version = base[idx+1:]
		}
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

func downloadAndHash(ctx context.Context, client *http.Client, urlStr string, creds AuthCreds) ([]byte, int64, string, error) {
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
	body, err := io.ReadAll(io.LimitReader(io.TeeReader(resp.Body, hasher), 2*1024*1024*1024))
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
