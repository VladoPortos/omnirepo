// Package raw — block_on_severity gate for /<project>/raw/<repo>/<path>
// GET (Phase 02-09, D-26, SCAN-07).
//
// Wired into Handler.SeverityGate at app.Run time. Mirrors the OCI gate
// (internal/protocol/oci/severity_gate.go) but adapts to RAW's
// SeverityGateFn signature, which returns (blocked, severity, scanID)
// rather than an *ErrBlockedByScan typed error.
package raw

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/dxc-internal/omnirepo/internal/audit"
	"github.com/dxc-internal/omnirepo/internal/metadata"
	"github.com/dxc-internal/omnirepo/internal/scan"
)

// severityRank orders severities so we can compare against a threshold.
var severityRank = map[string]int{
	"none":     0,
	"low":      1,
	"medium":   2,
	"high":     3,
	"critical": 4,
}

type summaryCounts struct {
	Critical int `json:"critical"`
	High     int `json:"high"`
	Medium   int `json:"medium"`
	Low      int `json:"low"`
	Unknown  int `json:"unknown"`
}

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
// returns (true, severity, scanID) when block_on_severity threshold is met.
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
	return func(ctx context.Context, repoID int64, artifactKind, artifactID string) (blocked bool, severity string, scanID int64) {
		repo, err := repos.FindByID(ctx, repoID)
		if err != nil || repo == nil {
			return false, "", 0
		}
		threshold := strings.ToLower(repo.BlockOnSeverity)
		if threshold == "" || threshold == "none" {
			return false, "", 0
		}

		if entry, hit := cache.Get(repoID, artifactKind, artifactID); hit {
			if !entry.Blocked {
				return false, "", 0
			}
			emitGateAudit(ctx, auditLogger, repoID, artifactKind, artifactID, entry, "cache")
			return true, entry.Severity, entry.ScanID
		}

		latest, err := scans.LatestForArtifact(ctx, repoID, artifactKind, artifactID)
		if err != nil {
			return false, "", 0
		}
		if latest == nil {
			cache.Set(repoID, artifactKind, artifactID, scan.CacheEntry{Blocked: false})
			return false, "", 0
		}
		var sum summaryCounts
		if err := json.Unmarshal([]byte(latest.SeveritySummaryJSON), &sum); err != nil {
			cache.Set(repoID, artifactKind, artifactID, scan.CacheEntry{Blocked: false})
			return false, "", 0
		}
		sev, count := sum.maxSeverityAtOrAbove(threshold)
		if sev == "" {
			cache.Set(repoID, artifactKind, artifactID, scan.CacheEntry{Blocked: false})
			return false, "", 0
		}
		entry := scan.CacheEntry{
			Blocked: true, Severity: sev, CVECount: count, ScanID: latest.ScanID,
		}
		cache.Set(repoID, artifactKind, artifactID, entry)
		emitGateAudit(ctx, auditLogger, repoID, artifactKind, artifactID, entry, "db")
		return true, sev, latest.ScanID
	}
}

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
