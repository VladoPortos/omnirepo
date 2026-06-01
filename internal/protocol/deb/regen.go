// Package deb — regen factory. Rebuilds the <repoRoot>/<project>/deb/<repo>/
// dists/ tree on disk under the per-repo mutex using the staging-dir atomic
// swap pattern:
//
//  1. SetMetadataState=regenerating
//  2. Group apt_suites rows by suite; for each suite build a sibling
//     dists/<suite>.tmp/ directory containing every
//     (component, architecture) → binary-<arch>/Packages + Packages.gz
//     plus Release / InRelease (clearsigned) / Release.gpg (detached sign).
//  3. Fsync the tmp dir, then os.Rename(current, trash) + os.Rename(tmp,
//     current). On a rename failure, best-effort reverse the trash move so
//     the previous snapshot keeps serving.
//  4. Remove the trash directory fire-and-forget.
//  5. SetMetadataState=clean + audit EvtRepoMetadataRegen +
//     EvtSigningKeyUsed.
//
// Failure path: setMetadataState=dirty + last_regen_error persisted;
// audit EvtRepoMetadataFailed emitted. The coalescer swallows the err.
package deb

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/vladoportos/omnirepo/internal/audit"
	omrcrypto "github.com/vladoportos/omnirepo/internal/crypto"
	"github.com/vladoportos/omnirepo/internal/metadata"
	"github.com/vladoportos/omnirepo/internal/protocol/regen"
	"github.com/vladoportos/omnirepo/internal/storage"
)

// RegenDeps bundles the state a RegenFn needs to rebuild the dists/ tree on
// disk. Populated by app.phase3_deb.wireDEB.
type RegenDeps struct {
	DB          *metadata.DB
	Repos       *metadata.ReposRepo
	Projects    *metadata.ProjectsRepo
	DEBPackages *metadata.DEBPackagesRepo
	AptSuites   *metadata.AptSuitesRepo
	SigningKeys *metadata.SigningKeysRepo
	Audit       audit.Logger
	Locks       storage.Locks
	RepoRoot    string
	RepoID      int64
}

