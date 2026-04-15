package pypi

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
)

// File is the normalized representation of a parsed PyPI artifact. The
// handler writes this into a pypi_files row (via MarshalCoreMetadata for
// the core_metadata_json column).
type File struct {
	ProjectNormalized string
	Version           string
	Filename          string
	Kind              string // "wheel" or "sdist"
	RequiresPython    string
	Digest            string // sha256:<hex> — filled by caller, not Parse
	SizeBytes         int64  // filled by caller, not Parse
	// CoreMetadata holds the parsed RFC 822 headers. Multi-valued fields
	// (Classifier, Provides-Extra, Requires-Dist, etc.) arrive as
	// []string values; all others are string. Shape is compatible with
	// PEP 566 JSON.
	CoreMetadata map[string]any
	Summary      string // shortcut for CoreMetadata["Summary"] string value
}

// MarshalCoreMetadata returns the JSON encoding of f.CoreMetadata, or
// "{}" on nil / error. Safe to call on a zero-value File.
func (f *File) MarshalCoreMetadata() string {
	if f == nil || len(f.CoreMetadata) == 0 {
		return "{}"
	}
	b, err := json.Marshal(f.CoreMetadata)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// ParseWheel opens the .whl archive at wheelPath, reads the
// *.dist-info/METADATA entry via archive/zip (no extraction on disk —
// T-03-03-02 mitigation), parses its RFC 822 headers, and returns a
// populated *File.
//
// Filename is parsed per PEP 427: NAME-VERSION-PYTHON_TAG-ABI_TAG-PLATFORM_TAG.whl.
// The NAME segment is Normalize()'d.
func ParseWheel(wheelPath string) (*File, error) {
	if wheelPath == "" {
		return nil, errors.New("pypi: empty wheel path")
	}
	zr, err := zip.OpenReader(wheelPath)
	if err != nil {
		return nil, fmt.Errorf("pypi: open wheel: %w", err)
	}
	defer func() { _ = zr.Close() }()

	var metaFile *zip.File
	for _, zf := range zr.File {
		if strings.HasSuffix(zf.Name, ".dist-info/METADATA") {
			metaFile = zf
			break
		}
	}
	if metaFile == nil {
		return nil, errors.New("pypi: wheel missing *.dist-info/METADATA")
	}
	rc, err := metaFile.Open()
	if err != nil {
		return nil, fmt.Errorf("pypi: open METADATA: %w", err)
	}
	defer func() { _ = rc.Close() }()

	meta, err := parseRFC822(rc)
	if err != nil {
		return nil, fmt.Errorf("pypi: parse METADATA: %w", err)
	}

	base := path.Base(wheelPath)
	name, version, err := parseWheelFilename(base)
	if err != nil {
		return nil, err
	}
	// Prefer metadata's Name/Version when present; fall back to filename.
	if m := firstString(meta["Name"]); m != "" {
		name = m
	}
	if v := firstString(meta["Version"]); v != "" {
		version = v
	}

	f := &File{
		ProjectNormalized: Normalize(name),
		Version:           version,
		Filename:          base,
		Kind:              "wheel",
		RequiresPython:    firstString(meta["Requires-Python"]),
		CoreMetadata:      meta,
		Summary:           firstString(meta["Summary"]),
	}
	return f, nil
}

// ParseSdist opens the sdist at sdistPath (supports .tar.gz / .tgz / .zip),
// reads the top-level PKG-INFO, parses its RFC 822 headers, and returns a
// populated *File with Kind="sdist".
func ParseSdist(sdistPath string) (*File, error) {
	if sdistPath == "" {
		return nil, errors.New("pypi: empty sdist path")
	}
	base := path.Base(sdistPath)
	name, version, err := parseSdistFilename(base)
	if err != nil {
		return nil, err
	}

	var meta map[string]any
	switch {
	case strings.HasSuffix(base, ".tar.gz") || strings.HasSuffix(base, ".tgz"):
		meta, err = readSdistPKGINFOFromTarGz(sdistPath)
	case strings.HasSuffix(base, ".zip"):
		meta, err = readSdistPKGINFOFromZip(sdistPath)
	default:
		return nil, fmt.Errorf("pypi: unsupported sdist extension: %s", base)
	}
	if err != nil {
		return nil, err
	}
	if m := firstString(meta["Name"]); m != "" {
		name = m
	}
	if v := firstString(meta["Version"]); v != "" {
		version = v
	}
	return &File{
		ProjectNormalized: Normalize(name),
		Version:           version,
		Filename:          base,
		Kind:              "sdist",
		RequiresPython:    firstString(meta["Requires-Python"]),
		CoreMetadata:      meta,
		Summary:           firstString(meta["Summary"]),
	}, nil
}

// parseWheelFilename extracts (name, version) from a PEP 427 wheel
// filename: {distribution}-{version}(-{build})?-{python}-{abi}-{platform}.whl.
// Minimum 5 dash-separated segments after stripping .whl.
func parseWheelFilename(base string) (string, string, error) {
	if !strings.HasSuffix(base, ".whl") {
		return "", "", fmt.Errorf("pypi: wheel filename must end in .whl: %s", base)
	}
	stem := strings.TrimSuffix(base, ".whl")
	parts := strings.Split(stem, "-")
	if len(parts) < 5 {
		return "", "", fmt.Errorf("pypi: malformed wheel filename: %s", base)
	}
	return parts[0], parts[1], nil
}

// parseSdistFilename extracts (name, version) from an sdist archive
// filename: {name}-{version}.{tar.gz|tgz|zip}.
func parseSdistFilename(base string) (string, string, error) {
	stem := base
	switch {
	case strings.HasSuffix(stem, ".tar.gz"):
		stem = strings.TrimSuffix(stem, ".tar.gz")
	case strings.HasSuffix(stem, ".tgz"):
		stem = strings.TrimSuffix(stem, ".tgz")
	case strings.HasSuffix(stem, ".zip"):
		stem = strings.TrimSuffix(stem, ".zip")
	default:
		return "", "", fmt.Errorf("pypi: unsupported sdist extension: %s", base)
	}
	i := strings.LastIndex(stem, "-")
	if i < 1 || i == len(stem)-1 {
		return "", "", fmt.Errorf("pypi: malformed sdist filename: %s", base)
	}
	return stem[:i], stem[i+1:], nil
}

// readSdistPKGINFOFromTarGz opens path as a gzipped tar archive, finds the
// top-level PKG-INFO entry (first directory + "/PKG-INFO"), and parses it.
// The archive contents are never materialized to disk — scanned by streaming
// the tar reader — so path traversal via malicious filenames is impossible
// (T-03-03-02 mitigation).
func readSdistPKGINFOFromTarGz(p string) (map[string]any, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, fmt.Errorf("pypi: open sdist: %w", err)
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("pypi: gunzip sdist: %w", err)
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("pypi: tar next: %w", err)
		}
		if h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeRegA {
			continue
		}
		// Match either "<top>/PKG-INFO" or "./<top>/PKG-INFO".
		name := h.Name
		name = strings.TrimPrefix(name, "./")
		parts := strings.Split(name, "/")
		if len(parts) == 2 && parts[1] == "PKG-INFO" {
			return parseRFC822(io.LimitReader(tr, 10<<20))
		}
	}
	return nil, errors.New("pypi: sdist missing top-level PKG-INFO")
}

