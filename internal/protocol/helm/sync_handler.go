// Package helm — sync handler.
package helm

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

	"github.com/vladoportos/omnirepo/internal/audit"
	"github.com/vladoportos/omnirepo/internal/auth"
	"github.com/vladoportos/omnirepo/internal/config"
	"github.com/vladoportos/omnirepo/internal/driftpurge"
	"github.com/vladoportos/omnirepo/internal/httpx"
	"github.com/vladoportos/omnirepo/internal/jobs"
	"github.com/vladoportos/omnirepo/internal/metadata"
	"github.com/vladoportos/omnirepo/internal/protocol/helm/ociclient"
	"github.com/vladoportos/omnirepo/internal/protocol/regen"
	"github.com/vladoportos/omnirepo/internal/protocol/upstreamfetch"
	"github.com/vladoportos/omnirepo/internal/storage"
)

// SyncJobKind is the sync_jobs.kind value routed to SyncHandler.Handle.
const SyncJobKind = "helm_sync"

// SyncPayload is the job payload shape.
type SyncPayload struct {
	UpstreamURL string      `json:"upstream_url"`
	CredID      *int64      `json:"cred_id,omitempty"`
	Filter      *SyncFilter `json:"filter,omitempty"`
	// Operator-confirmed override of the percent-threshold drift-purge
	// guard. See pypi.SyncPayload for full docs.
	ForceDriftThreshold bool `json:"force_drift_threshold,omitempty"`
}

// SyncDeps bundles dependencies for the Helm sync handler.
type SyncDeps struct {
	DB         *metadata.DB
	Path       storage.PathStore
	HelmCharts *metadata.HelmChartsRepo
	Repos      *metadata.ReposRepo
	Projects   *metadata.ProjectsRepo
	Creds      *metadata.UpstreamCredsRepo
	Scans      *metadata.ScansRepo
	Audit      audit.Logger
	Coalescer  *regen.Registry
	HTTPClient *http.Client
	RepoRoot   string
	Cfg        config.SyncConfig
	// sync-jobs repo for step-based progress emit. Helm is step-based
	// (index.yaml lacks chart sizes), so total_bytes is always 0 and
	// progress_bytes counts completed charts.
	SyncJobs *metadata.SyncJobsRepo
	// OCIClient is the Helm OCI registry client used when UpstreamEntry.Source
	// is EntrySourceOCI. Injected from phase3_sync.wireSync with the shared
	// HTTPClient. Isolated behind a narrow interface so
	// sync_handler_test.go can use ociclient.NewFake() for hermetic coverage,
	// and consumed to drive a real OCI pull + tag-rebound branch.
	OCIClient ociclient.Client
	// Trash is the soft-delete primitive used by the OCI tag-rebound handler
	// to move the prior chart's on-disk file under
	// <root>/trash/<ts>-oci_tag_rebound-<id>/ BEFORE inserting the
	// replacement helm_charts row. Nil in tests that do not exercise the
	// rebound path — fetchAndCommitOCI no-ops the trash step when nil.
	Trash storage.Trash
}

// SyncHandler is the sync-pool handler for kind="helm_sync".
type SyncHandler struct{ deps SyncDeps }

// NewSyncHandler constructs a handler.
func NewSyncHandler(deps SyncDeps) *SyncHandler { return &SyncHandler{deps: deps} }