// RegenFor returns a regen.RegenFn that rebuilds the dists/ tree. Caller
// hands the result to the per-repo coalescer registry; debounce is the
// registry's responsibility.
func RegenFor(d RegenDeps) regen.RegenFn {
	return func(ctx context.Context) error {
		if ctx == nil {
			ctx = context.Background()
		}
		rr, err := d.Repos.FindByID(ctx, d.RepoID)
		if err != nil || rr == nil {
			return fmt.Errorf("deb regen: repo %d lookup: %w", d.RepoID, err)
		}
		proj, err := d.Projects.FindByID(ctx, rr.ProjectID)
		if err != nil || proj == nil {
			return fmt.Errorf("deb regen: project %d lookup: %w", rr.ProjectID, err)
		}
		if d.Locks != nil {
			mu := d.Locks.For(storage.RepoKey{Project: proj.Name, Type: "deb", Repo: rr.Name})
			mu.Lock()
			defer mu.Unlock()
		}

		// 1. Mark regenerating.
		if err := d.DB.WriteTx(ctx, func(tx *sql.Tx) error {
			return d.Repos.SetMetadataState(ctx, tx, d.RepoID, metadata.MetadataStateRegenerating)
		}); err != nil {
			return fmt.Errorf("deb regen: set regenerating: %w", err)
		}
		start := time.Now()

		repoDir := filepath.Join(d.RepoRoot, proj.Name, "deb", rr.Name)
		distsDir := filepath.Join(repoDir, "dists")
		if err := os.MkdirAll(distsDir, 0o750); err != nil {
			return d.recordFailure(ctx, fmt.Errorf("deb regen: mkdir dists: %w", err))
		}
		// Fetch signing key ONCE. Dropped via explicit `priv = ""` below
		// so the armored private string doesn't linger.
		priv, err := d.SigningKeys.LookupPrivate(ctx, d.RepoID)
		if err != nil {
			return d.recordFailure(ctx, fmt.Errorf("deb regen: lookup private key: %w", err))
		}
		defer func() { priv = ""; _ = priv }()

		// Group apt_suites rows by suite.
		suites, err := d.AptSuites.ListByRepo(ctx, d.RepoID)
		if err != nil {
			return d.recordFailure(ctx, fmt.Errorf("deb regen: list apt_suites: %w", err))
		}
		bySuite := map[string][]metadata.AptSuite{}
		for _, s := range suites {
			bySuite[s.Suite] = append(bySuite[s.Suite], s)
		}

		filesRewritten := 0
		suiteNames := make([]string, 0, len(bySuite))
		for k := range bySuite {
			suiteNames = append(suiteNames, k)
		}
		sort.Strings(suiteNames)

		for _, suite := range suiteNames {
			entries := bySuite[suite]
			written, err := d.regenSuite(ctx, distsDir, suite, entries, priv, proj.Name, rr.Name)
			if err != nil {
				return d.recordFailure(ctx, err)
			}
			filesRewritten += written
		}

		// 2. Mark clean + clear last_regen_error.
		if err := d.DB.WriteTx(ctx, func(tx *sql.Tx) error {
			if err := d.Repos.SetLastRegenError(ctx, tx, d.RepoID, ""); err != nil {
				return err
			}
			return d.Repos.SetMetadataState(ctx, tx, d.RepoID, metadata.MetadataStateClean)
		}); err != nil {
			return fmt.Errorf("deb regen: set clean: %w", err)
		}

		// 3. Audit.
		if d.Audit != nil {
			fp := ""
			if meta, mErr := d.SigningKeys.Lookup(ctx, d.RepoID); mErr == nil && meta != nil {
				fp = meta.Fingerprint
			}
			_ = d.Audit.Record(ctx, audit.Event{
				Kind:       audit.EvtRepoMetadataRegen,
				TargetKind: "repo",
				TargetID:   fmt.Sprintf("%d", d.RepoID),
				Outcome:    "ok",
				OccurredAt: time.Now().UTC(),
				Details: map[string]any{
					"protocol":              "deb",
					"duration_ms":           time.Since(start).Milliseconds(),
					"files_rewritten_count": filesRewritten,
					"suite_count":           len(bySuite),
				},
			})
			_ = d.Audit.Record(ctx, audit.Event{
				Kind:       audit.EvtSigningKeyUsed,
				TargetKind: "repo",
				TargetID:   fmt.Sprintf("%d", d.RepoID),
				Outcome:    "ok",
				OccurredAt: time.Now().UTC(),
				Details: map[string]any{
					"protocol":         "deb",
					"fingerprint_used": fp,
				},
			})
		}
		return nil
	}
}

