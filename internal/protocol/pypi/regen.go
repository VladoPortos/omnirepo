package pypi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/vladoportos/omnirepo/internal/audit"
	"github.com/vladoportos/omnirepo/internal/metadata"
	"github.com/vladoportos/omnirepo/internal/protocol/regen"
	"github.com/vladoportos/omnirepo/internal/storage"
)

// RegenDeps bundles state needed to rebuild the /simple/ index tree from
// pypi_files for a single repo. Constructed by app.phase3_pypi.wirePyPI;
// the factory closes over RepoID at construction time.
type RegenDeps struct {
	DB        *metadata.DB
	Repos     *metadata.ReposRepo
	Projects  *metadata.ProjectsRepo
	PyPIFiles *metadata.PyPIFilesRepo
	Audit     audit.Logger
	Locks     storage.Locks
	RepoRoot  string
	RepoID    int64
	// PackagesURLPrefix is the relative or absolute prefix prepended to
	// each per-file Simple href. Defaults to "../../packages/" so a Simple
	// page at /<proj>/pypi/<repo>/simple/<name>/ resolves the file at
	// /<proj>/pypi/<repo>/packages/<filename>.
	PackagesURLPrefix string
}

// RegenFor returns a regen.RegenFn that rebuilds /simple/index.html,
// /simple/index.json, and per-project /simple/<name>/index.{html,json}
// from pypi_files. All writes go through storage.WriteAndRename
// so in-flight readers see either the pre- or post-regen state.
//
// State machine (under per-repo mutex):
//  1. SetMetadataState(regenerating).
//  2. List distinct projects → render top-level simple/index.html and
//     simple/index.json.
//  3. For each project: list files → render simple/<norm>/index.html and
//     simple/<norm>/index.json.
//  4. SweepStale removes prior content-hash files.
//  5. SetMetadataState(clean) + clear last_regen_error; audit
//     EvtRepoMetadataRegen.
//
// On any error: state=dirty + last_regen_error + EvtRepoMetadataFailed.
// The coalescer swallows the returned error per its contract; the
// audit row + repos field surface the failure to operators.
func RegenFor(d RegenDeps) regen.RegenFn {
	return func(ctx context.Context) error {
		if ctx == nil {
			ctx = context.Background()
		}
		rr, err := d.Repos.FindByID(ctx, d.RepoID)
		if err != nil || rr == nil {
			return fmt.Errorf("pypi regen: repo %d lookup: %w", d.RepoID, err)
		}
		proj, err := d.Projects.FindByID(ctx, rr.ProjectID)
		if err != nil || proj == nil {
			return fmt.Errorf("pypi regen: project %d lookup: %w", rr.ProjectID, err)
		}

		if d.Locks != nil {
			mu := d.Locks.For(storage.RepoKey{
				Project: proj.Name, Type: "pypi", Repo: rr.Name,
			})
			mu.Lock()
			defer mu.Unlock()
		}

		// 1. Mark regenerating.
		if err := d.DB.WriteTx(ctx, func(tx *sql.Tx) error {
			return d.Repos.SetMetadataState(ctx, tx, d.RepoID, metadata.MetadataStateRegenerating)
		}); err != nil {
			return fmt.Errorf("pypi regen: set regenerating: %w", err)
		}

		start := time.Now()
		simpleDir := filepath.Join(d.RepoRoot, proj.Name, "pypi", rr.Name, "simple")
		if err := os.MkdirAll(simpleDir, 0o750); err != nil {
			return d.recordFailure(ctx, fmt.Errorf("pypi regen: mkdir simple: %w", err))
		}

		projects, err := d.PyPIFiles.ListProjects(ctx, d.RepoID)
		if err != nil {
			return d.recordFailure(ctx, fmt.Errorf("pypi regen: list projects: %w", err))
		}
		if projects == nil {
			projects = []string{}
		}

		filesWritten := 0

		// 2. Top-level /simple/ pages.
		var topHTML bytes.Buffer
		if err := RenderSimpleHTML(&topHTML, projects); err != nil {
			return d.recordFailure(ctx, fmt.Errorf("pypi regen: render top html: %w", err))
		}
		var topJSON bytes.Buffer
		if err := RenderSimpleJSON(&topJSON, projects); err != nil {
			return d.recordFailure(ctx, fmt.Errorf("pypi regen: render top json: %w", err))
		}
		topHashHTML := writeHashAndPointer(ctx, d.RepoRoot, simpleDir, "html", topHTML.Bytes())
		if topHashHTML == "" {
			return d.recordFailure(ctx, fmt.Errorf("pypi regen: write top html"))
		}
		filesWritten += 2
		topHashJSON := writeHashAndPointer(ctx, d.RepoRoot, simpleDir, "json", topJSON.Bytes())
		if topHashJSON == "" {
			return d.recordFailure(ctx, fmt.Errorf("pypi regen: write top json"))
		}
		filesWritten += 2

		// Sweep stale top-level content-hash files.
		sweepStaleHashed(simpleDir, "html", topHashHTML)
		sweepStaleHashed(simpleDir, "json", topHashJSON)

		urlPrefix := d.PackagesURLPrefix
		if urlPrefix == "" {
			urlPrefix = "../../packages/"
		}

		// 3. Per-project pages. Track all kept-project dirs so we can sweep
		// stale per-project content-hash files cleanly.
		keptDirs := make(map[string]struct{}, len(projects))
		for _, projNorm := range projects {
			rows, err := d.PyPIFiles.ListByProject(ctx, d.RepoID, projNorm)
			if err != nil {
				return d.recordFailure(ctx, fmt.Errorf("pypi regen: list %s: %w", projNorm, err))
			}
			files := make([]FileLink, 0, len(rows))
			for _, r := range rows {
				files = append(files, FileLink{
					Filename:       r.Filename,
					URL:            urlPrefix + r.Filename,
					SHA256:         storage.TrimSHA256Prefix(r.Digest),
					RequiresPython: r.RequiresPython,
				})
			}
			projDir := filepath.Join(simpleDir, projNorm)
			if err := os.MkdirAll(projDir, 0o750); err != nil {
				return d.recordFailure(ctx, fmt.Errorf("pypi regen: mkdir %s: %w", projNorm, err))
			}
			keptDirs[projDir] = struct{}{}

			var pHTML bytes.Buffer
			if err := RenderProjectHTML(&pHTML, projNorm, files); err != nil {
				return d.recordFailure(ctx, fmt.Errorf("pypi regen: render %s html: %w", projNorm, err))
			}
			var pJSON bytes.Buffer
			if err := RenderProjectJSON(&pJSON, projNorm, files); err != nil {
				return d.recordFailure(ctx, fmt.Errorf("pypi regen: render %s json: %w", projNorm, err))
			}
			h1 := writeHashAndPointer(ctx, d.RepoRoot, projDir, "html", pHTML.Bytes())
			if h1 == "" {
				return d.recordFailure(ctx, fmt.Errorf("pypi regen: write %s html", projNorm))
			}
			filesWritten += 2
			h2 := writeHashAndPointer(ctx, d.RepoRoot, projDir, "json", pJSON.Bytes())
			if h2 == "" {
				return d.recordFailure(ctx, fmt.Errorf("pypi regen: write %s json", projNorm))
			}
			filesWritten += 2
			sweepStaleHashed(projDir, "html", h1)
			sweepStaleHashed(projDir, "json", h2)
		}

		// 5. Mark clean + clear last_regen_error.
		if err := d.DB.WriteTx(ctx, func(tx *sql.Tx) error {
			if err := d.Repos.SetLastRegenError(ctx, tx, d.RepoID, ""); err != nil {
				return err
			}
			return d.Repos.SetMetadataState(ctx, tx, d.RepoID, metadata.MetadataStateClean)
		}); err != nil {
			return fmt.Errorf("pypi regen: set clean: %w", err)
		}

		if d.Audit != nil {
			_ = d.Audit.Record(ctx, audit.Event{
				Kind:       audit.EvtRepoMetadataRegen,
				TargetKind: "repo",
				TargetID:   fmt.Sprintf("%d", d.RepoID),
				Outcome:    "ok",
				OccurredAt: time.Now().UTC(),
				Details: map[string]any{
					"protocol":              "pypi",
					"duration_ms":           time.Since(start).Milliseconds(),
					"files_rewritten_count": filesWritten,
				},
			})
		}
		return nil
	}
}

