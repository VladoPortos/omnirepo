// Package deb — .deb parser. Composes blakesmith/ar (outer archive),
// stdlib archive/tar, and three decompression codecs (compress/gzip,
// ulikunitz/xz, klauspost/compress/zstd) to extract the inner
// control.tar.{gz,xz,zst} → ./control RFC 822 paragraph.
//
// Trust boundary: the body is adversarial (uploader-supplied). Every layer
// enforces a cap:
//
//   - outer ar: blakesmith/ar errors on bad magic / alignment → 400 by caller.
//   - tar: stdlib; we cap the control file read at 1 MiB via io.LimitReader.
//   - control body: parsed line-by-line with bufio.Scanner using a 1 MiB buffer.
//
// ParseDeb returns the canonical Control struct with the Raw field carrying
// the control paragraph bytes byte-for-byte (so Packages regeneration can
// preserve Description folding discipline without a round-trip).
package deb

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"

	"github.com/blakesmith/ar"
	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"
)

// controlMaxBytes caps the inner ./control file at 1 MiB. Legit .deb control
// files are a few KiB; anything over 1 MiB is a zip-bomb or malformed. T-03-05-02.
const controlMaxBytes = 1 << 20

// Control is the parsed inner ./control paragraph. Raw carries the original
// bytes so regen can preserve Description folding without re-serializing.
type Control struct {
	Package       string
	Version       string
	Architecture  string
	Maintainer    string
	Section       string
	Priority      string
	Depends       string
	PreDepends    string
	Recommends    string
	Suggests      string
	Conflicts     string
	Provides      string
	Replaces      string
	Description   string
	Homepage      string
	InstalledSize int64
	Raw           string // full control paragraph bytes (LF line endings)
}

// ParseDeb reads a .deb archive from r, locates the inner control.tar.*
// member, decompresses it, and returns the parsed ./control paragraph.
func ParseDeb(r io.Reader) (*Control, error) {
	rd := ar.NewReader(r)
	for {
		hdr, err := rd.Next()
		if errors.Is(err, io.EOF) {
			return nil, errors.New("deb: control.tar.* not found in ar archive")
		}
		if err != nil {
			return nil, fmt.Errorf("deb: ar read: %w", err)
		}
		// ar member names are fixed-width padded with spaces and sometimes
		// end in "/" (GNU convention).
		name := strings.TrimRight(hdr.Name, "/ ")
		if !strings.HasPrefix(name, "control.tar") {
			// Skip silently; io.Copy drains to keep ar stream aligned.
			if _, err := io.Copy(io.Discard, rd); err != nil {
				return nil, fmt.Errorf("deb: skip member %q: %w", name, err)
			}
			continue
		}
		return decodeControlTar(name, rd)
	}
}

// decodeControlTar picks the right decompressor for name and extracts the
// ./control file from the inner tar.
func decodeControlTar(name string, raw io.Reader) (*Control, error) {
	var dec io.Reader
	switch {
	case name == "control.tar":
		dec = raw
	case strings.HasSuffix(name, ".gz"):
		gz, err := gzip.NewReader(raw)
		if err != nil {
			return nil, fmt.Errorf("deb: gzip.NewReader: %w", err)
		}
		defer func() { _ = gz.Close() }()
		dec = gz
	case strings.HasSuffix(name, ".xz"):
		xr, err := xz.NewReader(raw)
		if err != nil {
			return nil, fmt.Errorf("deb: xz.NewReader: %w", err)
		}
		dec = xr
	case strings.HasSuffix(name, ".zst"):
		zr, err := zstd.NewReader(raw)
		if err != nil {
			return nil, fmt.Errorf("deb: zstd.NewReader: %w", err)
		}
		defer zr.Close()
		dec = zr
	default:
		return nil, fmt.Errorf("deb: unknown compression on %q", name)
	}
	return readControlFromTar(dec)
}