// regenSuite builds dists/<suite>.tmp/, fsyncs it, and atomically swaps it
// into place of dists/<suite>/. Returns the number of files written under
// the tmp dir.
func (d RegenDeps) regenSuite(ctx context.Context, distsDir, suite string, entries []metadata.AptSuite, priv, project, repoName string) (int, error) {
	tmpSuiteDir := filepath.Join(distsDir, suite+".tmp")
	// Clear any prior stale staging dir.
	if err := os.RemoveAll(tmpSuiteDir); err != nil {
		return 0, fmt.Errorf("deb regen: clear tmp suite %q: %w", suite, err)
	}
	if err := os.MkdirAll(tmpSuiteDir, 0o750); err != nil {
		return 0, fmt.Errorf("deb regen: mkdir tmp suite %q: %w", suite, err)
	}

	// For the file writes inside the staging dir we use a single tmpDir for
	// the storage.WriteAndRename helper.
	tmpScratch := filepath.Join(distsDir, ".tmp-deb-regen-"+suite)
	defer func() { _ = os.RemoveAll(tmpScratch) }()

	releaseEntries := []ReleaseFileEntry{}
	components := map[string]bool{}
	archs := map[string]bool{}
	filesWritten := 0

	// Deterministic order by component then architecture.
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Component != entries[j].Component {
			return entries[i].Component < entries[j].Component
		}
		return entries[i].Architecture < entries[j].Architecture
	})

	for _, e := range entries {
		components[e.Component] = true
		archs[e.Architecture] = true

		pkgs, err := d.DEBPackages.ListBySuite(ctx, e.ID)
		if err != nil {
			return filesWritten, fmt.Errorf("deb regen: list by suite %d: %w", e.ID, err)
		}
		// Deterministic package order: package name, then version desc
		// (already matches ListBySuite ORDER BY, but re-sort defensively).
		sort.SliceStable(pkgs, func(i, j int) bool {
			if pkgs[i].Package != pkgs[j].Package {
				return pkgs[i].Package < pkgs[j].Package
			}
			return pkgs[i].Version > pkgs[j].Version
		})

		out := make([]PackagesEntry, 0, len(pkgs))
		for _, p := range pkgs {
			// Prefer the stored pool path (real on-disk location).
			// Fallback to the legacy synthesis covers edge cases where a row
			// somehow slipped in without the column populated (migration
			// backfills every row, but belt-and-braces).
			poolPath := p.StoragePoolPath
			if poolPath == "" {
				poolPath = fmt.Sprintf("pool/%s/%s/%s", componentPrefix(p.Package), p.Package, p.Filename)
			}
			ctrl := reconstructControlParagraph(storedPkg{
				Package: p.Package, Version: p.Version, Architecture: p.Architecture,
				Maintainer: p.Maintainer,
				Depends:    p.Depends,
				Section:    p.Section, Priority: p.Priority,
				Description: p.Description,
			})
			out = append(out, PackagesEntry{
				Control:  ctrl,
				Filename: poolPath,
				Size:     p.SizeBytes,
				SHA256:   strings.TrimPrefix(p.Digest, "sha256:"),
				// MD5 is not stored — derive from the on-disk file would cost
				// a read per package; instead we omit MD5 from Packages rows
				// (apt treats it as optional; SHA256 is the mandatory modern
				// hash). The Release file entries still include MD5 of the
				// Packages/Packages.gz bytes because those are computed on
				// the spot from in-memory buffers.
			})
		}
		packagesBody := WritePackages(out)

		var gzBuf bytes.Buffer
		gz := gzipDeterministic(&gzBuf)
		if _, err := gz.Write(packagesBody); err != nil {
			return filesWritten, fmt.Errorf("deb regen: gz write: %w", err)
		}
		if err := gz.Close(); err != nil {
			return filesWritten, fmt.Errorf("deb regen: gz close: %w", err)
		}

		relDir := filepath.Join(e.Component, "binary-"+e.Architecture)
		pkgRelPath := filepath.ToSlash(filepath.Join(relDir, "Packages"))
		pkgGzRelPath := pkgRelPath + ".gz"

		pkgAbsPath := filepath.Join(tmpSuiteDir, relDir, "Packages")
		pkgGzAbsPath := pkgAbsPath + ".gz"
		if err := os.MkdirAll(filepath.Join(tmpSuiteDir, relDir), 0o750); err != nil {
			return filesWritten, fmt.Errorf("deb regen: mkdir %s: %w", relDir, err)
		}
		if _, err := storage.WriteAndRename(ctx, tmpScratch, pkgAbsPath, bytes.NewReader(packagesBody)); err != nil {
			return filesWritten, fmt.Errorf("deb regen: write Packages: %w", err)
		}
		filesWritten++
		if _, err := storage.WriteAndRename(ctx, tmpScratch, pkgGzAbsPath, bytes.NewReader(gzBuf.Bytes())); err != nil {
			return filesWritten, fmt.Errorf("deb regen: write Packages.gz: %w", err)
		}
		filesWritten++

		md5Open, shaOpen, sizeOpen := ComputeMD5SHA256(packagesBody)
		releaseEntries = append(releaseEntries, ReleaseFileEntry{
			Path: pkgRelPath, MD5: md5Open, SHA256: shaOpen, Size: sizeOpen,
		})
		md5Gz, shaGz, sizeGz := ComputeMD5SHA256(gzBuf.Bytes())
		releaseEntries = append(releaseEntries, ReleaseFileEntry{
			Path: pkgGzRelPath, MD5: md5Gz, SHA256: shaGz, Size: sizeGz,
		})
	}

	// Build Release.
	releaseBody := WriteRelease(ReleaseInfo{
		Origin: "OmniRepo", Label: repoName,
		Suite: suite, Codename: suite,
		Description:   "OmniRepo " + project + "/" + repoName,
		Architectures: setToSorted(archs),
		Components:    setToSorted(components),
		Date:          time.Now().UTC(),
		Files:         releaseEntries,
	})
	releasePath := filepath.Join(tmpSuiteDir, "Release")
	if _, err := storage.WriteAndRename(ctx, tmpScratch, releasePath, bytes.NewReader(releaseBody)); err != nil {
		return filesWritten, fmt.Errorf("deb regen: write Release: %w", err)
	}
	filesWritten++

	// Re-read Release from disk before signing.
	releaseOnDisk, err := os.ReadFile(releasePath)
	if err != nil {
		return filesWritten, fmt.Errorf("deb regen: re-read Release: %w", err)
	}

	// Clearsign → InRelease.
	inRelease, err := omrcrypto.ClearSign(priv, releaseOnDisk)
	if err != nil {
		return filesWritten, fmt.Errorf("deb regen: ClearSign: %w", err)
	}
	if _, err := storage.WriteAndRename(ctx, tmpScratch, filepath.Join(tmpSuiteDir, "InRelease"), bytes.NewReader(inRelease)); err != nil {
		return filesWritten, fmt.Errorf("deb regen: write InRelease: %w", err)
	}
	filesWritten++

	// Detach sign → Release.gpg.
	relSig, err := omrcrypto.DetachSign(priv, releaseOnDisk)
	if err != nil {
		return filesWritten, fmt.Errorf("deb regen: DetachSign: %w", err)
	}
	if _, err := storage.WriteAndRename(ctx, tmpScratch, filepath.Join(tmpSuiteDir, "Release.gpg"), bytes.NewReader(relSig)); err != nil {
		return filesWritten, fmt.Errorf("deb regen: write Release.gpg: %w", err)
	}
	filesWritten++

	// Fsync tmp suite dir (best-effort — Linux ext4/xfs/btrfs honor this).
	if f, err := os.Open(tmpSuiteDir); err == nil {
		_ = f.Sync()
		_ = f.Close()
	}

	// Atomic swap: os.Rename(current, trash); os.Rename(tmp, current).
	curSuiteDir := filepath.Join(distsDir, suite)
	trashDir := filepath.Join(distsDir, fmt.Sprintf(".trash-%s-%d", suite, time.Now().UnixNano()))
	haveCurrent := false
	if _, err := os.Stat(curSuiteDir); err == nil {
		haveCurrent = true
		if err := os.Rename(curSuiteDir, trashDir); err != nil {
			return filesWritten, fmt.Errorf("deb regen: rename current→trash: %w", err)
		}
	}
	if err := os.Rename(tmpSuiteDir, curSuiteDir); err != nil {
		// Best-effort restore of the previous current dir.
		if haveCurrent {
			_ = os.Rename(trashDir, curSuiteDir)
		}
		return filesWritten, fmt.Errorf("deb regen: rename tmp→current: %w", err)
	}
	_ = os.RemoveAll(trashDir)

	return filesWritten, nil
}

// componentPrefix returns the first-character grouping segment used in apt
// pool paths: "pool/<prefix>/<pkg>/<file>". "lib*" packages use the first
// four characters ("libN..."), everything else uses the first character.
func componentPrefix(pkg string) string {
	if strings.HasPrefix(pkg, "lib") && len(pkg) >= 4 {
		return pkg[:4]
	}
	if pkg == "" {
		return "x"
	}
	return pkg[:1]
}

// setToSorted returns the sorted keys of a string→bool set.
func setToSorted(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// gzipDeterministic returns a gzip.Writer with cleared ModTime/Name/Comment
// so the same input bytes produce byte-identical gzip output across runs.
func gzipDeterministic(w *bytes.Buffer) *gzip.Writer {
	gz := gzip.NewWriter(w)
	gz.ModTime = time.Time{}
	gz.Name = ""
	gz.Comment = ""
	return gz
}

// recordFailure rolls the repo back to dirty + last_regen_error and emits
// EvtRepoMetadataFailed. Returns the wrapped cause for the coalescer to log.
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
				"protocol": "deb",
				"error":    msg,
			},
		})
	}
	return cause
}
