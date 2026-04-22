// Package deb — Phase 03 Plan 06 SYNC-05 sync handler. Mirrors the rpm
// sync_handler shape; differs in idempotency keying (FindByDigest still
// applies) and the per-(suite, component, arch) layout maintained via
// the apt_suites table + DEBPackagesRepo.Insert.
package deb

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
const SyncJobKind = "apt_sync"

// SyncPayload is the job payload shape (D-13). Suite optionally narrows
// which dist of the upstream to mirror; default = "stable".
type SyncPayload struct {
	UpstreamURL string      `json:"upstream_url"`
	CredID      *int64      `json:"cred_id,omitempty"`
	Suite       string      `json:"suite,omitempty"`
	Filter      *SyncFilter `json:"filter,omitempty"`
}

// SyncDeps bundles dependencies for the DEB sync handler.
type SyncDeps struct {
	DB          *metadata.DB
	Path        storage.PathStore
	DEBPackages *metadata.DEBPackagesRepo
	AptSuites   *metadata.AptSuitesRepo
	Repos       *metadata.ReposRepo
	Projects    *metadata.ProjectsRepo
	Creds       *metadata.UpstreamCredsRepo
	Scans       *metadata.ScansRepo
	Audit       audit.Logger
	Coalescer   *regen.Registry
	HTTPClient  *http.Client
	RepoRoot    string
	Cfg         config.SyncConfig
	// Phase 8 Plan 02 (M2.4): sync-jobs repo for throttled byte-level
	// progress emit. Nil-safe — if unwired, progress.Set is a no-op.
	SyncJobs *metadata.SyncJobsRepo
}

// SyncHandler is the sync-pool handler for kind="apt_sync".
type SyncHandler struct{ deps SyncDeps }

// NewSyncHandler constructs a handler.
func NewSyncHandler(deps SyncDeps) *SyncHandler { return &SyncHandler{deps: deps} }

