// Package scan — scan job handler (Phase 02-09, D-23..D-26, SCAN-03..08).
//
// This file owns the scan_pool job that materializes a Docker manifest
// (or RAW file) into a temp tree, invokes the Trivy Runner, and persists
// the result + SBOM + audit trail in a single writer transaction.
//
// Invariants:
//
//  1. Tmp dir cleanup runs via defer regardless of success / failure /
//     panic (T-02-09-06).
//  2. On Runner success: ScansRepo.MarkDone, VulnerabilitiesRepo.InsertBatch
//     (capped at vulnBatchCap), per-CVE FTS5 IndexVulnerability, and the
//     scan.finished audit event ALL run in one writer tx. Either everything
//     commits or nothing does.
//  3. SBOM generation failure does NOT fail the scan — the scan row is
//     still marked done with sbom_path="" and a slog.Warn is emitted.
//  4. Severity cache invalidation runs AFTER the writer tx commits so a
//     gate query immediately after sees the fresh decision.
//  5. last_error strings are sanitized via sanitizeErr to strip absolute
//     paths under the data root (T-02-09-07).
package scan

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/dxc-internal/omnirepo/internal/audit"
	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/storage"
)

// vulnBatchCap is the per-scan cap on inserted vulnerabilities rows
// (T-02-09-03). Over-cap → MarkPermanentlyFailed with "too many findings".
const vulnBatchCap = 10000

// ManifestStore abstracts the manifest body lookup needed for OCI scans.
// Implemented by *metadata.DockerManifestsRepo.
type ManifestStore interface {
	GetByDigest(ctx context.Context, repoID int64, digest string) (*metadata.DockerManifest, error)
}

// RawFileStore abstracts the raw file lookup needed for RAW scans.
// Implemented by *metadata.RawFilesRepo.
type RawFileStore interface {
	Get(ctx context.Context, repoID int64, path string) (metadata.RawFile, bool, error)
}

// ReposLookup abstracts the repo lookup used to resolve project name +
// repo name for the on-disk RAW file path. Implemented by
// *metadata.ReposRepo.
type ReposLookup interface {
	FindByID(ctx context.Context, id int64) (*metadata.Repo, error)
}

// ProjectsLookup abstracts the project name lookup used for RAW file
// paths. Implemented by *metadata.ProjectsRepo.
type ProjectsLookup interface {
	FindByID(ctx context.Context, id int64) (*metadata.Project, error)
}

// HandlerDeps bundles the dependencies the scan handler needs.
type HandlerDeps struct {
	DB        *metadata.DB
	Runner    Runner
	Scans     *metadata.ScansRepo
	Vulns     *metadata.VulnerabilitiesRepo
	Manifests ManifestStore
	RawFiles  RawFileStore
	Repos     ReposLookup
	Projects  ProjectsLookup
	CAS       storage.CAS
	Audit     audit.Logger
	Cache     *SeverityCache

	// DataRoot is the OmniRepo data root (e.g. /var/lib/omnirepo). The
	// handler writes scan tmp under DataRoot/tmp/scans/<id> and SBOMs
	// under DataRoot/sboms/<id>.json.
	DataRoot string
}

// Handler is the scan job handler. Register Handler.Handle as the scanPool
// handler for both "docker" and "raw" kinds in app.Run.
type Handler struct {
	deps HandlerDeps
	// pathSanitizer matches absolute paths under deps.DataRoot for stripping
	// from last_error strings (T-02-09-07).
	pathSanitizer *regexp.Regexp
}

