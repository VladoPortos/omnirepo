package scan

// Archive materializers for the package-style artifact kinds (rpm, deb,
// pypi, helm). Each one locates the uploaded archive on disk, copies it
// into the scan tmp dir, and — where feasible — extracts the contents so
// Trivy's filesystem scanner can inspect them.
//
// Trivy's value per format:
//   - helm .tgz: misconfig scanner surfaces Kubernetes misconfigurations
//     in templates/*.yaml (ImagePolicyAlways, privileged containers, etc.)
//   - pypi wheel: we also drop a minimal requirements.txt so vuln scanner
//     detects the package itself via pip-audit data.
//   - deb: extracted filesystem may contain language lockfiles / secrets.
//   - rpm: cpio extraction isn't stdlib — the raw .rpm file is kept in
//     place, and Trivy sees it as an unknown blob. Scans complete without
//     findings rather than erroring. (F-4 partial; cpio support is a
//     follow-up.)
//
// All materializers share the same contract: on success the returned dir
// is a fresh tmp subtree under `dstParent` containing the extracted (or
// at least the raw) artifact; caller runs Runner.Filesystem against it.
// Cleanup is the caller's responsibility (scan.handler uses defer os.RemoveAll).

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	arlib "github.com/blakesmith/ar"
	"github.com/ulikunitz/xz"
)

// resolveArtifactFileOnDisk converts (repoID, filename) into the absolute
// path of the stored artifact for the given kind. Uses the storage-key
// convention each protocol handler wrote to:
//
//	rpm   → <DataRoot>/repos/<proj>/rpm/<repo>/packages/<filename>
//	pypi  → <DataRoot>/repos/<proj>/pypi/<repo>/packages/<filename>
//	helm  → <DataRoot>/repos/<proj>/helm/<repo>/charts/<filename>
//	deb   → derived from deb_packages.filename + apt_suites → pool path
func (h *Handler) resolveArtifactFileOnDisk(ctx context.Context, kind string, repoID int64, artifactID string) (string, error) {
	if h.deps.Repos == nil || h.deps.Projects == nil {
		return "", errors.New("repos / projects store not wired")
	}
	repo, err := h.deps.Repos.FindByID(ctx, repoID)
	if err != nil || repo == nil {
		return "", fmt.Errorf("repo %d lookup: %w", repoID, err)
	}
	proj, err := h.deps.Projects.FindByID(ctx, repo.ProjectID)
	if err != nil || proj == nil {
		return "", fmt.Errorf("project %d lookup: %w", repo.ProjectID, err)
	}
	root := filepath.Join(h.deps.DataRoot, "repos", proj.Name)

	switch kind {
	case "rpm":
		return filepath.Join(root, "rpm", repo.Name, "packages", artifactID), nil
	case "pypi":
		return filepath.Join(root, "pypi", repo.Name, "packages", artifactID), nil
	case "helm":
		return filepath.Join(root, "helm", repo.Name, "charts", artifactID), nil
	case "deb":
		// Look up the deb_packages row to get suite+component, then build
		// the pool path: pool/<component>/<letter-or-lib-prefix>/<pkg>/<filename>.
		// The "letter or lib prefix" convention from Debian: single-letter
		// directory per package, except packages starting with "lib" use
		// "lib<letter>" (e.g., libssl → libs/libssl/...).
		poolPath, perr := h.resolveDebPoolPath(ctx, repoID, artifactID)
		if perr != nil {
			return "", perr
		}
		return filepath.Join(root, "deb", repo.Name, poolPath), nil
	}
	return "", fmt.Errorf("unknown package kind %q", kind)
}

// resolveDebPoolPath reconstructs the pool-relative path for one .deb row.
// Returns paths like `pool/main/h/hello/hello_2.10-3_amd64.deb`.
func (h *Handler) resolveDebPoolPath(ctx context.Context, repoID int64, filename string) (string, error) {
	if h.deps.DB == nil {
		return "", errors.New("db not wired")
	}
	var pkg, component string
	err := h.deps.DB.Reader.QueryRowContext(ctx, `
		SELECT d.package, COALESCE(s.component, 'main')
		FROM deb_packages d
		LEFT JOIN apt_suites s ON s.id = d.suite_id
		WHERE d.repo_id = ? AND d.filename = ?
	`, repoID, filename).Scan(&pkg, &component)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("deb package %q not found in repo %d", filename, repoID)
	}
	if err != nil {
		return "", fmt.Errorf("deb pool path lookup: %w", err)
	}
	letter := debPoolLetter(pkg)
	return filepath.Join("pool", component, letter, pkg, filename), nil
}

// debPoolLetter returns the Debian pool subdirectory for a package name.
// Standard: first char of the package; packages starting with "lib" use
// "lib" followed by the fourth char (e.g., libssl → libs).
func debPoolLetter(pkg string) string {
	if strings.HasPrefix(pkg, "lib") && len(pkg) > 3 {
		return "lib" + string(pkg[3])
	}
	if pkg == "" {
		return "x"
	}
	return string(pkg[0])
}

