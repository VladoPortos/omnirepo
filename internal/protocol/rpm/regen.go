// Package rpm — regen factory. Rebuilds <repoRoot>/<project>/rpm/<repo>/
// repodata/* on disk under the per-repo mutex:
//
//  1. SetMetadataState=regenerating
//  2. List rpm_packages → build primary/filelists/other.xml.gz
//  3. WriteAndRename hash-named files
//  4. WriteRepomd referencing the hash-named files; WriteAndRename repomd.xml
//  5. Re-read repomd.xml from disk → DetachSign (signing privArmored fetched
//     via SigningKeysRepo.LookupPrivate; in-memory string explicitly dropped
//     after sign) → WriteAndRename repomd.xml.asc
//  6. sweepStale removes prior content-hash files
//  7. SetMetadataState=clean + audit EvtRepoMetadataRegen + EvtSigningKeyUsed
package rpm

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/vladoportos/omnirepo/internal/audit"
	omrcrypto "github.com/vladoportos/omnirepo/internal/crypto"
	"github.com/vladoportos/omnirepo/internal/metadata"
	"github.com/vladoportos/omnirepo/internal/protocol/regen"
	"github.com/vladoportos/omnirepo/internal/storage"
)

// RegenDeps bundles the state a RegenFn needs to rebuild the repodata/
// tree on disk. Populated by app.phase3_rpm.wireRPM.
type RegenDeps struct {
	DB          *metadata.DB
	Repos       *metadata.ReposRepo
	Projects    *metadata.ProjectsRepo
	RPMPackages *metadata.RPMPackagesRepo
	SigningKeys *metadata.SigningKeysRepo
	Audit       audit.Logger
	Locks       storage.Locks
	RepoRoot    string
	RepoID      int64
}