// NewHandler constructs a Handler. Returns an error if any required
// dependency is missing.
func NewHandler(deps HandlerDeps) (*Handler, error) {
	if deps.DB == nil || deps.Runner == nil || deps.Scans == nil || deps.Vulns == nil {
		return nil, errors.New("scan.NewHandler: missing required dependency (DB/Runner/Scans/Vulns)")
	}
	if deps.DataRoot == "" {
		return nil, errors.New("scan.NewHandler: DataRoot is required")
	}
	if deps.Cache == nil {
		deps.Cache = NewSeverityCache(0)
	}
	// Sanitizer: replace absolute paths under DataRoot with "<data>" and
	// any /home/<user>/* path with "<home>" so stack traces / Trivy stderr
	// don't leak local layout into last_error.
	root := regexp.QuoteMeta(strings.TrimRight(deps.DataRoot, "/"))
	pat := regexp.MustCompile(root + `[^\s"']*|/home/[^/\s"']+(/[^\s"']*)?|/etc/[^\s"']*`)
	return &Handler{deps: deps, pathSanitizer: pat}, nil
}

// Handle executes one scan row. Returning a non-nil error causes the
// pool to MarkFailed + retry (or MarkPermanentlyFailed at MaxAttempts).
// Returning nil indicates the handler has already MarkDone'd (or
// MarkPermanentlyFailed'd) the row inside its own writer tx; the pool
// generic MarkDone path is a fallback only.
func (h *Handler) Handle(ctx context.Context, scan *metadata.Scan) error {
	startTime := time.Now()
	h.emitScanAudit(ctx, audit.EvtScanStarted, scan.ID, "ok", map[string]any{
		"repo_id":       scan.RepoID,
		"artifact_kind": scan.ArtifactKind,
		"artifact_id":   scan.ArtifactID,
	})

	tmp := filepath.Join(h.deps.DataRoot, "tmp", "scans", strconv.FormatInt(scan.ID, 10))
	defer func() { _ = os.RemoveAll(tmp) }()

	var (
		result Result
		err    error
	)
	switch scan.ArtifactKind {
	case "docker":
		if err = h.materializeDocker(ctx, tmp, scan.RepoID, scan.ArtifactID); err != nil {
			return h.failScan(ctx, scan, fmt.Errorf("materialize docker: %w", err))
		}
		result, err = h.deps.Runner.Image(ctx, tmp)
	case "raw":
		var fsRoot string
		fsRoot, err = h.materializeRaw(ctx, tmp, scan.RepoID, scan.ArtifactID)
		if err != nil {
			return h.failScan(ctx, scan, fmt.Errorf("materialize raw: %w", err))
		}
		result, err = h.deps.Runner.Filesystem(ctx, fsRoot)
	case "rpm", "deb", "pypi", "helm":
		// F-4: package-style artifacts. Materialize the archive (and best-
		// effort extract it) into tmp, then point Trivy's filesystem
		// scanner at the result. Trivy surfaces vuln + secret + misconfig
		// for what it can decode — Helm charts in particular light up
		// the misconfig scanner on templates/*.yaml.
		var fsRoot string
		fsRoot, err = h.materializePackage(ctx, tmp, scan.ArtifactKind, scan.RepoID, scan.ArtifactID)
		if err != nil {
			return h.failScan(ctx, scan,
				fmt.Errorf("materialize %s: %w", scan.ArtifactKind, err))
		}
		result, err = h.deps.Runner.Filesystem(ctx, fsRoot)
	default:
		return h.permFailScan(ctx, scan, fmt.Errorf("unknown artifact kind %q", scan.ArtifactKind))
	}
	if err != nil {
		return h.failScan(ctx, scan, fmt.Errorf("trivy: %w", err))
	}

	// Cap check BEFORE the tx so a runaway scan doesn't burn writer time.
	if len(result.Vulnerabilities) > vulnBatchCap {
		return h.permFailScan(ctx, scan,
			fmt.Errorf("too many findings: %d (cap=%d)", len(result.Vulnerabilities), vulnBatchCap))
	}

	// SBOM (docker scans only). Failure is warn-only — does not fail the scan.
	sbomPath := ""
	if scan.ArtifactKind == "docker" {
		sbomPath = filepath.Join(h.deps.DataRoot, "sboms", fmt.Sprintf("%d.json", scan.ID))
		if err := os.MkdirAll(filepath.Dir(sbomPath), 0o750); err != nil {
			slog.WarnContext(ctx, "scan.sbom.mkdir_failed", "scan_id", scan.ID, "err", err)
			sbomPath = ""
		} else if err := h.deps.Runner.SBOM(ctx, tmp, FormatCycloneDX, sbomPath); err != nil {
			slog.WarnContext(ctx, "scan.sbom.failed",
				"scan_id", scan.ID, "err", h.sanitize(err.Error()))
			// Best-effort cleanup of the partially-written file.
			_ = os.Remove(sbomPath)
			sbomPath = ""
		}
	}

	summaryJSON, err := json.Marshal(result.Summary)
	if err != nil {
		return h.failScan(ctx, scan, fmt.Errorf("encode summary: %w", err))
	}

	// One writer tx: scans.MarkDone + vulnerabilities.InsertBatch + per-CVE FTS.
	err = h.deps.DB.WriteTx(ctx, func(tx *sql.Tx) error {
		if derr := h.deps.Scans.MarkDone(ctx, tx, scan.ID,
			string(summaryJSON), sbomPath, result.TrivyDBVersion); derr != nil {
			return derr
		}
		vulns := toMetadataVulns(result.Vulnerabilities)
		if derr := h.deps.Vulns.InsertBatch(ctx, tx, scan.ID, vulns, vulnBatchCap); derr != nil {
			return derr
		}
		// Dedupe CVE ids — IndexVulnerability appends unconditionally,
		// the cves_fts table is dedup'd at the cve_id grain.
		seen := make(map[string]struct{}, len(result.Vulnerabilities))
		for _, v := range result.Vulnerabilities {
			if v.CVEID == "" {
				continue
			}
			if _, dup := seen[v.CVEID]; dup {
				continue
			}
			seen[v.CVEID] = struct{}{}
			if derr := metadata.IndexVulnerability(ctx, tx, v.CVEID, v.Package, v.Title); derr != nil {
				return derr
			}
		}
		return nil
	})
	if err != nil {
		return h.failScan(ctx, scan, fmt.Errorf("commit: %w", err))
	}

	// AFTER tx commit: invalidate cache, emit finished audit (best-effort).
	h.deps.Cache.Invalidate(scan.RepoID, scan.ArtifactKind, scan.ArtifactID)
	h.emitScanAudit(ctx, audit.EvtScanFinished, scan.ID, "ok", map[string]any{
		"repo_id":          scan.RepoID,
		"artifact_kind":    scan.ArtifactKind,
		"artifact_id":      scan.ArtifactID,
		"summary":          result.Summary,
		"vuln_count":       len(result.Vulnerabilities),
		"sbom_path":        sbomPath,
		"trivy_db_version": result.TrivyDBVersion,
		"duration_ms":      time.Since(startTime).Milliseconds(),
	})
	return nil
}