// materializePackage is the generic entry point used by the scan pool
// handlers for rpm/deb/pypi/helm. Returns the scan-root directory to pass
// to Runner.Filesystem.
func (h *Handler) materializePackage(ctx context.Context, dstDir, kind string, repoID int64, artifactID string) (string, error) {
	srcPath, err := h.resolveArtifactFileOnDisk(ctx, kind, repoID, artifactID)
	if err != nil {
		return "", err
	}
	if _, serr := os.Stat(srcPath); serr != nil {
		return "", fmt.Errorf("artifact file missing on disk: %w", serr)
	}
	if err := os.MkdirAll(dstDir, 0o750); err != nil {
		return "", fmt.Errorf("mkdir tmp: %w", err)
	}
	// Always copy the archive itself so the scan sees at least the raw
	// blob (helps with secret scanning on opaque payloads).
	srcBase := filepath.Base(srcPath)
	if srcBase == "" || srcBase == "." || srcBase == "/" {
		srcBase = "artifact"
	}
	rawCopy := filepath.Join(dstDir, srcBase)
	if err := copyFile(srcPath, rawCopy); err != nil {
		return "", fmt.Errorf("copy archive: %w", err)
	}

	// Best-effort extraction into a sibling "extracted" subdir. Extraction
	// failures don't fail the scan — the raw archive is still present.
	extracted := filepath.Join(dstDir, "extracted")
	_ = os.MkdirAll(extracted, 0o750)
	switch kind {
	case "helm":
		if err := extractTarGz(rawCopy, extracted); err != nil {
			// Swallow — Trivy still scans the raw .tgz dir below.
		}
	case "pypi":
		if err := extractWheel(rawCopy, extracted, artifactID); err != nil {
			// Same: raw .whl still present.
		}
	case "deb":
		_ = extractDeb(rawCopy, extracted)
	case "rpm":
		// cpio extraction isn't wired yet (F-4 follow-up). Trivy will
		// see only the raw .rpm; scan completes with zero findings.
	}
	return dstDir, nil
}

// extractTarGz expands a .tgz / .tar.gz archive into dstDir.
func extractTarGz(srcPath, dstDir string) error {
	f, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer func() { _ = gz.Close() }()
	return extractTarInto(tar.NewReader(gz), dstDir)
}

// extractWheel unzips a Python wheel (.whl is a zip archive) into dstDir.
// Additionally writes a minimal `requirements.txt` so Trivy's language
// scanner detects the package-under-scan as an installed Python dependency
// and matches it against the vuln DB (F-4: otherwise bare-METADATA wheels
// surface zero findings even for known-vulnerable versions).
func extractWheel(srcPath, dstDir, wheelFilename string) error {
	zr, err := zip.OpenReader(srcPath)
	if err != nil {
		return err
	}
	defer func() { _ = zr.Close() }()
	for _, zf := range zr.File {
		if err := extractOneZip(zf, dstDir); err != nil {
			return err
		}
	}
	// Synthesize requirements.txt from the wheel filename:
	//   <name>-<version>-<python>-<abi>-<platform>.whl
	// Trivy's python/pip analyzer reads `name==version` lines.
	base := strings.TrimSuffix(wheelFilename, ".whl")
	parts := strings.Split(base, "-")
	if len(parts) >= 2 {
		name, version := parts[0], parts[1]
		req := fmt.Sprintf("%s==%s\n", name, version)
		_ = os.WriteFile(filepath.Join(dstDir, "requirements.txt"), []byte(req), 0o640)
	}
	return nil
}

// extractDeb unpacks a Debian .deb (which is an ar archive of
// debian-binary, control.tar.*, and data.tar.*) into dstDir. Only the
// data.tar.* is actually extracted — that's where the package's files
// live and is what Trivy's filesystem scanner needs.
func extractDeb(srcPath, dstDir string) error {
	f, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	ar := arlib.NewReader(f)
	for {
		hdr, err := ar.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name := strings.TrimRight(hdr.Name, "/ ")
		if !strings.HasPrefix(name, "data.tar") {
			// Drain non-data entries so the ar reader advances.
			if _, derr := io.Copy(io.Discard, ar); derr != nil {
				return derr
			}
			continue
		}
		body, err := io.ReadAll(ar)
		if err != nil {
			return err
		}
		var tr *tar.Reader
		switch {
		case strings.HasSuffix(name, ".gz"):
			gz, err := gzip.NewReader(bytes.NewReader(body))
			if err != nil {
				return err
			}
			defer func() { _ = gz.Close() }()
			tr = tar.NewReader(gz)
		case strings.HasSuffix(name, ".xz"):
			xr, err := xz.NewReader(bytes.NewReader(body))
			if err != nil {
				return err
			}
			tr = tar.NewReader(xr)
		case strings.HasSuffix(name, ".tar"):
			tr = tar.NewReader(bytes.NewReader(body))
		default:
			// Zstd/other: skip — trivy will still see the raw .deb.
			return nil
		}
		return extractTarInto(tr, dstDir)
	}
}

// extractTarInto reads every regular-file entry in tr and writes it under
// dstDir. Zero-slip guards (Zip Slip equivalent) reject entries whose
// cleaned path escapes dstDir.
func extractTarInto(tr *tar.Reader, dstDir string) error {
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		clean := filepath.Clean(hdr.Name)
		if strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
			continue
		}
		target := filepath.Join(dstDir, clean)
		if !strings.HasPrefix(target, dstDir) {
			continue
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o750); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				_ = f.Close()
				return err
			}
			if err := f.Close(); err != nil {
				return err
			}
		default:
			// Symlinks, hardlinks, devices: skip.
		}
	}
}

// extractOneZip writes a single zip entry to dstDir with the usual path-
// traversal checks.
func extractOneZip(zf *zip.File, dstDir string) error {
	clean := filepath.Clean(zf.Name)
	if strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
		return nil
	}
	target := filepath.Join(dstDir, clean)
	if !strings.HasPrefix(target, dstDir) {
		return nil
	}
	if zf.FileInfo().IsDir() {
		return os.MkdirAll(target, 0o750)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
		return err
	}
	rc, err := zf.Open()
	if err != nil {
		return err
	}
	defer func() { _ = rc.Close() }()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, rc); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
