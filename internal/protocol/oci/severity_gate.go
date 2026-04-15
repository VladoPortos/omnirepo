// Package oci — block_on_severity gate for /v2/<name>/manifests/<ref> GET
// (Phase 02-09, D-26, SCAN-07).
//
// Wired into Handler.SeverityGate at app.Run time. The handler invokes the
// hook just before serving the manifest body; a non-nil ErrBlockedByScan
// causes manifestGet to write a 403 with the documented JSON envelope.
//
// The gate consults the in-memory cache first (D-26 30s TTL) and falls
// back to ScansRepo.LatestForArtifact on miss. Cache misses ALWAYS write
// the resulting decision back so subsequent requests are O(1).
package oci

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/dxc-internal/omnirepo/internal/audit"
	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/scan"
)

// ErrBlockedByScan is the typed sentinel returned by a SeverityGateFn when
// a GET should be denied. The OCI manifestGet handler turns it into a 403
// with body `{"error":"blocked_by_scan","severity":"<lvl>","cve_count":N,"scan_id":N}`.
type ErrBlockedByScan struct {
	Severity string
	CVECount int
	ScanID   int64
}

// Error satisfies error.
func (e *ErrBlockedByScan) Error() string {
	return fmt.Sprintf("blocked by scan: severity=%s cve_count=%d scan_id=%d",
		e.Severity, e.CVECount, e.ScanID)
}

// severityRank orders severities so we can compare against a threshold.
// "none" sentinel means no block (advisory-only repo).
var severityRank = map[string]int{
	"none":     0,
	"low":      1,
	"medium":   2,
	"high":     3,
	"critical": 4,
}

// summaryCounts mirrors the JSON shape scans.severity_summary_json carries.
type summaryCounts struct {
	Critical int `json:"critical"`
	High     int `json:"high"`
	Medium   int `json:"medium"`
	Low      int `json:"low"`
	Unknown  int `json:"unknown"`
}

// maxSeverityAtOrAbove returns the highest-level bucket that has at least
// one finding, plus the count at that level. When nothing is at-or-above
// threshold, returns ("", 0).
func (s summaryCounts) maxSeverityAtOrAbove(threshold string) (string, int) {
	tRank := severityRank[threshold]
	pairs := []struct {
		name  string
		count int
	}{
		{"critical", s.Critical},
		{"high", s.High},
		{"medium", s.Medium},
		{"low", s.Low},
	}
	for _, p := range pairs {
		if severityRank[p.name] >= tRank && p.count > 0 {
			return p.name, p.count
		}
	}
	return "", 0
}

// NewSeverityGate returns a SeverityGateFn that consults cache → DB and
// returns *ErrBlockedByScan when block_on_severity threshold is met. The
// returned closure is safe for concurrent use.
//
// auditLogger may be nil (tests); when non-nil, scan.gate.blocked is
// emitted on every block decision (cache hit OR DB lookup).
func NewSeverityGate(
	repos *metadata.ReposRepo,
	scans *metadata.ScansRepo,
	cache *scan.SeverityCache,
	auditLogger audit.Logger,
) SeverityGateFn {
	if cache == nil {
		cache = scan.NewSeverityCache(0)
	}
	return func(ctx context.Context, repoID int64, digest string) error {
		// Lookup the repo to read block_on_severity. Cheap; cached by SQLite
		// at the page level.
		repo, err := repos.FindByID(ctx, repoID)
		if err != nil || repo == nil {
			// Repo gone? Don't 500 the gate; just allow (the manifest GET
			// will 404 on its own).
			return nil
		}
		threshold := strings.ToLower(repo.BlockOnSeverity)
		if threshold == "" || threshold == "none" {
			return nil
		}

		// Cache lookup.
		if entry, hit := cache.Get(repoID, "docker", digest); hit {
			if !entry.Blocked {
				return nil
			}
			emitGateAudit(ctx, auditLogger, repoID, "docker", digest, entry, "cache")
			return &ErrBlockedByScan{
				Severity: entry.Severity, CVECount: entry.CVECount, ScanID: entry.ScanID,
			}
		}

		// DB miss path.
		latest, err := scans.LatestForArtifact(ctx, repoID, "docker", digest)
		if err != nil {
			// Treat lookup failure as fail-open with a warn: blocking on
			// transient SQLite errors would create a flaky pull. The cache
			// will be retried on the next GET.
			return nil
		}
		if latest == nil {
			// No completed scan yet → allow (auto-scan will produce one).
			cache.Set(repoID, "docker", digest, scan.CacheEntry{Blocked: false})
			return nil
		}
		var sum summaryCounts
		if err := json.Unmarshal([]byte(latest.SeveritySummaryJSON), &sum); err != nil {
			// Empty or malformed summary → allow.
			cache.Set(repoID, "docker", digest, scan.CacheEntry{Blocked: false})
			return nil
		}
		sev, count := sum.maxSeverityAtOrAbove(threshold)
		if sev == "" {
			cache.Set(repoID, "docker", digest, scan.CacheEntry{Blocked: false})
			return nil
		}
		entry := scan.CacheEntry{
			Blocked: true, Severity: sev, CVECount: count, ScanID: latest.ScanID,
		}
		cache.Set(repoID, "docker", digest, entry)
		emitGateAudit(ctx, auditLogger, repoID, "docker", digest, entry, "db")
		return &ErrBlockedByScan{
			Severity: sev, CVECount: count, ScanID: latest.ScanID,
		}
	}
}

// emitGateAudit records scan.gate.blocked best-effort. Source is "cache"
// or "db" — useful when investigating spurious blocks.
func emitGateAudit(
	ctx context.Context,
	logger audit.Logger,
	repoID int64,
	kind, artifactID string,
	entry scan.CacheEntry,
	source string,
) {
	if logger == nil {
		return
	}
	_ = logger.Record(ctx, audit.Event{
		Kind:       audit.EvtScanGateBlocked,
		TargetKind: kind,
		TargetID:   artifactID,
		Outcome:    "blocked",
		Details: map[string]any{
			"repo_id":   repoID,
			"severity":  entry.Severity,
			"cve_count": entry.CVECount,
			"scan_id":   entry.ScanID,
			"source":    source,
		},
	})
}

// WriteBlockedResponse writes the documented 403 envelope to w. Exported
// so the manifestGet handler in this package can call it without
// re-implementing the JSON shape.
func WriteBlockedResponse(w http.ResponseWriter, e *ErrBlockedByScan) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusForbidden)
	_, _ = fmt.Fprintf(w,
		`{"error":"blocked_by_scan","severity":%q,"cve_count":%d,"scan_id":%d}`,
		e.Severity, e.CVECount, e.ScanID,
	)
}

// IsBlockedByScan reports whether err is an *ErrBlockedByScan.
func IsBlockedByScan(err error) (*ErrBlockedByScan, bool) {
	var b *ErrBlockedByScan
	if errors.As(err, &b) {
		return b, true
	}
	return nil, false
}