// Handle runs one helm_sync job.
//
// jobID threads through so the handler can emit step-based progress via
// jobs.ProgressWriter. total_bytes stays 0 throughout: Helm's index.yaml
// doesn't expose chart sizes, so the UI renders
// "chart N of M · name-version.tgz" and can't show a byte-level bar.
// progress_bytes is the 1-based chart index for completed charts.
func (h *SyncHandler) Handle(ctx context.Context, payload string, projectID, repoID, jobID int64) error {
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
		creds = AuthCreds{User: user, Password: pw, Token: tok, BoundScheme: u.Scheme, BoundHost: host}
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

	// Collect entries first so the step format knows the total chart
	// count. Helm is step-based — total_bytes stays 0.
	//
	// oci:// entries are no longer skipped. ParseUpstream tags each
	// UpstreamEntry with Source (EntrySourceHTTP / EntrySourceOCI /
	// EntrySourceUnknown); the per-entry dispatch in fetchAndCommit
	// branches on the tag. The skipped_non_http_entries counter is
	// retained for any unclassified entry (should be zero in normal
	// operation — ParseUpstream only yields entries whose v.URLs[0]
	// parses; classifyEntryPath sets Source from the scheme). The legacy
	// skipped_oci_entries counter is retired: the audit detail is
	// preserved as a 0-valued field on the progress event when any
	// non-http skipping actually occurred, so downstream dashboards don't
	// break.
	var (
		entries       []UpstreamEntry
		upstreamKeys  []driftpurge.Key
		skippedOtherP int
	)
	collectFn := func(ent UpstreamEntry) error {
		// Record the chart's drift key regardless of transport (HTTP or
		// OCI) and regardless of whether the chart is already cached
		// locally. The drift step compares the FULL upstream key set
		// against locally-stored helm_charts rows. Helm projection:
		// Key{A: name, B: version, C: ""}.
		var name, version string
		if ent.Metadata != nil {
			name = ent.Metadata.Name
			version = ent.Metadata.Version
		}
		// OCI tags do not always populate Metadata pre-pull; fall back to
		// parseOCIRef so OCI drift detection still has a key set.
		if ent.Source == EntrySourceOCI && (name == "" || version == "") {
			if n, v := parseOCIRef(ent.Path); n != "" && v != "" {
				name, version = n, v
			}
		}
		if name != "" && version != "" {
			upstreamKeys = append(upstreamKeys, driftpurge.Key{
				A: name,
				B: version,
				C: "",
			})
		}
		if ent.Digest != "" {
			if existing, ferr := h.deps.HelmCharts.FindByDigest(ctx, repoID, ent.Digest); ferr == nil && existing != nil {
				return nil
			}
		}
		// Only yield entries with a recognized transport. OCI + HTTP both
		// dispatch from fetchAndCommit based on ent.Source; anything else
		// (e.g. file://, ftp://) is counted and dropped.
		if ent.Source != EntrySourceHTTP && ent.Source != EntrySourceOCI {
			skippedOtherP++
			return nil
		}
		entries = append(entries, ent)
		return nil
	}
	var parseErr error
	if strings.HasPrefix(strings.ToLower(pl.UpstreamURL), "oci://") {
		// Pure-OCI top-level helm upstream: enumerate tags via OCIClient
		// and synthesize UpstreamEntry per semver tag. This path is needed
		// because the validator already accepts oci:// but the HTTP path
		// would fail on "oci://.../index.yaml".
		_, parseErr = ParseOCIUpstream(ctx, h.deps.OCIClient, pl.UpstreamURL, creds, *pl.Filter, collectFn)
	} else {
		_, parseErr = ParseUpstream(ctx, h.deps.HTTPClient, pl.UpstreamURL, creds, *pl.Filter, collectFn)
	}
	if parseErr != nil {
		return h.fail(ctx, repoID, pl, startedAt, httpx.SanitizeUpstreamErr(parseErr))
	}
	if skippedOtherP > 0 && h.deps.Audit != nil {
		_ = h.deps.Audit.Record(ctx, audit.Event{
			Kind:       audit.EvtSyncProgress,
			TargetKind: "repo",
			TargetID:   strconv.FormatInt(repoID, 10),
			Details: map[string]any{
				// skipped_oci_entries now always 0 at steady state. Kept
				// for dashboard backward-compat (value stays 0 for
				// eligible upstreams).
				"skipped_oci_entries":      0,
				"skipped_non_http_entries": skippedOtherP,
				"upstream_url":             pl.UpstreamURL,
				"note":                     "non-http/oci entries skipped (unsupported transport)",
			},
		})
	}

	progress := jobs.NewProgressWriter(h.deps.SyncJobs, jobID)
	defer func() { _ = progress.Flush(ctx) }()

	totalCharts := len(entries)
	sem := make(chan struct{}, maxParallel)
	var (
		mu             sync.Mutex
		filesAdded     int64
		bytesDownload  int64
		downloadErrors []error
		wg             sync.WaitGroup
	)

	for i, ent := range entries {
		// ctx-cancellation gate. Stop dispatching new charts once ctx is
		// cancelled; already-dispatched goroutines drain via the wg.Wait()
		// below and may still commit before the final persisted-count read
		// under mu.Lock. MUST sit as the FIRST statement of the loop body
		// so the `sem <- struct{}{}` channel-send on a full semaphore
		// cannot deadlock post-cancel.
		if ctx.Err() != nil {
			break
		}
		i, ent := i, ent
		// Emit step-per-chart BEFORE dispatch so the UI sees the currently
		// downloading chart label rather than a stale "chart N-1 of M".
		// total=0 is Helm's convention; done counts completed + in-flight
		// (1-based) so UI renders "X of Y".
		step := fmt.Sprintf("chart %d of %d · %s", i+1, totalCharts, ent.Filename)
		_ = progress.Set(ctx, step, int64(i+1), 0)
		// Semaphore acquire must honour ctx. With MaxParallelDownloadsPerJob=N
		// the sem-send blocks until an in-flight goroutine finishes; if ctx
		// is cancelled while we wait, breaking here prevents dispatching
		// ANOTHER goroutine for a chart we've already decided to abandon.
		// Second ctx-gate (post-sem-take, pre-dispatch) catches the race
		// where the ctx was cancelled WHILE this goroutine held the sem
		// slot in-flight.
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			size, derr := h.fetchAndCommit(ctx, proj.Name, repo, ent, creds)
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

	// Read filesAdded under the same mutex that guards it. wg.Wait()
	// fences all worker writes, but -race still flags the read without the
	// Lock when some goroutine's final store-before-Done is
	// Go-memory-model-observed on the main goroutine through the WaitGroup
	// counter. Cheap belt-and-braces — validated by
	// `go test -race -count=10` on the ctx-cancel test.
	mu.Lock()
	persisted := filesAdded
	mu.Unlock()

	// Partial-sync path #1: ctx canceled mid-flight (live path).
	// Goroutines may have committed some charts before cancellation
	// propagated; report the exact persisted count so the jobs pool can
	// route to terminal-failed with the right accounting.
	if ctx.Err() != nil {
		// Kick regen even on partial. Any charts that landed before cancel
		// flipped metadata_state=dirty; skipping the kick leaves
		// index.yaml behind the DB.
		if h.deps.Coalescer != nil {
			h.deps.Coalescer.Get(repoID).Kick()
		}
		return h.fail(ctx, repoID, pl, startedAt,
			newPartialSyncErr(persisted, int64(totalCharts), ctx.Err()))
	}

	if len(downloadErrors) > 0 {
		// Failed syncs must still kick regen. Any charts that landed
		// before the error stay on disk + in helm_charts; the per-entry
		// commit flips metadata_state=dirty (fetchAndCommitOCI /
		// fetchAndCommit HTTP path). Skipping the Kick here leaves
		// index.yaml behind the DB until a subsequent successful sync
		// happens to run — easily 6h+ under a Docker Hub rate limit.
		// Regen is idempotent and cheap; no filesAdded gate.
		if h.deps.Coalescer != nil {
			h.deps.Coalescer.Get(repoID).Kick()
		}
		sanitized := httpx.SanitizeUpstreamErr(downloadErrors[0])
		// Partial-sync path #2: upstream 500 (or any download error)
		// before all charts persisted. Wrap the first download error as
		// cause so callers can still reach the HTTP-status-bearing error
		// via errors.Unwrap on PartialSyncErr.cause.
		if persisted < int64(totalCharts) {
			return h.fail(ctx, repoID, pl, startedAt,
				newPartialSyncErr(persisted, int64(totalCharts), sanitized))
		}
		return h.fail(ctx, repoID, pl, startedAt, sanitized)
	}

	// Terminal emit so Flush shows completion; progress_bytes = totalCharts.
	_ = progress.Set(ctx, "done", int64(totalCharts), 0)

	// Drift purge. Runs after upload success and before SetFilesSynced.
	// The partial-sync paths above (ctx-cancel + downloadErrors) return
	// via h.fail(...) BEFORE this point, so the drift step is structurally
	// unreachable on failed/partial syncs. Both HTTP and OCI helm
	// upstreams flow through here.
	if repo.DriftPurge && h.deps.Trash != nil {
		adapter := driftpurge.NewHelmAdapter(
			upstreamKeys,
			h.deps.HelmCharts,
			h.deps.Trash,
			func(row *metadata.HelmChart) string {
				// Mirror the ingest path: {project}/helm/{repo}/charts/{filename}.
				key := strings.Join([]string{proj.Name, "helm", repo.Name, "charts", row.Filename}, "/")
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
	// "Sync complete · N files". wg.Wait() synced all goroutines.
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
		details := map[string]any{
			"last_error":   truncateErr(err.Error()),
			"upstream_url": pl.UpstreamURL,
			"duration_ms":  time.Since(started).Milliseconds(),
		}
		// Mirror partial-sync counts into EvtSyncFailed so audit
		// observability matches the sync_jobs.log JSON. Only
		// *PartialSyncErr satisfies errors.As — generic errors cannot
		// wear the partial mask. Assignment spelled with a JSON-style key
		// ("partial": true).
		var pse *PartialSyncErr
		if errors.As(err, &pse) {
			for k, v := range map[string]any{
				"partial":         true,
				"files_persisted": pse.Persisted(),
				"files_expected":  pse.Expected(),
			} {
				details[k] = v
			}
		}
		_ = h.deps.Audit.Record(ctx, audit.Event{
			Kind:       audit.EvtSyncFailed,
			TargetKind: "repo",
			TargetID:   strconv.FormatInt(repoID, 10),
			Details:    details,
		})
	}
	return err
}

func (h *SyncHandler) fetchAndCommit(ctx context.Context, projectName string, repo *metadata.Repo, ent UpstreamEntry, creds AuthCreds) (int64, error) {
	// Dispatch on UpstreamEntry.Source. HTTP path falls through to the
	// existing implementation below unchanged; OCI entries route through
	// fetchAndCommitOCI which handles tag-rebound detection, soft-delete
	// via Trash.Move with kind "oci_tag_rebound", EvtOciTagRebound
	// emission with the details shape, and dedup on
	// (repo_id, name, version, digest) via HelmCharts.FindByNameVersion.
	if ent.Source == EntrySourceOCI {
		return h.fetchAndCommitOCI(ctx, projectName, repo, ent, creds)
	}
	if ent.Digest != "" {
		if existing, ferr := h.deps.HelmCharts.FindByDigest(ctx, repo.ID, ent.Digest); ferr == nil && existing != nil {
			return 0, nil
		}
	}
	tmpFile, size, dgst, err := upstreamfetch.DownloadToTemp(ctx, h.deps.HTTPClient, ent.Path, creds,
		filepath.Join(h.deps.RepoRoot, ".tmp-helm-sync"), nil, "", nil, 0, maxArtifactBytes)
	if err != nil {
		return 0, fmt.Errorf("helm_sync: download %s: %w", ent.Filename, err)
	}
	tmpPath := tmpFile.Name()
	defer func() {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
	}()
	digest := "sha256:" + dgst
	if ent.Digest != "" && !strings.EqualFold(ent.Digest, digest) {
		return 0, fmt.Errorf("helm_sync: digest mismatch on %s", ent.Filename)
	}
	if existing, ferr := h.deps.HelmCharts.FindByDigest(ctx, repo.ID, digest); ferr == nil && existing != nil {
		return 0, nil
	}

	storageKey := strings.Join([]string{projectName, "helm", repo.Name, "charts", ent.Filename}, "/")
	chart, perr := Parse(tmpPath)
	if perr != nil {
		return 0, fmt.Errorf("helm_sync: parse %s: %w", ent.Filename, perr)
	}

	if _, err := tmpFile.Seek(0, io.SeekStart); err != nil {
		return 0, fmt.Errorf("helm_sync: rewind %s: %w", ent.Filename, err)
	}
	_, err = h.deps.Path.Replace(ctx, storageKey, tmpFile, func(int64) error {
		return h.deps.DB.WriteTx(ctx, func(tx *sql.Tx) error {
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
			if repo.AutoScan && h.deps.Scans != nil {
				if _, err := h.deps.Scans.Enqueue(ctx, tx, repo.ID, "helm", ent.Filename); err != nil {
					return err
				}
			}
			return h.deps.Repos.SetMetadataState(ctx, tx, repo.ID, metadata.MetadataStateDirty)
		})
	})
	if err != nil {
		return 0, fmt.Errorf("helm_sync: commit %s: %w", ent.Filename, err)
	}
	return size, nil
}

// fetchAndCommitOCI handles a single oci:// upstream entry. Mirrors
// fetchAndCommit's HTTP-path commit-tail but swaps the fetch step for a
// Helm SDK OCI pull via ociclient.Client.
//
// Flow:
//  1. Nil-guard on deps.OCIClient — the SyncDeps wiring in
//     app.phase3_sync.wireSync always populates this, but tests that
//     construct SyncHandler directly may omit it. A nil client returns a
//     descriptive error rather than panicking.
//  2. Resolve the manifest digest pre-flight to gate bandwidth: if an
//     existing helm_charts row for (repo_id, name, version) already
//     carries this exact digest, skip the pull entirely (dedup).
//  3. Pull chart bytes only when the digest is new or different.
//  4. Tag-rebound handling when the existing row's digest differs from
//     the newly-pulled chart-layer digest:
//     - Soft-delete the prior on-disk tgz via Trash.Move with kind
//     "oci_tag_rebound" (distinct from the generic mirror-replaced
//     label; lets operators grep CVE-driven republication timelines
//     without a JOIN).
//     - Emit EvtOciTagRebound with the full details shape:
//     {name, version, old_digest, new_digest, upstream_url, repo_id,
//     replaced_at}.
//  5. Commit-tail — Put bytes → Parse → helm_charts.Insert (which does
//     UPSERT on (repo_id,name,version) so the replacement row lands in
//     place of the old one automatically).
func (h *SyncHandler) fetchAndCommitOCI(ctx context.Context, projectName string, repo *metadata.Repo, ent UpstreamEntry, creds AuthCreds) (int64, error) {
	if h.deps.OCIClient == nil {
		return 0, fmt.Errorf("helm_sync: OCIClient not wired; upstream %s requires OCI support", ent.Path)
	}

	// Derive (chartName, chartVersion) from the ref so we can run the
	// dedup lookup BEFORE spending a PullChart call. For Helm OCI refs
	// the tag IS the version; the last path segment before ':' is the
	// chart name. Example: "registry-1.docker.io/bitnamicharts/nginx:15.14.0"
	chartName, chartVersion := parseOCIRef(ent.Path)
	if chartName == "" || chartVersion == "" {
		return 0, fmt.Errorf("helm_sync: cannot parse name/version from oci ref %q", ent.Path)
	}

	ociCreds := ociclient.AuthCreds{User: creds.User, Password: creds.Password}

	// 1. Pre-flight manifest-digest resolve for dedup gate.
	manifestDigest, err := h.deps.OCIClient.Resolve(ctx, ent.Path, ociCreds)
	if err != nil {
		return 0, fmt.Errorf("helm_sync: resolve %s: %w", ent.Path, httpx.SanitizeUpstreamErr(err))
	}

	// 2. Dedup gate on (repo_id, name, version, digest). If the existing
	// row already carries the same manifest-or-chart-layer digest, skip.
	// We compare against the manifest digest first because that's what
	// Resolve returns; PullChart's Digest is the chart-layer digest
	// (typically different hex). Both are recorded consistently below.
	existing, ferr := h.deps.HelmCharts.FindByNameVersion(ctx, repo.ID, chartName, chartVersion)
	if ferr != nil && !errors.Is(ferr, metadata.ErrNotFound) {
		return 0, fmt.Errorf("helm_sync: find-by-name-version: %w", ferr)
	}
	if existing != nil && existing.Digest == manifestDigest {
		// Exact digest already cached; no pull, no audit.
		return 0, nil
	}

	// 3. Pull chart bytes. Helm SDK verifies layer digests internally via
	// ORAS Copy; we capture the result's chart-layer digest for dedup
	// semantics consistent with HTTP path (helm_charts.digest column is
	// the sha256 of the downloaded tgz bytes).
	res, err := h.deps.OCIClient.PullChart(ctx, ent.Path, ociCreds)
	if err != nil {
		// Defence in depth: the upstream may publish non-chart OCI
		// artifacts under the same tag namespace (Bitnami's `-metadata`
		// sidecar is filtered at ParseOCIUpstream but new conventions
		// may appear). Helm SDK bubbles these up as "manifest does not
		// contain minimum number of descriptors". Treat as a skip with
		// WARN so one malformed tag doesn't abort a 200-tag sync.
		if isNonChartManifestErr(err) {
			slog.WarnContext(ctx, "helm_sync: skipping non-chart OCI artifact",
				slog.Int64("repo_id", repo.ID),
				slog.String("ref", ent.Path),
				slog.Any("err", err),
			)
			return 0, nil
		}
		return 0, fmt.Errorf("helm_sync: pull %s: %w", ent.Path, httpx.SanitizeUpstreamErr(err))
	}
	if res == nil || len(res.Data) == 0 {
		return 0, fmt.Errorf("helm_sync: empty chart bytes for %s", ent.Path)
	}
	chartDigest := res.Digest
	if chartDigest == "" {
		chartDigest = manifestDigest
	}

	// Second dedup gate: the post-pull layer digest may match the stored
	// one even when the pre-flight manifest digest differed (e.g. Resolve
	// returned a manifest digest but the stored row captured a layer
	// digest from a prior pull). Cheap to double-check before writing.
	if existing != nil && existing.Digest == chartDigest {
		return 0, nil
	}

	// 4. Detect tag-rebound. State is captured here so the post-commit
	// audit emission below uses the OLD digest even after the upsert has
	// landed the NEW digest. The prior order (audit + trash BEFORE commit)
	// was a state-drift hazard — a failed upsert would leave the audit log
	// claiming a replacement that never happened, while the DB still
	// carried the prior digest.
	//
	// The new ordering keeps Trash.Move BEFORE Path.Put because Put
	// atomically rename-overwrites the canonical chart path (see
	// storage.WriteAndRename) — once Put runs, the old bytes are gone, so
	// we must move them to the trash holder first to preserve them.
	// However the AUDIT emission is deferred until AFTER commit succeeds:
	// audit is the operator's source of truth and must never claim a
	// replacement that did not commit. If commit fails after Trash.Move +
	// Put already ran, we compensate by Restoring the old file from trash
	// back to the canonical path and removing the partial new file.
	rebound := existing != nil && existing.Digest != chartDigest
	var (
		reboundOldDigest   string
		reboundOldFilename string
		reboundOldID       int64
	)
	if rebound {
		reboundOldDigest = existing.Digest
		reboundOldFilename = existing.Filename
		reboundOldID = existing.ID
	}

	// 5. Commit-tail — mirrors the HTTP path.
	filename := fmt.Sprintf("%s-%s.tgz", chartName, chartVersion)
	storageKey := strings.Join([]string{projectName, "helm", repo.Name, "charts", filename}, "/")

	// 5a. Pre-Put: if rebound + filename collision, move the old file to
	// trash first so Put does not silently clobber it. We capture the
	// trash holder path so a failed commit can compensate via Restore.
	var trashHolder string
	if rebound && h.deps.Trash != nil && reboundOldFilename != "" && reboundOldFilename == filename {
		actor := auth.ActorLoginFromContext(ctx)
		oldPath := filepath.Join(h.deps.RepoRoot, projectName, "helm", repo.Name, "charts", reboundOldFilename)
		holder, terr := h.deps.Trash.Move(ctx, oldPath, "oci_tag_rebound", reboundOldID, actor)
		if terr != nil && !errors.Is(terr, os.ErrNotExist) {
			return 0, fmt.Errorf("helm_sync: trash.Move on rebound: %w", terr)
		}
		trashHolder = holder
	}

	if _, err := h.deps.Path.Put(ctx, storageKey, openBytesReader(res.Data)); err != nil {
		// Compensating restore — Trash.Move moved the old file to a
		// holder dir; bring it back so the canonical path is whole again.
		if trashHolder != "" {
			restorePath := filepath.Join(h.deps.RepoRoot, projectName, "helm", repo.Name, "charts", reboundOldFilename)
			if rerr := h.deps.Trash.Restore(ctx, trashHolder, restorePath); rerr != nil {
				slog.WarnContext(ctx, "helm_sync: trash.Restore failed after Put error (orphaned trash holder)",
					slog.Int64("repo_id", repo.ID),
					slog.String("trash_holder", trashHolder),
					slog.Any("err", rerr),
				)
			}
		}
		return 0, fmt.Errorf("helm_sync: store %s: %w", filename, err)
	}
	abs := filepath.Join(h.deps.RepoRoot, filepath.FromSlash(storageKey))
	chart, perr := Parse(abs)
	if perr != nil {
		_ = os.Remove(abs)
		if trashHolder != "" {
			restorePath := filepath.Join(h.deps.RepoRoot, projectName, "helm", repo.Name, "charts", reboundOldFilename)
			if rerr := h.deps.Trash.Restore(ctx, trashHolder, restorePath); rerr != nil {
				slog.WarnContext(ctx, "helm_sync: trash.Restore failed after Parse error",
					slog.Int64("repo_id", repo.ID),
					slog.String("trash_holder", trashHolder),
					slog.Any("err", rerr),
				)
			}
		}
		return 0, fmt.Errorf("helm_sync: parse %s: %w", filename, perr)
	}
	size := int64(len(res.Data))
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
			Digest:          chartDigest,
			Filename:        filename,
		}); err != nil {
			return err
		}
		if err := metadata.IndexHelmDelete(ctx, tx, repo.ID, chart.Name, chart.Version, chart.AppVersion); err != nil {
			return err
		}
		if err := metadata.IndexHelm(ctx, tx, repo.ID, chart.Name, chart.Version, chart.AppVersion, chart.Description); err != nil {
			return err
		}
		if repo.AutoScan && h.deps.Scans != nil {
			if _, err := h.deps.Scans.Enqueue(ctx, tx, repo.ID, "helm", filename); err != nil {
				return err
			}
		}
		return h.deps.Repos.SetMetadataState(ctx, tx, repo.ID, metadata.MetadataStateDirty)
	}); err != nil {
		_ = os.Remove(abs)
		// Compensating restore on commit failure — the audit must NOT
		// fire and the on-disk state must roll back to the prior good
		// chart so the next sync attempt sees the consistent old state.
		if trashHolder != "" {
			restorePath := filepath.Join(h.deps.RepoRoot, projectName, "helm", repo.Name, "charts", reboundOldFilename)
			if rerr := h.deps.Trash.Restore(ctx, trashHolder, restorePath); rerr != nil {
				slog.WarnContext(ctx, "helm_sync: trash.Restore failed after commit error (orphaned trash holder)",
					slog.Int64("repo_id", repo.ID),
					slog.String("trash_holder", trashHolder),
					slog.Any("err", rerr),
				)
			}
		}
		return 0, fmt.Errorf("helm_sync: commit %s: %w", filename, err)
	}

	// 6. Post-commit side-effects — best-effort.
	//
	// 6a. If rebound + filename DIFFERS, the old file is still on disk at
	// its prior path (Put landed at a new key). Move it to trash now that
	// the DB is consistent. This is the rare case in OCI Helm because
	// filename is derived from name+version (which are pinned); kept here
	// for robustness against future filename derivation changes.
	if rebound && h.deps.Trash != nil && reboundOldFilename != "" && reboundOldFilename != filename {
		actor := auth.ActorLoginFromContext(ctx)
		oldPath := filepath.Join(h.deps.RepoRoot, projectName, "helm", repo.Name, "charts", reboundOldFilename)
		if _, terr := h.deps.Trash.Move(ctx, oldPath, "oci_tag_rebound", reboundOldID, actor); terr != nil {
			if !errors.Is(terr, os.ErrNotExist) {
				slog.WarnContext(ctx, "helm_sync: trash.Move on rebound (post-commit, non-fatal)",
					slog.Int64("repo_id", repo.ID),
					slog.String("name", chartName),
					slog.String("version", chartVersion),
					slog.String("old_filename", reboundOldFilename),
					slog.Any("err", terr),
				)
			}
		}
	}

	// 6b. Emit EvtOciTagRebound with the details_json shape AFTER commit.
	// Audit must never claim a replacement that did not commit; the
	// deferred emission is the load-bearing guarantee of this fix.
	if rebound && h.deps.Audit != nil {
		_ = h.deps.Audit.Record(ctx, audit.Event{
			Kind:       audit.EvtOciTagRebound,
			TargetKind: "repo",
			TargetID:   strconv.FormatInt(repo.ID, 10),
			Details: map[string]any{
				"name":         chartName,
				"version":      chartVersion,
				"old_digest":   reboundOldDigest,
				"new_digest":   chartDigest,
				"upstream_url": ent.Path,
				"repo_id":      repo.ID,
				"replaced_at":  time.Now().UTC().Format(time.RFC3339),
			},
		})
	}
	return size, nil
}