// Handle runs one apt_sync job.
//
// Phase 8 Plan 02 / M2.4: accepts jobID so the handler can emit throttled
// byte-level progress via ProgressWriter. Flow change: ParseUpstream's
// yieldFn now only COLLECTS entries (no downloads); after parse we sum
// .Size for totalBytes and iterate the collected slice with progress
// emits. Keeps idempotency + parallelism semantics byte-for-byte with
// v1.0 — only instrumentation added.
func (h *SyncHandler) Handle(ctx context.Context, payload string, projectID, repoID, jobID int64) error {
	timeout := h.deps.Cfg.UpstreamHTTPTimeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var pl SyncPayload
	if err := json.Unmarshal([]byte(payload), &pl); err != nil {
		return fmt.Errorf("apt_sync: payload: %w", err)
	}
	if pl.UpstreamURL == "" {
		return fmt.Errorf("apt_sync: upstream_url required")
	}
	if pl.Filter == nil {
		pl.Filter = &SyncFilter{}
	}
	suite := pl.Suite
	if suite == "" {
		suite = "stable"
	}

	repo, err := h.deps.Repos.FindByID(ctx, repoID)
	if err != nil || repo == nil {
		return fmt.Errorf("apt_sync: repo %d: %w", repoID, err)
	}
	proj, err := h.deps.Projects.FindByID(ctx, repo.ProjectID)
	if err != nil || proj == nil {
		return fmt.Errorf("apt_sync: project %d: %w", repo.ProjectID, err)
	}

	var creds AuthCreds
	if pl.CredID != nil {
		user, pw, tok, host, lerr := h.deps.Creds.Lookup(ctx, projectID, *pl.CredID)
		if lerr != nil {
			return httpx.SanitizeUpstreamErr(fmt.Errorf("apt_sync: cred lookup: %w", lerr))
		}
		u, perr := url.Parse(pl.UpstreamURL)
		if perr != nil {
			return fmt.Errorf("apt_sync: parse upstream_url: %w", perr)
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

	// Phase 8 Plan 02 / M2.4: collect all entries first so we can emit
	// byte-level progress with a stable denominator. The collect pass
	// filters identical to v1.0's yieldFn (skips rows already present
	// by digest) so the slice holds only entries we'll actually download.
	var (
		entries    []UpstreamEntry
		totalBytes int64
	)
	collectFn := func(ent UpstreamEntry) error {
		if ent.Digest != "" {
			if existing, ferr := h.deps.DEBPackages.FindByDigest(ctx, repoID, ent.Digest); ferr == nil && existing != nil {
				return nil
			}
		}
		entries = append(entries, ent)
		totalBytes += ent.Size
		return nil
	}
	// Phase 8 live-test fix: iterate ParseUpstream over each suite the
	// filter names. The mirror UI submits suites via `filter.Suites`
	// (CSV input in FilterWidgetApt); the v1.0 top-level `suite` field
	// is unset for mirror-driven syncs and would otherwise default to
	// "stable", which Ubuntu archives don't publish — ParseUpstream
	// then returns (0, nil) silently, yielding a 0-byte "done" with no
	// files. Iterating matches the filter's intent: "sync every suite
	// in this list".
	suitesToSync := pl.Filter.Suites
	if len(suitesToSync) == 0 {
		suitesToSync = []string{suite}
	}
	// Remove the Suites narrow from the inner filter so ParseUpstream's
	// per-suite accept gate passes (filter.acceptSuite is what blocked
	// the whole sync when the top-level suite didn't appear in
	// filter.Suites).
	innerFilter := *pl.Filter
	innerFilter.Suites = nil
	for _, s := range suitesToSync {
		if s == "" {
			continue
		}
		_, parseErr := ParseUpstream(ctx, h.deps.HTTPClient, pl.UpstreamURL, s, creds, innerFilter, collectFn)
		if parseErr != nil {
			return h.fail(ctx, repoID, pl, startedAt, httpx.SanitizeUpstreamErr(parseErr))
		}
	}

	// ProgressWriter persists (step, done, total) under the throttle
	// contract (D-12). Flush on return guarantees the final step lands
	// even if the last Set was suppressed.
	progress := jobs.NewProgressWriter(h.deps.SyncJobs, jobID)
	defer func() { _ = progress.Flush(ctx) }()

	sem := make(chan struct{}, maxParallel)
	var (
		mu             sync.Mutex
		filesAdded     int64
		bytesDownload  int64
		accumulatedDone int64 // atomic: CountingReader callbacks
		downloadErrors []error
		wg             sync.WaitGroup
	)

	for _, ent := range entries {
		ent := ent
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			step := fmt.Sprintf("pulling %s_%s", ent.Control.Package, ent.Control.Version)
			size, derr := h.fetchAndCommit(ctx, proj.Name, repo, ent, creds, progress, step, &accumulatedDone, totalBytes)
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
		// metadata_state=dirty; without a failure-path Kick the
		// Packages/Release files stay behind the DB until the next
		// successful sync. Regen is idempotent — always safe.
		if h.deps.Coalescer != nil {
			h.deps.Coalescer.Get(repoID).Kick()
		}
		return h.fail(ctx, repoID, pl, startedAt, httpx.SanitizeUpstreamErr(downloadErrors[0]))
	}

	// Terminal progress emit so Flush writes "done" rather than the last
	// in-flight per-file step (UI poll mid-flush would otherwise keep
	// reading "pulling foo_1.0" forever on zero-entry syncs).
	_ = progress.Set(ctx, "done", atomic.LoadInt64(&accumulatedDone), totalBytes)

	// D-03 closure: persist per-job file count once so the UI pill can
	// render "Sync complete · N files · X MB". wg.Wait() synced all goroutines.
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

func (h *SyncHandler) fetchAndCommit(ctx context.Context, projectName string, repo *metadata.Repo, ent UpstreamEntry, creds AuthCreds, progress *jobs.ProgressWriter, step string, accumulatedDone *int64, totalBytes int64) (int64, error) {
	if ent.Digest != "" {
		if existing, ferr := h.deps.DEBPackages.FindByDigest(ctx, repo.ID, ent.Digest); ferr == nil && existing != nil {
			return 0, nil
		}
	}
	body, size, dgst, err := downloadAndHashWithProgress(ctx, h.deps.HTTPClient, ent.Path, creds, progress, step, accumulatedDone, totalBytes)
	if err != nil {
		return 0, fmt.Errorf("apt_sync: download %s: %w", ent.Filename, err)
	}
	digest := "sha256:" + dgst
	if ent.Digest != "" && !strings.EqualFold(ent.Digest, digest) {
		return 0, fmt.Errorf("apt_sync: digest mismatch on %s", ent.Filename)
	}
	if existing, ferr := h.deps.DEBPackages.FindByDigest(ctx, repo.ID, digest); ferr == nil && existing != nil {
		return 0, nil
	}

	// Pool-relative storage key reuses the deb handler convention.
	// W-03: consult dists/<suite>/Release for layout hints before falling
	// back to filename-based inference. ent.Suite defaults to "stable"
	// one step up if the upstream parser didn't populate it.
	suite := ent.Suite
	if suite == "" {
		suite = "stable"
	}
	rest := relPoolPath(h.deps.RepoRoot, projectName, repo.Name, suite, ent.Filename, ent.Control)
	storageKey := strings.Join([]string{projectName, "deb", repo.Name, rest}, "/")
	if _, err := h.deps.Path.Put(ctx, storageKey, openBytesReader(body)); err != nil {
		return 0, fmt.Errorf("apt_sync: store %s: %w", ent.Filename, err)
	}

	// Resolve / upsert apt_suites row, then insert deb_packages.
	if err := h.deps.DB.WriteTx(ctx, func(tx *sql.Tx) error {
		suiteID, err := h.deps.AptSuites.Insert(ctx, tx, repo.ID, ent.Suite, ent.Component, ent.Arch)
		if err != nil {
			return err
		}
		if _, err := h.deps.DEBPackages.Insert(ctx, tx, &metadata.DEBPackage{
			RepoID:          repo.ID,
			SuiteID:         suiteID,
			Package:         ent.Control.Package,
			Version:         ent.Control.Version,
			Architecture:    ent.Control.Architecture,
			Maintainer:      ent.Control.Maintainer,
			Section:         ent.Control.Section,
			Priority:        ent.Control.Priority,
			Depends:         ent.Control.Depends,
			Description:     ent.Control.Description,
			SizeBytes:       size,
			Digest:          digest,
			Filename:        ent.Filename,
			// F-T6: persist the real pool path so regen.go emits it
			// verbatim as the Filename field. `rest` already matches the
			// on-disk layout relPoolPath() just computed.
			StoragePoolPath: rest,
		}); err != nil {
			return err
		}
		if err := metadata.IndexDEBDelete(ctx, tx, repo.ID, ent.Control.Package, ent.Control.Version, ent.Control.Architecture); err != nil {
			return err
		}
		if err := metadata.IndexDEB(ctx, tx, repo.ID, ent.Control.Package, ent.Control.Version, ent.Control.Architecture, ent.Control.Description); err != nil {
			return err
		}
		if repo.AutoScan && h.deps.Scans != nil {
			if _, err := h.deps.Scans.Enqueue(ctx, tx, repo.ID, "deb", ent.Filename); err != nil {
				return err
			}
		}
		return h.deps.Repos.SetMetadataState(ctx, tx, repo.ID, metadata.MetadataStateDirty)
	}); err != nil {
		abs := filepath.Join(h.deps.RepoRoot, filepath.FromSlash(storageKey))
		_ = os.Remove(abs)
		return 0, fmt.Errorf("apt_sync: commit %s: %w", ent.Filename, err)
	}
	return size, nil
}

// relPoolPath returns the per-package canonical pool/-relative path,
// consulting the repo's dists/<suite>/Release (W-03) before falling back to
// filename inference. Thin wrapper over ResolvePoolPath — kept to preserve
// the call-site shape in fetchAndCommit.
//
// Shape: pool/<component>/<initial>/<package>/<filename>. Component comes
// from the Release file's Components: line when available; otherwise
// defaults to "main".
func relPoolPath(repoRoot, projectName, repoName, suite, filename string, ctrl *Control) string {
	return ResolvePoolPath(repoRoot, projectName, repoName, suite, filename, ctrl)
}

// downloadAndHashWithProgress is the instrumented version used by the sync
// handler. When progress is non-nil, the response body is wrapped with
// jobs.CountingReader so every non-zero read advances *accumulatedDone
// (atomic under parallel downloads) and triggers a throttled progress.Set
// with the supplied step. total is passed through verbatim.
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
	var reader io.Reader = io.LimitReader(io.TeeReader(resp.Body, hasher), 4*1024*1024*1024)
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