// readSdistPKGINFOFromZip opens path as a zip archive and returns the
// top-level PKG-INFO.
func readSdistPKGINFOFromZip(p string) (map[string]any, error) {
	zr, err := zip.OpenReader(p)
	if err != nil {
		return nil, fmt.Errorf("pypi: open sdist zip: %w", err)
	}
	defer func() { _ = zr.Close() }()
	for _, zf := range zr.File {
		name := strings.TrimPrefix(zf.Name, "./")
		parts := strings.Split(name, "/")
		if len(parts) == 2 && parts[1] == "PKG-INFO" {
			rc, err := zf.Open()
			if err != nil {
				return nil, fmt.Errorf("pypi: open PKG-INFO: %w", err)
			}
			defer func() { _ = rc.Close() }()
			return parseRFC822(io.LimitReader(rc, 10<<20))
		}
	}
	return nil, errors.New("pypi: sdist missing top-level PKG-INFO")
}

// parseRFC822 parses RFC 822-style headers (what PEP 566 / PEP 241 core
// metadata uses). Continuation lines (leading SP/TAB) fold into the
// preceding value. Multi-valued fields (appearing more than once) collect
// into a []string; single-valued fields stay string. The body after the
// first blank line becomes the "Description" key when present and no
// Description header was set.
func parseRFC822(r io.Reader) (map[string]any, error) {
	out := make(map[string]any)
	br := bufio.NewReader(r)
	var (
		lastKey string
		inBody  bool
		body    strings.Builder
	)
	for {
		line, err := br.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("pypi: read metadata: %w", err)
		}
		eof := errors.Is(err, io.EOF)
		trimmedNewline := strings.TrimRight(line, "\r\n")
		if inBody {
			body.WriteString(line)
			if eof {
				break
			}
			continue
		}
		if trimmedNewline == "" {
			// Blank line: header section done; remaining bytes are body.
			inBody = true
			if eof {
				break
			}
			continue
		}
		if (line[0] == ' ' || line[0] == '\t') && lastKey != "" {
			// Continuation of previous value.
			addToKey(out, lastKey, "\n"+strings.TrimLeft(trimmedNewline, " \t"))
			if eof {
				break
			}
			continue
		}
		colon := strings.IndexByte(trimmedNewline, ':')
		if colon <= 0 {
			if eof {
				break
			}
			continue
		}
		key := trimmedNewline[:colon]
		val := strings.TrimSpace(trimmedNewline[colon+1:])
		setKey(out, key, val)
		lastKey = key
		if eof {
			break
		}
	}
	if _, hasDesc := out["Description"]; !hasDesc {
		if b := strings.TrimSpace(body.String()); b != "" {
			out["Description"] = b
		}
	}
	if len(out) == 0 {
		return nil, errors.New("pypi: empty METADATA/PKG-INFO")
	}
	return out, nil
}

// setKey assigns val under key. If the key already exists, promotes to
// []string (multi-valued fields per PEP 566).
func setKey(m map[string]any, key, val string) {
	if v, ok := m[key]; ok {
		switch t := v.(type) {
		case string:
			m[key] = []string{t, val}
		case []string:
			m[key] = append(t, val)
		}
		return
	}
	m[key] = val
}

// addToKey appends suffix to the current value of key (continuation
// line). Handles both string and []string shapes.
func addToKey(m map[string]any, key, suffix string) {
	v, ok := m[key]
	if !ok {
		return
	}
	switch t := v.(type) {
	case string:
		m[key] = t + suffix
	case []string:
		if len(t) > 0 {
			t[len(t)-1] = t[len(t)-1] + suffix
			m[key] = t
		}
	}
}

// firstString returns the string value of v, or the first element when v
// is []string, or "" otherwise.
func firstString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []string:
		if len(t) > 0 {
			return t[0]
		}
	}
	return ""
}