// materializeDocker loads the manifest body from docker_manifests, parses
// referenced blob digests, and writes them all to dstDir as an OCI layout.
func (h *Handler) materializeDocker(ctx context.Context, dstDir string, repoID int64, digest string) error {
	if h.deps.Manifests == nil {
		return errors.New("manifests store not wired")
	}
	if h.deps.CAS == nil {
		return errors.New("cas not wired")
	}
	m, err := h.deps.Manifests.GetByDigest(ctx, repoID, digest)
	if err != nil {
		return fmt.Errorf("manifest lookup: %w", err)
	}
	if m == nil {
		return fmt.Errorf("manifest %s not found in repo %d", digest, repoID)
	}
	refs, _, err := extractManifestRefs(m.Body)
	if err != nil {
		// Tolerate parse failure: scan the manifest only.
		slog.WarnContext(ctx, "scan.materialize.refs_unparseable",
			"repo_id", repoID, "digest", digest, "err", err)
		refs = nil
	}
	lookup := func(ctx context.Context, d string) (io.ReadCloser, error) {
		return h.deps.CAS.Get(ctx, d)
	}
	return MaterializeOCILayout(ctx, dstDir, m.Body, m.MediaType, refs, lookup)
}

// materializeRaw copies the raw file at (repoID, artifactPath) into dstDir
// and returns the directory the Filesystem scanner should target. The
// scanner targets the parent dir so Trivy fs sees one file.
func (h *Handler) materializeRaw(ctx context.Context, dstDir string, repoID int64, artifactPath string) (string, error) {
	if h.deps.RawFiles == nil || h.deps.Repos == nil || h.deps.Projects == nil {
		return "", errors.New("raw_files / repos / projects store not wired")
	}
	rf, found, err := h.deps.RawFiles.Get(ctx, repoID, artifactPath)
	if err != nil {
		return "", fmt.Errorf("raw file lookup: %w", err)
	}
	if !found {
		return "", fmt.Errorf("raw file %q not found in repo %d", artifactPath, repoID)
	}
	repo, err := h.deps.Repos.FindByID(ctx, repoID)
	if err != nil || repo == nil {
		return "", fmt.Errorf("repo %d lookup: %w", repoID, err)
	}
	proj, err := h.deps.Projects.FindByID(ctx, repo.ProjectID)
	if err != nil || proj == nil {
		return "", fmt.Errorf("project %d lookup: %w", repo.ProjectID, err)
	}

	srcAbs := filepath.Join(h.deps.DataRoot, "repos",
		proj.Name, repo.Type, repo.Name, filepath.FromSlash(rf.Path))

	if err := os.MkdirAll(dstDir, 0o750); err != nil {
		return "", fmt.Errorf("mkdir tmp: %w", err)
	}
	base := filepath.Base(rf.Path)
	if base == "" || base == "." || base == "/" {
		base = "file"
	}
	dstFile := filepath.Join(dstDir, base)
	if err := copyFile(srcAbs, dstFile); err != nil {
		return "", fmt.Errorf("copy raw file: %w", err)
	}
	return dstDir, nil
}

