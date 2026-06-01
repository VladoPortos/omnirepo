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
	"bufio"
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
	"github.com/cavaliergopher/cpio"
	"github.com/cavaliergopher/rpm"
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

// resolveDebPoolPath returns the pool-relative path for one .deb row.
//
// F-T6 follow-up: prefer the stored `storage_pool_path` column (the exact
// path the client PUT to, e.g. pool/main/libz/libzstd/zstd_….deb). The
// synthesised layout below collapses source-package information — a
// `zstd` binary from the `libzstd` source package lives at
// pool/main/libz/libzstd/, not pool/main/z/zstd/ — so the materializer
// looked at the wrong path and the scan failed "file missing on disk"
// forever. Falls back to the legacy synthesis when the column is empty
// (pre-migration rows that were never re-PUT).
func (h *Handler) resolveDebPoolPath(ctx context.Context, repoID int64, filename string) (string, error) {
	if h.deps.DB == nil {
		return "", errors.New("db not wired")
	}
	var pkg, component, storagePoolPath string
	err := h.deps.DB.Reader.QueryRowContext(ctx, `
		SELECT d.package, COALESCE(s.component, 'main'), COALESCE(d.storage_pool_path, '')
		FROM deb_packages d
		LEFT JOIN apt_suites s ON s.id = d.suite_id
		WHERE d.repo_id = ? AND d.filename = ?
	`, repoID, filename).Scan(&pkg, &component, &storagePoolPath)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("deb package %q not found in repo %d", filename, repoID)
	}
	if err != nil {
		return "", fmt.Errorf("deb pool path lookup: %w", err)
	}
	if storagePoolPath != "" {
		return storagePoolPath, nil
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
		// Swallow — Trivy still scans the raw .tgz dir below.
		_ = extractTarGz(rawCopy, extracted)
	case "pypi":
		// Same: raw .whl still present.
		_ = extractWheel(rawCopy, extracted, artifactID)
	case "deb":
		_ = extractDeb(rawCopy, extracted)
	case "rpm":
		// S-1: extract the RPM payload so Trivy's language / OS-package
		// analyzers see the real file list. Extraction failures are
		// non-fatal — the raw .rpm remains in place and Trivy falls back
		// to its RPM-header analyzer.
		_ = extractRPM(rawCopy, extracted)
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
// Additionally writes a `requirements.txt` so Trivy's language scanner
// detects the package-under-scan and its transitive deps as installed
// Python dependencies (F-4 / S-2: otherwise bare-METADATA wheels surface
// zero findings even for known-vulnerable versions, and transitive deps
// never got represented at all).
//
// Requirements built from two sources:
//  1. the wheel filename itself: `<name>-<version>-...whl` → `name==version`
//  2. the wheel's `*.dist-info/METADATA` file: every `Requires-Dist:`
//     header → normalized pip-syntax line. Environment markers
//     (`; python_version >= "3"`) are stripped — we want Trivy to see
//     every dep regardless of runtime conditionals.
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

	var lines []string
	// 1. Package under scan from the filename.
	base := strings.TrimSuffix(wheelFilename, ".whl")
	parts := strings.Split(base, "-")
	if len(parts) >= 2 {
		lines = append(lines, fmt.Sprintf("%s==%s", parts[0], parts[1]))
	}
	// 2. Transitive deps from METADATA.
	lines = append(lines, requirementsFromMetadata(dstDir)...)
	if len(lines) > 0 {
		body := strings.Join(lines, "\n") + "\n"
		_ = os.WriteFile(filepath.Join(dstDir, "requirements.txt"), []byte(body), 0o640)
	}
	return nil
}

// requirementsFromMetadata walks dstDir looking for the wheel's
// `*.dist-info/METADATA` file and returns one normalized pip-compatible
// requirement line per `Requires-Dist` header. Best-effort: any parse
// failure returns an empty slice, NOT an error.
func requirementsFromMetadata(dstDir string) []string {
	var metaPath string
	_ = filepath.Walk(dstDir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info == nil {
			return nil
		}
		if info.IsDir() {
			return nil
		}
		if strings.HasSuffix(filepath.Dir(p), ".dist-info") && filepath.Base(p) == "METADATA" {
			metaPath = p
			return filepath.SkipAll
		}
		return nil
	})
	if metaPath == "" {
		return nil
	}
	body, err := os.ReadFile(metaPath)
	if err != nil {
		return nil
	}
	var out []string
	for _, raw := range strings.Split(string(body), "\n") {
		// METADATA headers end at the first blank line (RFC 822-ish);
		// once we see one, the body of the file follows (README etc.)
		// and has no Requires-Dist of interest.
		if strings.TrimSpace(raw) == "" {
			break
		}
		line, ok := strings.CutPrefix(raw, "Requires-Dist:")
		if !ok {
			continue
		}
		if norm := normalizeRequiresDist(line); norm != "" {
			out = append(out, norm)
		}
	}
	return out
}

