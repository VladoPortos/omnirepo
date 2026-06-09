// Package pypi — sync handler.
//
// PyPI idempotency uses FindByFilename (not FindByDigest) because the
// pypi_files schema constraint is UNIQUE(repo_id, filename) and PEP 691
// responses sometimes omit the SHA256 entirely. The handler does
// still verify the digest when upstream supplied one.
package pypi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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

// errParseSkip marks a fetchAndCommit error as originating from a filename
// parse failure (PEP 440 Validate gate in parseSdistFilename /
// parseWheelFilename). The outer sync loop recognises this sentinel via
// errors.Is and emits EvtSyncFileSkipped + continues rather than failing
// the whole sync. Package-local + unexported so no other error path can
// wear this mask by accident.
var errParseSkip = errors.New("pypi_sync: parse skip")

// SyncJobKind is the sync_jobs.kind value routed to SyncHandler.Handle.
const SyncJobKind = "pypi_sync"

// SyncPayload is the job payload shape.
type SyncPayload struct {
	UpstreamURL string      `json:"upstream_url"`
	CredID      *int64      `json:"cred_id,omitempty"`
	Filter      *SyncFilter `json:"filter,omitempty"`
	// Operator-confirmed override of the percent-threshold drift-purge
	// guard. Threaded through from the /sync REST body. Set true after
	// the operator clicked "Override and purge" on a previous sync that
	// returned summary.drift_blocked.
	ForceDriftThreshold bool `json:"force_drift_threshold,omitempty"`
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
	// Sync-jobs repo for throttled byte-level progress emit. Nil-safe —
	// if unwired, progress.Set is a no-op.
	SyncJobs *metadata.SyncJobsRepo
	// Trash is the soft-delete primitive used by drift purge to move
	// drifted file blobs into the trash root before deleting their
	// pypi_files row. Nil-safe — when nil, drift purge is structurally
	// skipped even if repo.DriftPurge is true.
	Trash storage.Trash
}

// SyncHandler is the sync-pool handler for kind="pypi_sync".
type SyncHandler struct{ deps SyncDeps }

// NewSyncHandler constructs a handler.
func NewSyncHandler(deps SyncDeps) *SyncHandler { return &SyncHandler{deps: deps} }

