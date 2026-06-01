package helm

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	helmrepo "helm.sh/helm/v3/pkg/repo"

	"github.com/vladoportos/omnirepo/internal/audit"
	"github.com/vladoportos/omnirepo/internal/auth"
	"github.com/vladoportos/omnirepo/internal/metadata"
	"github.com/vladoportos/omnirepo/internal/protocol/regen"
	"github.com/vladoportos/omnirepo/internal/storage"
)

// RegenDeps bundles the state a RegenFn needs to rebuild <repoRoot>/index.yaml
// from the .tgz files currently on disk. Populated by app.phase3_helm.wireHelm.
type RegenDeps struct {
	DB       *metadata.DB
	Repos    *metadata.ReposRepo
	Projects *metadata.ProjectsRepo
	Audit    audit.Logger
	Locks    storage.Locks
	RepoRoot string
	RepoID   int64
	// BaseURL is the chart-URL prefix written into index.yaml entries'
	// `urls:` field. When empty, the regen uses a relative "charts" prefix
	// which works for Helm clients consuming /<project>/helm/<repo>/index.yaml
	// directly (they resolve relative URLs against the index URL).
	BaseURL string
}

// RegenFor returns a regen.RegenFn that rebuilds <repoRoot>/index.yaml from
// disk under the per-repo mutex (storage.Locks). Uses
// helm.sh/helm/v3/pkg/repo.IndexDirectory so the index always matches what
// actually sits on disk.
//
// Regen state machine:
//  1. SetMetadataState(regenerating)
//  2. IndexDirectory(<repoRoot>/charts, baseURL); on failure → state=dirty +
//     last_regen_error; audit EvtRepoMetadataFailed.
//  3. Serialize index → compute sha256 → write index-<sha>.yaml then
//     atomically swap index.yaml (both through storage.WriteAndRename).
//  4. sweepStale(repoRoot, currentHashName) removes old content-hash files.
//  5. SetMetadataState(clean) + clear last_regen_error; audit
//     EvtRepoMetadataRegen.
func RegenFor(d RegenDeps) regen.RegenFn {
	return func(ctx context.Context) error {
		if ctx == nil {
			ctx = context.Background()
		}
		// Resolve project + repo names for the per-repo mutex key. These
		// are stable across the lifetime of the repo so a single lookup
		// per coalescer-fire is fine.
		rr, err := d.Repos.FindByID(ctx, d.RepoID)
		if err != nil || rr == nil {
			return fmt.Errorf("helm regen: repo %d lookup: %w", d.RepoID, err)
		}
		proj, err := d.Projects.FindByID(ctx, rr.ProjectID)
		if err != nil || proj == nil {
			return fmt.Errorf("helm regen: project %d lookup: %w", rr.ProjectID, err)
		}

		if d.Locks != nil {
			mu := d.Locks.For(storage.RepoKey{
				Project: proj.Name, Type: "helm", Repo: rr.Name,
			})
			mu.Lock()
			defer mu.Unlock()
		}

		// 1. Mark regenerating.
		if err := d.DB.WriteTx(ctx, func(tx *sql.Tx) error {
			return d.Repos.SetMetadataState(ctx, tx, d.RepoID, metadata.MetadataStateRegenerating)
		}); err != nil {
			return fmt.Errorf("helm regen: set regenerating: %w", err)
		}

		start := time.Now()
		repoDir := filepath.Join(d.RepoRoot, proj.Name, "helm", rr.Name)
		chartsDir := filepath.Join(repoDir, "charts")
		if err := os.MkdirAll(chartsDir, 0o750); err != nil {
			return d.recordFailure(ctx, fmt.Errorf("helm regen: mkdir charts: %w", err))
		}

		baseURL := d.BaseURL
		if baseURL == "" {
			baseURL = "charts"
		}

		// 2. Rebuild index from disk via the helm SDK.
		idx, err := helmrepo.IndexDirectory(chartsDir, baseURL)
		if err != nil {
			return d.recordFailure(ctx, fmt.Errorf("helm regen: IndexDirectory: %w", err))
		}
		idx.SortEntries()

		// 3. Serialize via helm SDK's WriteFile into a scratch path, then
		// read back bytes so we can sha256 + content-hash name + atomic-swap.
		tmpScratch, err := os.CreateTemp(repoDir, ".tmp-helm-index-*.yaml")
		if err != nil {
			return d.recordFailure(ctx, fmt.Errorf("helm regen: scratch: %w", err))
		}
		scratchPath := tmpScratch.Name()
		// Surface scratch-file close errors (FD-leak signal under
		// failure storms). Still proceed on error — the file is removed
		// via defer regardless.
		if closeErr := tmpScratch.Close(); closeErr != nil {
			slog.WarnContext(ctx, "helm.regen.scratch_close_failed", "path", scratchPath, "err", closeErr)
		}
		defer func() { _ = os.Remove(scratchPath) }()

		if err := idx.WriteFile(scratchPath, 0o644); err != nil {
			return d.recordFailure(ctx, fmt.Errorf("helm regen: WriteFile: %w", err))
		}
		indexBytes, err := os.ReadFile(scratchPath)
		if err != nil {
			return d.recordFailure(ctx, fmt.Errorf("helm regen: read scratch: %w", err))
		}

		sum := sha256.Sum256(indexBytes)
		hashName := fmt.Sprintf("index-%x.yaml", sum)

		// 4. Atomic writes through storage.WriteAndRename.
		tmpDir := filepath.Join(d.RepoRoot, ".tmp-helm-regen")
		if _, err := storage.WriteAndRename(ctx, tmpDir, filepath.Join(repoDir, hashName), bytes.NewReader(indexBytes)); err != nil {
			return d.recordFailure(ctx, fmt.Errorf("helm regen: write hash-named: %w", err))
		}
		if _, err := storage.WriteAndRename(ctx, tmpDir, filepath.Join(repoDir, "index.yaml"), bytes.NewReader(indexBytes)); err != nil {
			return d.recordFailure(ctx, fmt.Errorf("helm regen: write index.yaml: %w", err))
		}

		// 5. Sweep stale content-hash files (anything matching index-*.yaml
		// except the current hashName).
		sweepStale(repoDir, hashName)

		// 6. Mark clean + clear last_regen_error.
		if err := d.DB.WriteTx(ctx, func(tx *sql.Tx) error {
			if err := d.Repos.SetLastRegenError(ctx, tx, d.RepoID, ""); err != nil {
				return err
			}
			return d.Repos.SetMetadataState(ctx, tx, d.RepoID, metadata.MetadataStateClean)
		}); err != nil {
			return fmt.Errorf("helm regen: set clean: %w", err)
		}

		if d.Audit != nil {
			_ = d.Audit.Record(ctx, audit.Event{
				Kind:       audit.EvtRepoMetadataRegen,
				TargetKind: "repo",
				TargetID:   fmt.Sprintf("%d", d.RepoID),
				Outcome:    "ok",
				OccurredAt: time.Now().UTC(),
				Details: map[string]any{
					"protocol":              "helm",
					"duration_ms":           time.Since(start).Milliseconds(),
					"files_rewritten_count": len(idx.Entries),
				},
			})
		}
		// Drop actor hint into ctx for compile-time coupling check.
		_ = auth.Actor{}
		return nil
	}
}

