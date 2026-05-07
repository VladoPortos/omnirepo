// Package rpm — Phase 03 Plan 06 SYNC-05 sync handler.
//
// Registered on the SyncPool under kind="rpm_sync". Drives the upstream
// parser, fetches missing .rpm files into the path store, inserts
// rpm_packages + rpm_fts rows in batch (per-file writer tx, NO per-file
// coalescer.Kick), then performs ONE end-of-batch coalescer.Kick so the
// debounced regen runs once per sync batch (D-08).
package rpm

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	"github.com/dxc-internal/omnirepo/internal/driftpurge"
	"github.com/dxc-internal/omnirepo/internal/httpx"
	"github.com/dxc-internal/omnirepo/internal/jobs"
	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/protocol/regen"
	"github.com/dxc-internal/omnirepo/internal/storage"
	"github.com/dxc-internal/omnirepo/internal/streamio"
)

// SyncJobKind is the sync_jobs.kind value routed to SyncHandler.Handle.
const SyncJobKind = "rpm_sync"

// SyncPayload is the JSON shape persisted in sync_jobs.payload_json (D-13).
type SyncPayload struct {
	UpstreamURL string      `json:"upstream_url"`
	CredID      *int64      `json:"cred_id,omitempty"`
	Filter      *SyncFilter `json:"filter,omitempty"`
	// v1.7 / UIBACK-03: operator-confirmed override of the percent-
	// threshold drift-purge guard. See pypi.SyncPayload for full
	// docs — same shape across the four mirror protocols.
	ForceDriftThreshold bool `json:"force_drift_threshold,omitempty"`
}

// SyncDeps bundles dependencies for the RPM sync handler.
type SyncDeps struct {
	DB          *metadata.DB
	Path        storage.PathStore
	RPMPackages *metadata.RPMPackagesRepo
	Repos       *metadata.ReposRepo
	Projects    *metadata.ProjectsRepo
	Creds       *metadata.UpstreamCredsRepo
	Scans       *metadata.ScansRepo
	Audit       audit.Logger
	Coalescer   *regen.Registry
	HTTPClient  *http.Client
	RepoRoot    string
	Cfg         config.SyncConfig
	// Phase 8 Plan 02 (M2.5): sync-jobs repo for throttled byte-level
	// progress emit. Nil-safe — if unwired, progress.Set is a no-op.
	SyncJobs *metadata.SyncJobsRepo
	// Trash is the soft-delete primitive used by v1.5 Phase 6 drift purge
	// (DRIFTPURGE-01..05) to move drifted .rpm blobs to the trash root
	// before deleting their rpm_packages row. Nil-safe — when nil, drift
	// purge is structurally skipped even if repo.DriftPurge is true.
	Trash storage.Trash
}

// SyncHandler is the sync-pool handler for kind="rpm_sync".
type SyncHandler struct{ deps SyncDeps }

// NewSyncHandler constructs a handler.
func NewSyncHandler(deps SyncDeps) *SyncHandler { return &SyncHandler{deps: deps} }