// readControlFromTar walks the inner tar until it finds the ./control file.
func readControlFromTar(r io.Reader) (*Control, error) {
	tr := tar.NewReader(r)
	for {
		th, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, errors.New("deb: control file not found in control.tar")
		}
		if err != nil {
			return nil, fmt.Errorf("deb: tar read: %w", err)
		}
		if path.Base(th.Name) != "control" {
			continue
		}
		body, err := io.ReadAll(io.LimitReader(tr, controlMaxBytes+1))
		if err != nil {
			return nil, fmt.Errorf("deb: read control body: %w", err)
		}
		if len(body) > controlMaxBytes {
			return nil, fmt.Errorf("deb: control file exceeds %d bytes", controlMaxBytes)
		}
		return ParseControlParagraph(body)
	}
}

// ParseControlParagraph parses one RFC 822-style control paragraph. Keys are
// case-sensitive per dpkg convention (canonical case used here). Continuation
// lines begin with a single space or tab and are preserved byte-for-byte in
// the returned Description value (so Packages regen can re-emit them).
//
// Unknown keys are captured into Raw but not surfaced through struct fields.
func ParseControlParagraph(body []byte) (*Control, error) {
	// Normalize CRLF → LF so downstream code sees canonical separators, but
	// keep the Raw as the normalized form for deterministic regen output.
	norm := bytes.ReplaceAll(body, []byte("\r\n"), []byte("\n"))
	// Strip trailing blank lines / paragraph separators so Raw contains only
	// the control block itself (no trailing blank line).
	norm = bytes.TrimRight(norm, "\n")

	c := &Control{Raw: string(norm) + "\n"} // always one trailing \n

	var (
		curKey string
		curVal strings.Builder
	)
	flush := func() {
		if curKey == "" {
			return
		}
		setControlField(c, curKey, curVal.String())
		curKey = ""
		curVal.Reset()
	}

	sc := bufio.NewScanner(bytes.NewReader(norm))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			// RFC 822 paragraph separator — the caller gave us a single
			// paragraph so stop on any blank line.
			break
		}
		if line[0] == ' ' || line[0] == '\t' {
			// Continuation line: append verbatim including the leading
			// single space (Debian convention for Description folding).
			if curKey == "" {
				return nil, errors.New("deb: control: continuation with no preceding field")
			}
			curVal.WriteByte('\n')
			curVal.WriteString(line)
			continue
		}
		// New field — flush previous.
		flush()
		colon := strings.IndexByte(line, ':')
		if colon <= 0 {
			return nil, fmt.Errorf("deb: control: malformed line %q", line)
		}
		curKey = line[:colon]
		val := line[colon+1:]
		// Field body: single-space separator is canonical. Trim the first
		// leading space only; keep the rest (including tabs) intact.
		if len(val) > 0 && val[0] == ' ' {
			val = val[1:]
		}
		curVal.WriteString(val)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("deb: control scan: %w", err)
	}
	flush()
	if c.Package == "" {
		return nil, errors.New("deb: control: missing Package field")
	}
	if c.Version == "" {
		return nil, errors.New("deb: control: missing Version field")
	}
	if c.Architecture == "" {
		return nil, errors.New("deb: control: missing Architecture field")
	}
	return c, nil
}

// setControlField dispatches a key/value onto the canonical struct field.
// Unknown keys are silently ignored (Raw captures them).
func setControlField(c *Control, key, val string) {
	switch key {
	case "Package":
		c.Package = val
	case "Version":
		c.Version = val
	case "Architecture":
		c.Architecture = val
	case "Maintainer":
		c.Maintainer = val
	case "Section":
		c.Section = val
	case "Priority":
		c.Priority = val
	case "Depends":
		c.Depends = val
	case "Pre-Depends":
		c.PreDepends = val
	case "Recommends":
		c.Recommends = val
	case "Suggests":
		c.Suggests = val
	case "Conflicts":
		c.Conflicts = val
	case "Provides":
		c.Provides = val
	case "Replaces":
		c.Replaces = val
	case "Description":
		c.Description = val
	case "Homepage":
		c.Homepage = val
	case "Installed-Size":
		if n, err := strconv.ParseInt(strings.TrimSpace(val), 10, 64); err == nil {
			c.InstalledSize = n
		}
	}
}