// Handle runs one pypi_sync job.
//
// jobID threads through so the handler can emit byte-level progress via
// jobs.ProgressWriter. totalBytes is pre-computed from summed PEP 691
// file.size so the progress bar has a stable denominator throughout the
// sync.
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

	// Collect all project/file pairs first so we can emit byte-level
	// progress with a stable totalBytes denominator (sum of PEP 691
	// file.size entries). Filter + idempotency checks run in the collect
	// pass so already-present files don't inflate total.
	//
	// The same loop also captures the full upstream key set (every accepted
	// upstream file, not just the to-fetch subset) so the end-of-Handle
	// drift step can compute local\upstream without re-parsing. Digest is
	// the bare hex from PEP 691 hashes.sha256; we re-prefix to
	// "sha256:<hex>" so the projection matches the digest stored on
	// pypi_files rows (the PyPI key C slot).
	type fileToFetch struct {
		project string
		file    UpstreamFile
	}
	var (
		toFetch        []fileToFetch
		upstreamKeys   []driftpurge.Key
		totalBytes     int64
		downloadErrors []error
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
			// Upstream pypi.org still lists legacy .egg / .exe / .msi
			// entries for some projects. pip hasn't installed from any of
			// them via simple/ since 2017, and the inline sync-path version
			// parser below only strips .gz/.tar/.zip — so a .egg filename
			// tail ends up as the "version" column. Skip here before we
			// ever enqueue the download so the mirror stays clean (sdist
			// parse invariant in parse.go:202).
			if !isInstallableExt(f.Filename) {
				continue
			}
			// The filename becomes the last path segment under
			// {proj}/pypi/{repo}/packages/ and the key the simple-index
			// uses for href generation. Reject hostile shapes (path
			// separators, control chars, quotes) before they touch
			// PathStore or the DB.
			if !isSafeMirrorFilename(f.Filename) {
				continue
			}
			// Record this upstream file's drift key (PyPI projection:
			// {project_normalized, filename, digest}). Locally-stored rows
			// carry the "sha256:<hex>" digest form; re-prefix the bare
			// PEP 691 hex so set membership matches.
			var keyDigest string
			if f.SHA256 != "" {
				keyDigest = "sha256:" + strings.ToLower(f.SHA256)
			}
			upstreamKeys = append(upstreamKeys, driftpurge.Key{
				A: project,
				B: f.Filename,
				C: keyDigest,
			})
			// Idempotency by filename — matches pypi_files UNIQUE(repo_id, filename).
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
				// Filename parse failures from fetchAndCommit carry the
				// errParseSkip sentinel so the outer loop can emit
				// EvtSyncFileSkipped and continue rather than failing the
				// whole sync. Non-parse errors (HTTP, disk, tx,
				// digest-mismatch) fall through to downloadErrors unchanged.
				if errors.Is(derr, errParseSkip) {
					if h.deps.Audit != nil {
						// Best-effort emit — audit failure must never mask
						// a sync outcome.
						_ = h.deps.Audit.Record(ctx, audit.Event{
							Kind:       audit.EvtSyncFileSkipped,
							TargetKind: "repo",
							TargetID:   strconv.FormatInt(repo.ID, 10),
							Details: map[string]any{
								"filename":     tf.file.Filename,
								"reason":       reasonFromErr(derr),
								"protocol":     "pypi",
								"upstream_url": tf.file.URL,
								"repo_id":      repo.ID,
							},
						})
					}
					return
				}
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
		// Per-file commits flipped metadata_state=dirty; without a
		// failure-path Kick the PEP 503 simple index pages stay behind the
		// DB until the next successful sync. Regen is idempotent — always
		// safe.
		if h.deps.Coalescer != nil {
			h.deps.Coalescer.Get(repoID).Kick()
		}
		return h.fail(ctx, repoID, pl, startedAt, httpx.SanitizeUpstreamErr(downloadErrors[0]))
	}

	_ = progress.Set(ctx, "done", atomic.LoadInt64(&accumulatedDone), totalBytes)

	// Drift purge. Runs after upload success and before SetFilesSynced.
	// Failed syncs return via h.fail(...) earlier in the function, so this
	// step is structurally unreachable on partial-sync paths. Only fires
	// when the repo has the per-mirror toggle on and Trash is wired
	// (test/dev paths that omit Trash structurally skip drift).
	if repo.DriftPurge && h.deps.Trash != nil {
		adapter := driftpurge.NewPyPIAdapter(
			upstreamKeys,
			h.deps.PyPIFiles,
			h.deps.Trash,
			func(row *metadata.PyPIFile) string {
				// Mirror the ingest path used by fetchAndCommit:
				// {project}/pypi/{repo}/packages/{filename}.
				key := strings.Join([]string{proj.Name, "pypi", repo.Name, "packages", row.Filename}, "/")
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
		// on-disk file (recoverable by GC), never a missing artifact.
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
	// "Sync complete · N files · X MB". Safe to read filesAdded without
	// the lock here — wg.Wait() above synced all goroutines.
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

	// Filenames flow through parseWheelFilename / parseSdistFilename
	// (parse.go) which Validate the version slot against pep440.go. The
	// parse runs BEFORE the download so a malformed upstream filename
	// never triggers an HTTP GET or leaves an orphan blob on disk —
	// otherwise the post-Put parse-failure path leaks storage. Validation
	// failures are wrapped with the errParseSkip sentinel so the outer
	// loop emits EvtSyncFileSkipped and continues rather than failing the
	// whole sync. isInstallableExt already filters the legacy bdist forms
	// earlier in the collect pass.
	var (
		kind    string
		version string
	)
	if strings.HasSuffix(strings.ToLower(f.Filename), ".whl") {
		kind = "wheel"
		_, v, perr := parseWheelFilename(f.Filename)
		if perr != nil {
			return 0, fmt.Errorf("%w: %w", errParseSkip, perr)
		}
		version = v
	} else {
		kind = "sdist"
		_, v, perr := parseSdistFilename(f.Filename)
		if perr != nil {
			return 0, fmt.Errorf("%w: %w", errParseSkip, perr)
		}
		version = v
	}

	body, size, dgst, err := upstreamfetch.DownloadAndHash(ctx, h.deps.HTTPClient, f.URL, creds, progress, step, accumulatedDone, totalBytes, maxArtifactBytes)
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

// maxArtifactBytes caps the per-artifact upstream body for mirror sync
// downloads. Test-overridable (var, not const) so cap+1 oversized-upstream
// regression guards can run without serving multi-GiB bodies.
var maxArtifactBytes int64 = 2 * 1024 * 1024 * 1024

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

// reasonFromErr maps an errParseSkip-wrapped error to the audit reason
// enum documented alongside EvtSyncFileSkipped (internal/audit/events.go).
//
// Two values are produced:
//   - "pep440_invalid"  — filename's version slot failed pep440.Validate
//     (produced by parseSdistFilename exhausting all -<digit> boundaries
//     or by parseWheelFilename's parts[1] failing Validate). Error shapes:
//     "pypi: malformed sdist filename: <base>" or
//     "pypi: malformed wheel filename: <base>".
//   - "unsupported_ext" — sdist filename has no .tar.gz/.tgz/.zip suffix.
//     Error shape: "pypi: unsupported sdist extension: <base>". Today
//     this path is filtered earlier by isInstallableExt in the collect
//     pass so the reason is unlikely to surface; documented here so the
//     enum stays consistent with the declared event contract.
//
// Unknown shapes conservatively return "pep440_invalid" — the parse
// layer only produces these three shapes today, so an unknown shape
// indicates a new parser error path that should surface via the most
// specific reason until the enum is widened.
func reasonFromErr(err error) string {
	if err == nil {
		return "pep440_invalid"
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "pypi: unsupported sdist extension:"):
		return "unsupported_ext"
	case strings.Contains(msg, "pypi: malformed sdist filename:"),
		strings.Contains(msg, "pypi: malformed wheel filename:"):
		return "pep440_invalid"
	default:
		return "pep440_invalid"
	}
}