// Handle implements the jobs.JobHandler signature
// (ctx, payload string, projectID, repoID, jobID int64) -> error.
//
// Phase 8 Plan 02 / M2.5: jobID threads through so the handler can emit
// byte-level progress via jobs.ProgressWriter.
func (h *SyncHandler) Handle(ctx context.Context, payload string, projectID, repoID, jobID int64) error {
	timeout := h.deps.Cfg.UpstreamHTTPTimeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var pl SyncPayload
	if err := json.Unmarshal([]byte(payload), &pl); err != nil {
		return fmt.Errorf("rpm_sync: payload: %w", err)
	}
	if pl.UpstreamURL == "" {
		return fmt.Errorf("rpm_sync: upstream_url required")
	}
	if pl.Filter == nil {
		pl.Filter = &SyncFilter{}
	}

	repo, err := h.deps.Repos.FindByID(ctx, repoID)
	if err != nil || repo == nil {
		return fmt.Errorf("rpm_sync: repo %d: %w", repoID, err)
	}
	proj, err := h.deps.Projects.FindByID(ctx, repo.ProjectID)
	if err != nil || proj == nil {
		return fmt.Errorf("rpm_sync: project %d: %w", repo.ProjectID, err)
	}

	// Cred lookup + host-match validation.
	var creds AuthCreds
	if pl.CredID != nil {
		user, pw, tok, host, lerr := h.deps.Creds.Lookup(ctx, projectID, *pl.CredID)
		if lerr != nil {
			return httpx.SanitizeUpstreamErr(fmt.Errorf("rpm_sync: cred lookup: %w", lerr))
		}
		u, perr := url.Parse(pl.UpstreamURL)
		if perr != nil {
			return fmt.Errorf("rpm_sync: parse upstream_url: %w", perr)
		}
		if host != u.Host {
			return httpx.SanitizeUpstreamErr(
				fmt.Errorf("cred_host_mismatch: cred=%s upstream=%s", host, u.Host))
		}
		creds = AuthCreds{User: user, Password: pw, Token: tok}
		// Emit upstream_cred.used ONCE per job (D-19).
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

	// Phase 8 Plan 02 / M2.5: collect entries first so the progress bar
	// has a stable totalBytes denominator (sum of primary.xml <size
	// package="..."/> values). Keeps idempotency-by-digest filtering in
	// the collect pass to avoid listing already-present rows as
	// "to-download".
	//
	// v1.5 Phase 6 (DRIFTPURGE-01): the SAME pass also captures the full
	// upstream key set (every accepted upstream entry, not just the
	// to-fetch subset) so the end-of-Handle drift step can compute
	// local\upstream without re-parsing primary.xml. D-12 RPM projection:
	// {name, version, arch} — coarser than DB UNIQUE NEVRA per Pitfall 7.
	var (
		entries      []UpstreamEntry
		upstreamKeys []driftpurge.Key
		totalBytes   int64
	)
	collectFn := func(ent UpstreamEntry) error {
		if ent.Metadata != nil {
			upstreamKeys = append(upstreamKeys, driftpurge.Key{
				A: ent.Metadata.Name,
				B: ent.Metadata.Version.Ver,
				C: ent.Metadata.Arch,
			})
		}
		if ent.Digest != "" {
			if existing, ferr := h.deps.RPMPackages.FindByDigest(ctx, repoID, ent.Digest); ferr == nil && existing != nil {
				return nil
			}
		}
		entries = append(entries, ent)
		totalBytes += ent.Size
		return nil
	}
	_, parseErr := ParseUpstream(ctx, h.deps.HTTPClient, pl.UpstreamURL, creds, *pl.Filter, collectFn)
	if parseErr != nil {
		return h.fail(ctx, repoID, pl, startedAt, httpx.SanitizeUpstreamErr(parseErr))
	}

	progress := jobs.NewProgressWriter(h.deps.SyncJobs, jobID)
	defer func() { _ = progress.Flush(ctx) }()

	sem := make(chan struct{}, maxParallel)
	var (
		mu              sync.Mutex
		filesAdded      int64
		bytesDownload   int64
		accumulatedDone int64
		downloadErrors  []error
		wg              sync.WaitGroup
	)

	progressEvery := func(n int64) bool { return n > 0 && n%50 == 0 }

	for _, ent := range entries {
		ent := ent
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			// RPM package filename encodes <name>-<version>-<release>.<arch>.rpm;
			// render the step as "pulling <stem>.rpm" where stem is the
			// filename minus extension.
			stem := strings.TrimSuffix(ent.Filename, ".rpm")
			step := fmt.Sprintf("pulling %s.rpm", stem)
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
				if progressEvery(filesAdded) && h.deps.Audit != nil {
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
		// metadata_state=dirty; without a failure-path Kick the repomd/
		// primary.xml stays behind the DB until the next successful sync.
		// Regen is idempotent — always safe.
		if h.deps.Coalescer != nil {
			h.deps.Coalescer.Get(repoID).Kick()
		}
		return h.fail(ctx, repoID, pl, startedAt, httpx.SanitizeUpstreamErr(downloadErrors[0]))
	}

	// Terminal progress emit so Flush writes "done" with accumulated bytes.
	_ = progress.Set(ctx, "done", atomic.LoadInt64(&accumulatedDone), totalBytes)

	// v1.5 Phase 6 — Drift purge (DRIFTPURGE-01..05). Runs after upload
	// success (D-07) and before SetFilesSynced. Failed syncs return via
	// h.fail(...) earlier in the function, so this step is structurally
	// unreachable on partial-sync paths (D-11).
	if repo.DriftPurge && h.deps.Trash != nil {
		adapter := driftpurge.NewRPMAdapter(
			upstreamKeys,
			h.deps.RPMPackages,
			h.deps.Trash,
			func(row *metadata.RPMPackage) string {
				// Mirror the ingest path used by fetchAndCommit (canonical
				// NEVRA filename in the {project}/rpm/{repo}/Packages/ dir).
				key := storageKeyFor(proj.Name, repo.Name, row.Filename)
				return filepath.Join(h.deps.RepoRoot, filepath.FromSlash(key))
			},
		)
		var report driftpurge.DriftReport
		if err := h.deps.DB.WriteTx(ctx, func(tx *sql.Tx) error {
			var rerr error
			report, rerr = driftpurge.Run(
				ctx, tx, repo.ID, "", adapter,
				h.deps.Cfg.DriftPurgeThresholdPct, pl.ForceDriftThreshold,
			)
			return rerr
		}); err != nil {
			return h.fail(ctx, repo.ID, pl, startedAt, fmt.Errorf("drift purge: %w", err))
		}

		switch {
		case report.Skipped && report.Reason == "threshold_exceeded":
			// UIBACK-03: percent-threshold guard tripped.
			if h.deps.SyncJobs != nil {
				_ = h.deps.SyncJobs.SetSummaryDriftBlocked(ctx, jobID, int64(report.BlockedCount))
			}
			if h.deps.Audit != nil {
				_ = h.deps.Audit.Record(ctx, audit.Event{
					Kind:       audit.EvtMirrorDriftPurgeSkipped,
					TargetKind: "repo",
					TargetID:   strconv.FormatInt(repo.ID, 10),
					Details: map[string]any{
						"protocol":      "rpm",
						"reason":        report.Reason,
						"local_count":   int64(report.LocalCount),
						"blocked_count": int64(report.BlockedCount),
						"threshold_pct": int64(h.deps.Cfg.DriftPurgeThresholdPct),
						"sync_job_id":   jobID,
						"upstream_url":  pl.UpstreamURL,
					},
				})
			}
		case report.Skipped:
			if h.deps.Audit != nil {
				_ = h.deps.Audit.Record(ctx, audit.Event{
					Kind:       audit.EvtMirrorDriftPurgeSkipped,
					TargetKind: "repo",
					TargetID:   strconv.FormatInt(repo.ID, 10),
					Details: map[string]any{
						"protocol":     "rpm",
						"reason":       report.Reason,
						"local_count":  int64(report.LocalCount),
						"sync_job_id":  jobID,
						"upstream_url": pl.UpstreamURL,
					},
				})
			}
		case report.PurgedCount > 0:
			if h.deps.Audit != nil {
				_ = h.deps.Audit.Record(ctx, audit.Event{
					Kind:       audit.EvtMirrorDriftPurged,
					TargetKind: "repo",
					TargetID:   strconv.FormatInt(repo.ID, 10),
					Details: map[string]any{
						"protocol":     "rpm",
						"count":        int64(report.PurgedCount),
						"sample":       report.Sample,
						"sync_job_id":  jobID,
						"upstream_url": pl.UpstreamURL,
					},
				})
			}
			if h.deps.SyncJobs != nil {
				_ = h.deps.SyncJobs.SetSummaryDriftPurged(ctx, jobID, int64(report.PurgedCount))
			}
		default:
			if h.deps.SyncJobs != nil {
				_ = h.deps.SyncJobs.SetSummaryDriftPurged(ctx, jobID, 0)
			}
		}
	}

	// D-03 closure: persist per-job file count once so the UI pill can
	// render "Sync complete · N files · X MB". wg.Wait() synced all goroutines.
	if h.deps.SyncJobs != nil {
		_ = h.deps.SyncJobs.SetFilesSynced(ctx, jobID, filesAdded)
	}

	// end-of-batch kick: single regen trigger after the whole sync batch.
	// Per-file commits intentionally omit coalescer.Kick; this one call
	// coalesces regen into a single post-batch run (D-08). No DB flag named
	// SkipMetadataRegen — caller-behaviour convention only.
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

// fetchAndCommit downloads ent into the path store and commits the row.
// Returns the number of bytes downloaded (0 on idempotency skip).
func (h *SyncHandler) fetchAndCommit(ctx context.Context, projectName string, repo *metadata.Repo, ent UpstreamEntry, creds AuthCreds, progress *jobs.ProgressWriter, step string, accumulatedDone *int64, totalBytes int64) (int64, error) {
	// HEAD-less fast-path: if upstream digest known and already present, skip.
	if ent.Digest != "" {
		if existing, ferr := h.deps.RPMPackages.FindByDigest(ctx, repo.ID, ent.Digest); ferr == nil && existing != nil {
			return 0, nil
		}
	}
	body, size, dgst, err := downloadAndHashWithProgress(ctx, h.deps.HTTPClient, ent.Path, creds, progress, step, accumulatedDone, totalBytes)
	if err != nil {
		return 0, fmt.Errorf("rpm_sync: download %s: %w", ent.Filename, err)
	}
	digest := "sha256:" + dgst
	if ent.Digest != "" && !strings.EqualFold(ent.Digest, digest) {
		return 0, fmt.Errorf("rpm_sync: digest mismatch on %s: upstream=%s computed=%s", ent.Filename, ent.Digest, digest)
	}
	// Re-check via computed digest.
	if existing, ferr := h.deps.RPMPackages.FindByDigest(ctx, repo.ID, digest); ferr == nil && existing != nil {
		return 0, nil
	}

	// F-06.1 extension (Codex follow-up): upstream repositories occasionally
	// publish an href/NEVRA mismatch (e.g. packages/foo.rpm whose internal
	// header says centos-release-7-…). If we stored + indexed at ent.Filename
	// here, regen would emit <location href="packages/<canonicalFilename>">
	// (NEVRA-derived) and every dnf client would 404 the fetch. Stage the
	// download to a tmp path, parse, then commit storage + DB entries under
	// the canonical NEVRA filename so every published href points at a
	// file that actually exists.
	tmpDir := filepath.Join(h.deps.RepoRoot, ".tmp-rpm-sync")
	if err := os.MkdirAll(tmpDir, 0o750); err != nil {
		return 0, fmt.Errorf("rpm_sync: mkdir tmp %s: %w", ent.Filename, err)
	}
	tmpFile, err := os.CreateTemp(tmpDir, "rpm-sync-*.rpm")
	if err != nil {
		return 0, fmt.Errorf("rpm_sync: tmp %s: %w", ent.Filename, err)
	}
	tmpPath := tmpFile.Name()
	// Belt-and-braces: always delete the staging copy on the way out —
	// succeeded or not. Storage Put reads the body buffer directly (not
	// the file) so the canonical storage is independent of tmpPath.
	defer func() { _ = os.Remove(tmpPath) }()
	if _, err := tmpFile.Write(body); err != nil {
		_ = tmpFile.Close()
		return 0, fmt.Errorf("rpm_sync: tmp write %s: %w", ent.Filename, err)
	}
	if err := tmpFile.Close(); err != nil {
		return 0, fmt.Errorf("rpm_sync: tmp close %s: %w", ent.Filename, err)
	}

	parsed, perr := Parse(tmpPath)
	if perr != nil {
		return 0, fmt.Errorf("rpm_sync: parse %s: %w", ent.Filename, perr)
	}

	canonicalName := parsed.canonicalFilename()
	storageKey := storageKeyFor(projectName, repo.Name, canonicalName)
	if _, err := h.deps.Path.Put(ctx, storageKey, openBytesReader(body)); err != nil {
		return 0, fmt.Errorf("rpm_sync: store %s: %w", canonicalName, err)
	}
	abs := filepath.Join(h.deps.RepoRoot, filepath.FromSlash(storageKey))

	if err := h.deps.DB.WriteTx(ctx, func(tx *sql.Tx) error {
		if _, err := h.deps.RPMPackages.Insert(ctx, tx, &metadata.RPMPackage{
			RepoID:      repo.ID,
			Name:        parsed.Name,
			Epoch:       parsed.Epoch,
			Version:     parsed.Version,
			Release:     parsed.Release,
			Arch:        parsed.Arch,
			Summary:     parsed.Summary,
			Description: parsed.Description,
			License:     parsed.License,
			URL:         parsed.URL,
			SourceRPM:   parsed.SourceRPM,
			SizeBytes:   size,
			Digest:      digest,
			Filename:    canonicalName,
		}); err != nil {
			return err
		}
		if err := metadata.IndexRPMDelete(ctx, tx, repo.ID, parsed.Name, parsed.Version, parsed.Arch); err != nil {
			return err
		}
		if err := metadata.IndexRPM(ctx, tx, repo.ID, parsed.Name, parsed.Version, parsed.Arch, parsed.Summary); err != nil {
			return err
		}
		if repo.AutoScan && h.deps.Scans != nil {
			if _, err := h.deps.Scans.Enqueue(ctx, tx, repo.ID, "rpm", canonicalName); err != nil {
				return err
			}
		}
		// NOTE: per-file Insert intentionally does NOT call coalescer.Kick.
		// The single end-of-batch kick happens in Handle after parse+wait.
		return h.deps.Repos.SetMetadataState(ctx, tx, repo.ID, metadata.MetadataStateDirty)
	}); err != nil {
		_ = os.Remove(abs)
		return 0, fmt.Errorf("rpm_sync: commit %s: %w", canonicalName, err)
	}
	return size, nil
}

// downloadAndHashWithProgress GETs urlStr with creds, hashes the body
// inline, and returns the bytes + size + hex digest. When progress is
// non-nil, the body is wrapped with jobs.CountingReader so every non-zero
// read advances *accumulatedDone (atomic under parallel downloads) and
// triggers a throttled progress.Set.
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
	var reader = io.TeeReader(resp.Body, hasher)
	if progress != nil && accumulatedDone != nil {
		reader = &jobs.CountingReader{R: reader, OnRead: func(n int) {
			done := atomic.AddInt64(accumulatedDone, int64(n))
			_ = progress.Set(ctx, step, done, total)
		}}
	}
	// STREAMIO-06 (audit #4): fail-explicit on cap+1 instead of the
	// previous silent-truncation idiom (full-body read through a
	// LimitReader). The cap is a package-level var (not a const) only
	// so tests can shrink it; no production caller mutates it.
	body, err := streamio.ReadAllLimited(reader, maxArtifactBytes, streamio.ErrArtifactTooLarge)
	if err != nil {
		return nil, 0, "", fmt.Errorf("read %s: %w", urlStr, err)
	}
	return body, int64(len(body)), hex.EncodeToString(hasher.Sum(nil)), nil
}

// maxArtifactBytes caps the per-artifact upstream body for mirror sync
// downloads. Test-overridable (var, not const) so cap+1 oversized-upstream
// regression guards can run without serving multi-GiB bodies.
var maxArtifactBytes int64 = 4 * 1024 * 1024 * 1024

// openBytesReader is a tiny helper so callers can pass a []byte to a
// PathStore.Put without an inline import dance.
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

// truncateErr keeps last_error reasonable for sync_jobs storage.
func truncateErr(s string) string {
	const max = 1024
	if len(s) <= max {
		return s
	}
	return s[:max] + "...[truncated]"
}

// PayloadFromBytes is a small parse helper used by the REST endpoint to
// validate a sync request body before enqueue.
func PayloadFromBytes(body []byte) (SyncPayload, error) {
	var pl SyncPayload
	if err := json.Unmarshal(body, &pl); err != nil {
		return pl, errors.New("invalid JSON")
	}
	return pl, nil
}