// failScan records the error on the scans row and returns the error so the
// pool's generic MarkFailed path schedules a retry. Sanitizes paths from
// errStr.
func (h *Handler) failScan(ctx context.Context, scan *metadata.Scan, herr error) error {
	sanitized := h.sanitize(herr.Error())
	h.emitScanAudit(ctx, audit.EvtScanFailed, scan.ID, "error", map[string]any{
		"repo_id":       scan.RepoID,
		"artifact_kind": scan.ArtifactKind,
		"artifact_id":   scan.ArtifactID,
		"err":           sanitized,
	})
	// The pool's markFailed path will record the error in the scans row.
	return errors.New(sanitized)
}

// MarkNotImplemented is the termination path for artifact kinds we accept
// but don't yet know how to scan (rpm, deb, pypi, helm as of F-4). Marks
// the scans row permanently failed with `reason`, emits a scan.failed
// audit record, and returns nil so the pool stops retrying.
func (h *Handler) MarkNotImplemented(ctx context.Context, scanID int64, reason string) error {
	if derr := h.deps.DB.WriteTx(ctx, func(tx *sql.Tx) error {
		return h.deps.Scans.MarkPermanentlyFailed(ctx, tx, scanID, reason)
	}); derr != nil {
		slog.ErrorContext(ctx, "scan.not_implemented.markfailed_err",
			"scan_id", scanID, "err", derr)
		return derr
	}
	h.emitScanAudit(ctx, audit.EvtScanFailed, scanID, "permanent", map[string]any{
		"reason": reason,
	})
	return nil
}