// recordFailure marks the repo dirty with last_regen_error, emits an
// EvtRepoMetadataFailed audit row, and returns the wrapped cause so the
// coalescer (which swallows the error) still surfaces failure to ops.
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
				"protocol": "pypi",
				"error":    msg,
			},
		})
	}
	return cause
}

// writeHashAndPointer writes body to dir/index-<sha256>.{ext} via
// WriteAndRename, then atomically updates dir/index.{ext} to the same
// bytes. Returns the hashed filename ("index-<sha>.html") on success or
// "" on error.
func writeHashAndPointer(ctx context.Context, repoRoot, dir, ext string, body []byte) string {
	sum := sha256.Sum256(body)
	hashName := fmt.Sprintf("index-%x.%s", sum, ext)
	tmpDir := filepath.Join(repoRoot, ".tmp-pypi-regen")
	if _, err := storage.WriteAndRename(ctx, tmpDir, filepath.Join(dir, hashName), bytes.NewReader(body)); err != nil {
		return ""
	}
	if _, err := storage.WriteAndRename(ctx, tmpDir, filepath.Join(dir, "index."+ext), bytes.NewReader(body)); err != nil {
		return ""
	}
	return hashName
}

// sweepStaleHashed removes prior index-*.{ext} files in dir except the
// current keepHashName. Errors are swallowed (a stale file cannot corrupt
// correctness because readers always serve the pointer file).
func sweepStaleHashed(dir, ext, keepHashName string) {
	matches, err := filepath.Glob(filepath.Join(dir, "index-*."+ext))
	if err != nil {
		return
	}
	for _, m := range matches {
		base := filepath.Base(m)
		if base == keepHashName {
			continue
		}
		_ = os.Remove(m)
	}
}