// isNonChartManifestErr matches Helm SDK / ORAS errors from pulling an
// OCI artifact that doesn't carry the 2+ descriptors a Helm chart ref
// requires (config + chart layer). Bitnami's `-metadata` sidecar tags
// and any future single-layer "alongside the chart" artifact hit this
// path. String-matched because neither library exports a typed error
// for the condition (verified against helm.sh/helm/v3 v3.20.x).
func isNonChartManifestErr(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "minimum number of descriptors")
}

// parseOCIRef extracts (chartName, chartVersion) from
// "[oci://]host/path/<chartName>:<chartVersion>". Returns ("","") if the
// ref is malformed. The Helm OCI convention ties the chart name to the
// last path segment and the version to the tag.
func parseOCIRef(ref string) (name, version string) {
	trimmed := strings.TrimPrefix(ref, "oci://")
	colonIdx := strings.LastIndex(trimmed, ":")
	if colonIdx < 0 {
		return "", ""
	}
	version = trimmed[colonIdx+1:]
	pre := trimmed[:colonIdx]
	slashIdx := strings.LastIndex(pre, "/")
	if slashIdx < 0 || slashIdx == len(pre)-1 {
		return "", ""
	}
	name = pre[slashIdx+1:]
	if name == "" || version == "" {
		return "", ""
	}
	return name, version
}

// maxArtifactBytes caps the per-artifact upstream body for mirror sync
// downloads. Test-overridable (var, not const) so cap+1 oversized-upstream
// regression guards can run without serving multi-GiB bodies.
var maxArtifactBytes int64 = 1024 * 1024 * 1024

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