// recordFailure rolls the repo back to dirty + last_regen_error and emits an
// audit event. Returned as the wrapper of the inbound err so the coalescer's
// contract (swallow err, caller keeps going) stays intact but the failure is
// visible in logs, audit, and the repos row.
func (d RegenDeps) recordFailure(ctx context.Context, cause error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	msg := cause.Error()
	_ = d.DB.WriteTx(ctx, func(tx *sql.Tx) error {
		if err := d.Repos.SetLastRegenError(ctx, tx, d.RepoID, msg); err != nil {
			return err
		}
		return d.Repos.SetMetadataState(ctx, tx, d.RepoID, metadata.MetadataStateDirty)
	})
	if d.Audit != nil {
		_ = d.Audit.Record(ctx, audit.Event{
			Kind:       audit.EvtRepoMetadataFailed,
			TargetKind: "repo",
			TargetID:   fmt.Sprintf("%d", d.RepoID),
			Outcome:    "failed",
			OccurredAt: time.Now().UTC(),
			Details: map[string]any{
				"protocol": "helm",
				"error":    msg,
			},
		})
	}
	return cause
}

// sweepStale removes content-hash-named index files in repoDir except the
// keep filename and the plain "index.yaml". Errors are logged-and-ignored:
// a stale file cannot corrupt correctness because readers always serve
// "index.yaml" (which was atomically replaced).
func sweepStale(repoDir, keepHashName string) {
	matches, err := filepath.Glob(filepath.Join(repoDir, "index-*.yaml"))
	if err != nil {
		return
	}
	for _, m := range matches {
		base := filepath.Base(m)
		if base == keepHashName || base == "index.yaml" {
			continue
		}
		_ = os.Remove(m)
	}
}
