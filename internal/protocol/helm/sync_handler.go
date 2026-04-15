// Package helm — Phase 03 Plan 06 SYNC-05 sync handler.
package helm

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
const SyncJobKind = "helm_sync"

// SyncPayload is the job payload shape (D-13).
type SyncPayload struct {
	UpstreamURL string      `json:"upstream_url"`
	CredID      *int64      `json:"cred_id,omitempty"`
	Filter      *SyncFilter `json:"filter,omitempty"`
}

// SyncDeps bundles dependencies for the Helm sync handler.
type SyncDeps struct {
	DB         *metadata.DB
	Path       storage.PathStore
	HelmCharts *metadata.HelmChartsRepo
	Repos      *metadata.ReposRepo
	Projects   *metadata.ProjectsRepo
	Creds      *metadata.UpstreamCredsRepo
	Audit      audit.Logger
	Coalescer  *regen.Registry
	HTTPClient *http.Client
	RepoRoot   string
	Cfg        config.SyncConfig
}

// SyncHandler is the sync-pool handler for kind="helm_sync".
type SyncHandler struct{ deps SyncDeps }

// NewSyncHandler constructs a handler.
func NewSyncHandler(deps SyncDeps) *SyncHandler { return &SyncHandler{deps: deps} }

// Handle runs one helm_sync job.
func (h *SyncHandler) Handle(ctx context.Context, payload string, projectID, repoID int64) error {
	timeout := h.deps.Cfg.UpstreamHTTPTimeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var pl SyncPayload
	if err := json.Unmarshal([]byte(payload), &pl); err != nil {
		return fmt.Errorf("helm_sync: payload: %w", err)
	}
	if pl.UpstreamURL == "" {
		return fmt.Errorf("helm_sync: upstream_url required")
	}
	if pl.Filter == nil {
		pl.Filter = &SyncFilter{}
	}

	repo, err := h.deps.Repos.FindByID(ctx, repoID)
	if err != nil || repo == nil {
		return fmt.Errorf("helm_sync: repo %d: %w", repoID, err)
	}
	proj, err := h.deps.Projects.FindByID(ctx, repo.ProjectID)
	if err != nil || proj == nil {
		return fmt.Errorf("helm_sync: project %d: %w", repo.ProjectID, err)
	}

	var creds AuthCreds
	if pl.CredID != nil {
		user, pw, tok, host, lerr := h.deps.Creds.Lookup(ctx, projectID, *pl.CredID)
		if lerr != nil {
			return httpx.SanitizeUpstreamErr(fmt.Errorf("helm_sync: cred lookup: %w", lerr))
		}
		u, perr := url.Parse(pl.UpstreamURL)
		if perr != nil {
			return fmt.Errorf("helm_sync: parse upstream_url: %w", perr)
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

	yieldFn := func(ent UpstreamEntry) error {
		if ent.Digest != "" {
			if existing, ferr := h.deps.HelmCharts.FindByDigest(ctx, repoID, ent.Digest); ferr == nil && existing != nil {
				return nil
			}
		}
		entCopy := ent
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			size, derr := h.fetchAndCommit(ctx, proj.Name, repo, entCopy, creds)
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
		return nil
	}

	_, parseErr := ParseUpstream(ctx, h.deps.HTTPClient, pl.UpstreamURL, creds, *pl.Filter, yieldFn)
	wg.Wait()

	if parseErr != nil {
		return h.fail(ctx, repoID, pl, startedAt, httpx.SanitizeUpstreamErr(parseErr))
	}
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

func (h *SyncHandler) fetchAndCommit(ctx context.Context, projectName string, repo *metadata.Repo, ent UpstreamEntry, creds AuthCreds) (int64, error) {
	if ent.Digest != "" {
		if existing, ferr := h.deps.HelmCharts.FindByDigest(ctx, repo.ID, ent.Digest); ferr == nil && existing != nil {
			return 0, nil
		}
	}
	body, size, dgst, err := downloadAndHash(ctx, h.deps.HTTPClient, ent.Path, creds)
	if err != nil {
		return 0, fmt.Errorf("helm_sync: download %s: %w", ent.Filename, err)
	}
	digest := "sha256:" + dgst
	if ent.Digest != "" && !strings.EqualFold(ent.Digest, digest) {
		return 0, fmt.Errorf("helm_sync: digest mismatch on %s", ent.Filename)
	}
	if existing, ferr := h.deps.HelmCharts.FindByDigest(ctx, repo.ID, digest); ferr == nil && existing != nil {
		return 0, nil
	}

	storageKey := strings.Join([]string{projectName, "helm", repo.Name, "charts", ent.Filename}, "/")
	if _, err := h.deps.Path.Put(ctx, storageKey, openBytesReader(body)); err != nil {
		return 0, fmt.Errorf("helm_sync: store %s: %w", ent.Filename, err)
	}

	abs := filepath.Join(h.deps.RepoRoot, filepath.FromSlash(storageKey))
	chart, perr := Parse(abs)
	if perr != nil {
		_ = os.Remove(abs)
		return 0, fmt.Errorf("helm_sync: parse %s: %w", ent.Filename, perr)
	}

	if err := h.deps.DB.WriteTx(ctx, func(tx *sql.Tx) error {
		if _, err := h.deps.HelmCharts.Insert(ctx, tx, &metadata.HelmChart{
			RepoID:          repo.ID,
			Name:            chart.Name,
			Version:         chart.Version,
			AppVersion:      chart.AppVersion,
			Description:     chart.Description,
			KeywordsJSON:    chart.KeywordsJSON(),
			MaintainersJSON: chart.MaintainersJSON(),
			SizeBytes:       size,
			Digest:          digest,
			Filename:        ent.Filename,
		}); err != nil {
			return err
		}
		if err := metadata.IndexHelmDelete(ctx, tx, repo.ID, chart.Name, chart.Version, chart.AppVersion); err != nil {
			return err
		}
		if err := metadata.IndexHelm(ctx, tx, repo.ID, chart.Name, chart.Version, chart.AppVersion, chart.Description); err != nil {
			return err
		}
		return h.deps.Repos.SetMetadataState(ctx, tx, repo.ID, metadata.MetadataStateDirty)
	}); err != nil {
		_ = os.Remove(abs)
		return 0, fmt.Errorf("helm_sync: commit %s: %w", ent.Filename, err)
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
	body, err := io.ReadAll(io.LimitReader(io.TeeReader(resp.Body, hasher), 1024*1024*1024))
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