// normalizeRequiresDist converts one PEP 508 Requires-Dist value into a
// pip-friendly line: drops environment markers, strips wrapping parens
// around the version spec, collapses whitespace. Returns an empty string
// when the input is blank after trimming.
//
// Examples:
//
//	"requests (>=2.25.0)"                       → "requests>=2.25.0"
//	"urllib3<2"                                 → "urllib3<2"
//	"idna (>=2.5,<4); python_version >= \"3\""  → "idna>=2.5,<4"
//	"pytest[testing] (>=6)"                     → "pytest>=6"
func normalizeRequiresDist(s string) string {
	// Drop the environment marker first — everything after the semicolon
	// is matcher syntax for runtime Python, useless to Trivy.
	if i := strings.IndexByte(s, ';'); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	// Separate the package identifier from any version spec. The
	// identifier starts with [A-Za-z0-9_.-] and may carry a [extras]
	// block right after.
	i := 0
	for i < len(s) {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
			c == '_' || c == '.' || c == '-' {
			i++
			continue
		}
		break
	}
	name := s[:i]
	rest := s[i:]
	// Strip optional extras [foo,bar].
	rest = strings.TrimLeft(rest, " \t")
	if strings.HasPrefix(rest, "[") {
		if end := strings.IndexByte(rest, ']'); end >= 0 {
			rest = rest[end+1:]
		}
	}
	// Strip surrounding parens on the version spec.
	rest = strings.TrimSpace(rest)
	rest = strings.TrimPrefix(rest, "(")
	rest = strings.TrimSuffix(rest, ")")
	rest = strings.TrimSpace(rest)
	// Remove whitespace inside the version spec so Trivy's strict parser
	// handles it: "requests >= 2.0" → "requests>=2.0".
	rest = strings.ReplaceAll(rest, " ", "")
	if name == "" {
		return ""
	}
	return name + rest
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
		case tar.TypeReg:
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

// extractRPM unpacks the payload of an RPM package into dstDir. RPM files
// are: [lead][signature-header][main-header][compressed-cpio-archive].
// rpm.Read(r) consumes the first three; PayloadCompression() names the
// compressor (xz / gzip / zstd — we handle xz and gzip, which cover every
// RPM we've seen in the field as of 2026). The cpio archive is then walked
// entry by entry with github.com/cavaliergopher/cpio.
//
// Walkthrough gotcha: cpio paths inside RPMs are rooted at "./" (e.g.
// "./usr/bin/curl"). strip the leading "./" before applying the zip-slip
// guard so clean paths don't silently get rejected.
func extractRPM(srcPath, dstDir string) error {
	f, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	br := bufio.NewReader(f)
	pkg, err := rpm.Read(br)
	if err != nil {
		return fmt.Errorf("rpm header: %w", err)
	}

	var payload io.Reader = br
	switch comp := pkg.PayloadCompression(); comp {
	case "xz":
		xr, xerr := xz.NewReader(br)
		if xerr != nil {
			return fmt.Errorf("rpm xz: %w", xerr)
		}
		payload = xr
	case "gzip":
		gz, gerr := gzip.NewReader(br)
		if gerr != nil {
			return fmt.Errorf("rpm gzip: %w", gerr)
		}
		defer func() { _ = gz.Close() }()
		payload = gz
	case "", "none":
		// Uncompressed cpio — accepted by the cpio reader as-is.
	default:
		return fmt.Errorf("rpm payload compression %q not supported", comp)
	}

	// PayloadFormat is typically "cpio"; RPMs using "drpm" (delta RPM) have
	// a fundamentally different format and show up as uploads rarely. We
	// treat anything other than cpio as a no-op rather than corrupting
	// dstDir.
	if fmtName := pkg.PayloadFormat(); fmtName != "" && fmtName != "cpio" {
		return fmt.Errorf("rpm payload format %q not supported", fmtName)
	}

	cr := cpio.NewReader(payload)
	for {
		hdr, cerr := cr.Next()
		if cerr == io.EOF {
			return nil
		}
		if cerr != nil {
			return cerr
		}
		clean := filepath.Clean(strings.TrimPrefix(hdr.Name, "./"))
		if clean == "" || clean == "." || strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
			continue
		}
		target := filepath.Join(dstDir, clean)
		if !strings.HasPrefix(target, dstDir+string(filepath.Separator)) && target != dstDir {
			continue
		}
		mode := hdr.Mode
		switch {
		case mode.IsDir():
			if err := os.MkdirAll(target, 0o750); err != nil {
				return err
			}
		case mode.IsRegular():
			if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, cr); err != nil {
				_ = out.Close()
				return err
			}
			if err := out.Close(); err != nil {
				return err
			}
		default:
			// Symlinks / devices / hardlinks: skipped. Trivy scans only
			// regular files anyway.
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