// permFailScan marks the scan permanently failed (no retry) for over-cap
// findings or unknown artifact kinds.
func (h *Handler) permFailScan(ctx context.Context, scan *metadata.Scan, herr error) error {
	sanitized := h.sanitize(herr.Error())
	if derr := h.deps.DB.WriteTx(ctx, func(tx *sql.Tx) error {
		return h.deps.Scans.MarkPermanentlyFailed(ctx, tx, scan.ID, sanitized)
	}); derr != nil {
		slog.ErrorContext(ctx, "scan.permfail.markfailed_err",
			"scan_id", scan.ID, "err", derr)
	}
	h.emitScanAudit(ctx, audit.EvtScanFailed, scan.ID, "permanent", map[string]any{
		"repo_id":       scan.RepoID,
		"artifact_kind": scan.ArtifactKind,
		"artifact_id":   scan.ArtifactID,
		"err":           sanitized,
	})
	// Returning nil tells the pool we already terminated the row; nothing
	// further to do.
	return nil
}

// sanitize strips local filesystem paths from s (T-02-09-07).
func (h *Handler) sanitize(s string) string {
	if h.pathSanitizer == nil {
		return s
	}
	return h.pathSanitizer.ReplaceAllString(s, "<path>")
}

// emitScanAudit is best-effort.
func (h *Handler) emitScanAudit(ctx context.Context, kind audit.EventKind, scanID int64, outcome string, details map[string]any) {
	if h.deps.Audit == nil {
		return
	}
	_ = h.deps.Audit.Record(ctx, audit.Event{
		Kind:       kind,
		TargetKind: "scan",
		TargetID:   strconv.FormatInt(scanID, 10),
		Outcome:    outcome,
		Details:    details,
		OccurredAt: time.Now().UTC(),
	})
}

// extractManifestRefs returns the deduped list of "sha256:..." digests
// referenced by a manifest body. For an image manifest: config + layers.
// For an index: child manifests' digests. The isIndex flag tells the
// caller whether refs refer to docker_manifests rows (index) vs
// docker_blobs rows (image manifest).
//
// This duplicates internal/protocol/oci.manifestRefs to avoid an import
// cycle (oci → scan via SeverityGate hook is the existing direction).
func extractManifestRefs(body []byte) (refs []string, isIndex bool, err error) {
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, false, fmt.Errorf("manifest parse: %w", err)
	}
	seen := map[string]struct{}{}
	collect := func(d string) {
		if !strings.HasPrefix(d, "sha256:") {
			return
		}
		if _, ok := seen[d]; ok {
			return
		}
		seen[d] = struct{}{}
		refs = append(refs, d)
	}
	if mfs, ok := raw["manifests"].([]any); ok {
		isIndex = true
		for _, m := range mfs {
			if mm, ok := m.(map[string]any); ok {
				if d, ok := mm["digest"].(string); ok {
					collect(d)
				}
			}
		}
		return refs, true, nil
	}
	if cfg, ok := raw["config"].(map[string]any); ok {
		if d, ok := cfg["digest"].(string); ok {
			collect(d)
		}
	}
	if layers, ok := raw["layers"].([]any); ok {
		for _, l := range layers {
			if ll, ok := l.(map[string]any); ok {
				if d, ok := ll["digest"].(string); ok {
					collect(d)
				}
			}
		}
	}
	return refs, false, nil
}

// toMetadataVulns converts scan.Vuln into metadata.Vuln for batch insert.
func toMetadataVulns(in []Vuln) []metadata.Vuln {
	out := make([]metadata.Vuln, 0, len(in))
	for _, v := range in {
		out = append(out, metadata.Vuln{
			CVEID:          v.CVEID,
			Severity:       v.Severity,
			PackageName:    v.Package,
			PackageVersion: v.InstalledVersion,
			FixedVersion:   v.FixedVersion,
			Title:          v.Title,
			Description:    v.Description,
		})
	}
	return out
}

// copyFile copies src → dst with a sane buffer. Used by materializeRaw.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
