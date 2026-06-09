// Package rpm — sync handler.
//
// Registered on the SyncPool under kind="rpm_sync". Drives the upstream
// parser, fetches missing .rpm files into the path store, inserts
// rpm_packages + rpm_fts rows in batch (per-file writer tx, NO per-file
// coalescer.Kick), then performs ONE end-of-batch coalescer.Kick so the
// debounced regen runs once per sync batch.
package rpm

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"sync/atomic"

	"github.com/vladoportos/omnirepo/internal/audit"
	"github.com/vladoportos/omnirepo/internal/config"
	"github.com/vladoportos/omnirepo/internal/driftpurge"
	"github.com/vladoportos/omnirepo/internal/httpx"
	"github.com/vladoportos/omnirepo/internal/jobs"
	"github.com/vladoportos/omnirepo/internal/metadata"
	"github.com/vladoportos/omnirepo/internal/protocol/regen"
	"github.com/vladoportos/omnirepo/internal/protocol/upstreamfetch"
	"github.com/vladoportos/omnirepo/internal/storage"
)

// SyncJobKind is the sync_jobs.kind value routed to SyncHandler.Handle.
const SyncJobKind = "rpm_sync"

// SyncPayload is the JSON shape persisted in sync_jobs.payload_json.
type SyncPayload struct {
	UpstreamURL string      `json:"upstream_url"`
	CredID      *int64      `json:"cred_id,omitempty"`
	Filter      *SyncFilter `json:"filter,omitempty"`
	// Operator-confirmed override of the percent-threshold drift-purge
	// guard. See pypi.SyncPayload for full docs — same shape across the
	// four mirror protocols.
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
	// Sync-jobs repo for throttled byte-level progress emit. Nil-safe —
	// if unwired, progress.Set is a no-op.
	SyncJobs *metadata.SyncJobsRepo
	// Trash is the soft-delete primitive used by drift purge to move
	// drifted .rpm blobs to the trash root before deleting their
	// rpm_packages row. Nil-safe — when nil, drift purge is structurally
	// skipped even if repo.DriftPurge is true.
	Trash storage.Trash
}

// SyncHandler is the sync-pool handler for kind="rpm_sync".
type SyncHandler struct{ deps SyncDeps }

// NewSyncHandler constructs a handler.
func NewSyncHandler(deps SyncDeps) *SyncHandler { return &SyncHandler{deps: deps} }

// Handle implements the jobs.JobHandler signature
// (ctx, payload string, projectID, repoID, jobID int64) -> error.
//
// jobID threads through so the handler can emit byte-level progress via
// jobs.ProgressWriter.
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
		// Emit upstream_cred.used ONCE per job.
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

	// Collect entries first so the progress bar has a stable totalBytes
	// denominator (sum of primary.xml <size package="..."/> values).
	// Keeps idempotency-by-digest filtering in the collect pass to avoid
	// listing already-present rows as "to-download".
	//
	// The SAME pass also captures the full upstream key set (every
	// accepted upstream entry, not just the to-fetch subset) so the
	// end-of-Handle drift step can compute local\upstream without
	// re-parsing primary.xml. RPM projection: {name, version, arch} —
	// coarser than DB UNIQUE NEVRA to tolerate NEVRA href mismatches.
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
		// Per-file commits flipped metadata_state=dirty; without a
		// failure-path Kick the repomd/primary.xml stays behind the DB
		// until the next successful sync. Regen is idempotent — always safe.
		if h.deps.Coalescer != nil {
			h.deps.Coalescer.Get(repoID).Kick()
		}
		return h.fail(ctx, repoID, pl, startedAt, httpx.SanitizeUpstreamErr(downloadErrors[0]))
	}

	// Terminal progress emit so Flush writes "done" with accumulated bytes.
	_ = progress.Set(ctx, "done", atomic.LoadInt64(&accumulatedDone), totalBytes)

	// Drift purge. Runs after upload success and before SetFilesSynced.
	// Failed syncs return via h.fail(...) earlier in the function, so
	// this step is structurally unreachable on partial-sync paths.
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
		var pending []driftpurge.PendingMove
		if err := h.deps.DB.WriteTx(ctx, func(tx *sql.Tx) error {
			var rerr error
			report, pending, rerr = driftpurge.Run(
				ctx, tx, repo.ID, "", adapter,
				h.deps.Cfg.DriftPurgeThresholdPct, pl.ForceDriftThreshold,
			)
			return rerr
		}); err != nil {
			return h.fail(ctx, repo.ID, pl, startedAt, fmt.Errorf("drift purge: %w", err))
		}

		// Perform the deferred trash moves only AFTER the purge tx commits, so
		// a rolled-back purge can never strand a restored row against an
		// already-trashed file. Best-effort: a failure leaves an orphaned
		// on-disk file (a storage leak), never a missing artifact.
		if err := driftpurge.ApplyPendingMoves(ctx, pending); err != nil {
			slog.WarnContext(ctx, "driftpurge.trash_move_failed", "err", err, "repo_id", repo.ID)
		}

		driftpurge.EmitReportAudit(ctx, driftpurge.AuditSink{
			Audit:        h.deps.Audit,
			SyncJobs:     h.deps.SyncJobs,
			ThresholdPct: h.deps.Cfg.DriftPurgeThresholdPct,
		}, report, repo.ID, jobID, pl.UpstreamURL)
	}

	// Persist per-job file count once so the UI pill can render
	// "Sync complete · N files · X MB". wg.Wait() synced all goroutines.
	if h.deps.SyncJobs != nil {
		_ = h.deps.SyncJobs.SetFilesSynced(ctx, jobID, filesAdded)
	}

	// end-of-batch kick: single regen trigger after the whole sync batch.
	// Per-file commits intentionally omit coalescer.Kick; this one call
	// coalesces regen into a single post-batch run. No DB flag named
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
	body, size, dgst, err := upstreamfetch.DownloadAndHash(ctx, h.deps.HTTPClient, ent.Path, creds, progress, step, accumulatedDone, totalBytes, maxArtifactBytes)
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

	// Upstream repositories occasionally publish an href/NEVRA mismatch
	// (e.g. packages/foo.rpm whose internal
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