// RegenFor returns a regen.RegenFn that rebuilds repodata/ + repomd.xml +
// repomd.xml.asc atomically. Caller must hand the result to the regen
// coalescer registry; debounce is the registry's responsibility.
func RegenFor(d RegenDeps) regen.RegenFn {
	return func(ctx context.Context) error {
		if ctx == nil {
			ctx = context.Background()
		}
		rr, err := d.Repos.FindByID(ctx, d.RepoID)
		if err != nil || rr == nil {
			return fmt.Errorf("rpm regen: repo %d lookup: %w", d.RepoID, err)
		}
		proj, err := d.Projects.FindByID(ctx, rr.ProjectID)
		if err != nil || proj == nil {
			return fmt.Errorf("rpm regen: project %d lookup: %w", rr.ProjectID, err)
		}
		if d.Locks != nil {
			mu := d.Locks.For(storage.RepoKey{Project: proj.Name, Type: "rpm", Repo: rr.Name})
			mu.Lock()
			defer mu.Unlock()
		}

		// 1. Mark regenerating.
		if err := d.DB.WriteTx(ctx, func(tx *sql.Tx) error {
			return d.Repos.SetMetadataState(ctx, tx, d.RepoID, metadata.MetadataStateRegenerating)
		}); err != nil {
			return fmt.Errorf("rpm regen: set regenerating: %w", err)
		}
		start := time.Now()

		repoDir := filepath.Join(d.RepoRoot, proj.Name, "rpm", rr.Name)
		repodataDir := filepath.Join(repoDir, "repodata")
		if err := os.MkdirAll(repodataDir, 0o750); err != nil {
			return d.recordFailure(ctx, fmt.Errorf("rpm regen: mkdir repodata: %w", err))
		}

		// 2. Load packages and build the three data blocks.
		dbPkgs, err := d.RPMPackages.ListByRepo(ctx, d.RepoID)
		if err != nil {
			return d.recordFailure(ctx, fmt.Errorf("rpm regen: list packages: %w", err))
		}
		parsed := make([]*Parsed, 0, len(dbPkgs))
		for i := range dbPkgs {
			p := &dbPkgs[i]
			parsed = append(parsed, &Parsed{
				Name: p.Name, Version: p.Version, Release: p.Release, Arch: p.Arch,
				Epoch: p.Epoch, Summary: p.Summary, Description: p.Description,
				License: p.License, URL: p.URL, SourceRPM: p.SourceRPM,
				Size:      uint64(p.SizeBytes),
				BuildTime: p.UploadedAt, // best-effort: build time not stored in v1
				Digest:    stripSha256Prefix(p.Digest),
				// Restore the persisted file index so filelists.xml carries
				// real <file> entries (dnf file-based dependency resolution).
				Files: UnmarshalFiles(p.FilesJSON),
			})
		}

		primaryGz, primarySum, primaryOpenSum, primaryOpenSize, primaryGzSize, err := WritePrimary(parsed)
		if err != nil {
			return d.recordFailure(ctx, fmt.Errorf("rpm regen: WritePrimary: %w", err))
		}
		filelistsGz, filelistsSum, filelistsOpenSum, filelistsOpenSize, filelistsGzSize, err := WriteFilelists(parsed)
		if err != nil {
			return d.recordFailure(ctx, fmt.Errorf("rpm regen: WriteFilelists: %w", err))
		}
		otherGz, otherSum, otherOpenSum, otherOpenSize, otherGzSize, err := WriteOther(parsed)
		if err != nil {
			return d.recordFailure(ctx, fmt.Errorf("rpm regen: WriteOther: %w", err))
		}

		// 3. Hash-named files.
		primaryName := fmt.Sprintf("primary-%s.xml.gz", primarySum)
		filelistsName := fmt.Sprintf("filelists-%s.xml.gz", filelistsSum)
		otherName := fmt.Sprintf("other-%s.xml.gz", otherSum)

		tmpDir := filepath.Join(d.RepoRoot, ".tmp-rpm-regen")
		if _, err := storage.WriteAndRename(ctx, tmpDir, filepath.Join(repodataDir, primaryName), bytes.NewReader(primaryGz)); err != nil {
			return d.recordFailure(ctx, fmt.Errorf("rpm regen: write primary: %w", err))
		}
		if _, err := storage.WriteAndRename(ctx, tmpDir, filepath.Join(repodataDir, filelistsName), bytes.NewReader(filelistsGz)); err != nil {
			return d.recordFailure(ctx, fmt.Errorf("rpm regen: write filelists: %w", err))
		}
		if _, err := storage.WriteAndRename(ctx, tmpDir, filepath.Join(repodataDir, otherName), bytes.NewReader(otherGz)); err != nil {
			return d.recordFailure(ctx, fmt.Errorf("rpm regen: write other: %w", err))
		}

		// 4. Build + write repomd.xml. Set timestamps to the regen start time
		// (createrepo_c uses time.Time of the source file; we use start as a
		// stable per-batch value).
		now := start.Unix()
		repomdBytes, err := WriteRepomd(
			&RepomdData{
				Checksum:     RepomdCksum{Type: "sha256", Value: primarySum},
				OpenChecksum: RepomdCksum{Type: "sha256", Value: primaryOpenSum},
				Location:     RepomdLoc{Href: "repodata/" + primaryName},
				Timestamp:    now, Size: primaryGzSize, OpenSize: primaryOpenSize,
			},
			&RepomdData{
				Checksum:     RepomdCksum{Type: "sha256", Value: filelistsSum},
				OpenChecksum: RepomdCksum{Type: "sha256", Value: filelistsOpenSum},
				Location:     RepomdLoc{Href: "repodata/" + filelistsName},
				Timestamp:    now, Size: filelistsGzSize, OpenSize: filelistsOpenSize,
			},
			&RepomdData{
				Checksum:     RepomdCksum{Type: "sha256", Value: otherSum},
				OpenChecksum: RepomdCksum{Type: "sha256", Value: otherOpenSum},
				Location:     RepomdLoc{Href: "repodata/" + otherName},
				Timestamp:    now, Size: otherGzSize, OpenSize: otherOpenSize,
			},
		)
		if err != nil {
			return d.recordFailure(ctx, fmt.Errorf("rpm regen: WriteRepomd: %w", err))
		}
		repomdPath := filepath.Join(repodataDir, "repomd.xml")
		if _, err := storage.WriteAndRename(ctx, tmpDir, repomdPath, bytes.NewReader(repomdBytes)); err != nil {
			return d.recordFailure(ctx, fmt.Errorf("rpm regen: write repomd.xml: %w", err))
		}

		// 5. Re-read on-disk repomd.xml, sign it, write .asc.
		// (Never sign bytes you didn't just fsync.)
		repomdOnDisk, err := os.ReadFile(repomdPath)
		if err != nil {
			return d.recordFailure(ctx, fmt.Errorf("rpm regen: re-read repomd: %w", err))
		}
		priv, err := d.SigningKeys.LookupPrivate(ctx, d.RepoID)
		if err != nil {
			return d.recordFailure(ctx, fmt.Errorf("rpm regen: lookup private key: %w", err))
		}
		sig, err := omrcrypto.DetachSign(priv, repomdOnDisk)
		// Drop the in-memory plaintext private key reference immediately
		// after the sign call.
		priv = ""
		_ = priv
		if err != nil {
			return d.recordFailure(ctx, fmt.Errorf("rpm regen: sign repomd: %w", err))
		}
		ascPath := filepath.Join(repodataDir, "repomd.xml.asc")
		if _, err := storage.WriteAndRename(ctx, tmpDir, ascPath, bytes.NewReader(sig)); err != nil {
			return d.recordFailure(ctx, fmt.Errorf("rpm regen: write repomd.xml.asc: %w", err))
		}

		// 6. Sweep stale content-hash files. Keep set is the three current
		// hash-named files + repomd.xml + repomd.xml.asc.
		keep := map[string]bool{
			primaryName: true, filelistsName: true, otherName: true,
			"repomd.xml": true, "repomd.xml.asc": true,
		}
		matches, _ := filepath.Glob(filepath.Join(repodataDir, "*.xml.gz"))
		for _, m := range matches {
			if !keep[filepath.Base(m)] {
				_ = os.Remove(m)
			}
		}

		// 7. Mark clean + clear last_regen_error.
		if err := d.DB.WriteTx(ctx, func(tx *sql.Tx) error {
			if err := d.Repos.SetLastRegenError(ctx, tx, d.RepoID, ""); err != nil {
				return err
			}
			return d.Repos.SetMetadataState(ctx, tx, d.RepoID, metadata.MetadataStateClean)
		}); err != nil {
			return fmt.Errorf("rpm regen: set clean: %w", err)
		}

		// Audit: regen + signing-key usage. Best-effort.
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
					"protocol":              "rpm",
					"duration_ms":           time.Since(start).Milliseconds(),
					"files_rewritten_count": 5, // 3 .xml.gz + repomd.xml + .asc
					"package_count":         len(parsed),
				},
			})
			_ = d.Audit.Record(ctx, audit.Event{
				Kind:       audit.EvtSigningKeyUsed,
				TargetKind: "repo",
				TargetID:   fmt.Sprintf("%d", d.RepoID),
				Outcome:    "ok",
				OccurredAt: time.Now().UTC(),
				Details: map[string]any{
					"protocol":         "rpm",
					"fingerprint_used": fp,
				},
			})
		}
		return nil
	}
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
				"protocol": "rpm",
				"error":    msg,
			},
		})
	}
	return cause
}

// stripSha256Prefix removes a leading "sha256:" so the value can land in the
// pkgid/checksum XML attributes (which carry just the hex digest).
func stripSha256Prefix(d string) string {
	const p = "sha256:"
	if len(d) > len(p) && d[:len(p)] == p {
		return d[len(p):]
	}
	return d
}
