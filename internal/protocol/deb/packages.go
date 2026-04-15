// Package deb — Packages / Release / InRelease writers.
//
// The Packages file is the concatenation of per-package control paragraphs
// separated by blank lines (one trailing blank line after the last paragraph
// is omitted; byte-for-byte this is `para1\n\npara2\n`). Packages.gz MUST
// gzip the EXACT bytes of Packages — any trailing-newline drift triggers
// "Hash Sum mismatch" in apt (anti-pattern guard T-03-05-09).
//
// The Release file is a single control paragraph containing MD5Sum:/SHA256:
// index blocks whose entries are 1-space-indented "<hex>  <size>  <path>"
// lines. Debian repo format: https://wiki.debian.org/DebianRepository/Format
package deb

import (
	"bytes"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ReleaseFileEntry is one row of the MD5Sum:/SHA256: index in Release.
// Path is slash-separated relative to the suite dir (e.g.
// "main/binary-amd64/Packages").
type ReleaseFileEntry struct {
	Path   string
	Size   int64
	MD5    string // hex, lowercase
	SHA256 string // hex, lowercase
}

// ReleaseInfo is the metadata for the Release header block.
type ReleaseInfo struct {
	Origin        string
	Label         string
	Suite         string
	Codename      string
	Description   string
	Architectures []string
	Components    []string
	Date          time.Time // emitted in RFC 1123 UTC
	Files         []ReleaseFileEntry
}

// PackagesEntry is one record fed to WritePackages. Control carries the
// pre-assembled control paragraph (no trailing Filename/Size/SHA256/MD5sum);
// WritePackages appends those four fields in canonical order.
//
// Control MUST end in a single "\n".
type PackagesEntry struct {
	Control  string
	Filename string
	Size     int64
	SHA256   string // hex, lowercase
	MD5      string // hex, lowercase
}

// WritePackages serializes entries into the canonical Packages byte stream.
// Paragraphs are separated by a single blank line. No trailing blank line
// after the final paragraph — matches `apt-ftparchive` output.
//
// Format per entry:
//
//	<Control paragraph>   (already newline-terminated)
//	Filename: <path>
//	Size: <bytes>
//	MD5sum: <md5hex>
//	SHA256: <sha256hex>
//
// (blank line between entries)
func WritePackages(entries []PackagesEntry) []byte {
	var buf bytes.Buffer
	for i, e := range entries {
		if i > 0 {
			buf.WriteByte('\n')
		}
		// Ensure the control paragraph ends with exactly one newline.
		ctrl := strings.TrimRight(e.Control, "\n") + "\n"
		buf.WriteString(ctrl)
		fmt.Fprintf(&buf, "Filename: %s\n", e.Filename)
		fmt.Fprintf(&buf, "Size: %d\n", e.Size)
		if e.MD5 != "" {
			fmt.Fprintf(&buf, "MD5sum: %s\n", e.MD5)
		}
		if e.SHA256 != "" {
			fmt.Fprintf(&buf, "SHA256: %s\n", e.SHA256)
		}
	}
	return buf.Bytes()
}

// WriteRelease produces the Release file body per Debian repo format spec.
// Mandatory fields emitted: Origin, Label, Suite, Codename, Date,
// Architectures, Components, Description, MD5Sum:, SHA256:.
//
// Entries inside each hash block are sorted by Path for determinism.
func WriteRelease(info ReleaseInfo) []byte {
	var buf bytes.Buffer
	writeField := func(key, val string) {
		if val == "" {
			return
		}
		fmt.Fprintf(&buf, "%s: %s\n", key, val)
	}
	writeField("Origin", info.Origin)
	writeField("Label", info.Label)
	writeField("Suite", info.Suite)
	writeField("Codename", info.Codename)
	// RFC 1123 in UTC is what apt expects (e.g. "Tue, 15 Apr 2026 12:34:56 UTC").
	writeField("Date", info.Date.UTC().Format(time.RFC1123))
	writeField("Architectures", strings.Join(info.Architectures, " "))
	writeField("Components", strings.Join(info.Components, " "))
	writeField("Description", info.Description)

	files := make([]ReleaseFileEntry, len(info.Files))
	copy(files, info.Files)
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })

	// MD5Sum block: Debian convention is to emit MD5Sum: even if hashes are
	// identical to modern SHA256 (apt consumes SHA256 preferentially but
	// some mirror tooling still looks for MD5Sum).
	buf.WriteString("MD5Sum:\n")
	for _, f := range files {
		// Padding is cosmetic — apt tolerates single-space. Use a single
		// space + size field width that's consistent.
		fmt.Fprintf(&buf, " %s %s %s\n", f.MD5, padSize(f.Size), f.Path)
	}
	buf.WriteString("SHA256:\n")
	for _, f := range files {
		fmt.Fprintf(&buf, " %s %s %s\n", f.SHA256, padSize(f.Size), f.Path)
	}
	return buf.Bytes()
}

// padSize right-aligns size in a 16-column field, matching apt-ftparchive
// cosmetics. Apt does not require padding; we emit it for readability.
func padSize(size int64) string {
	s := strconv.FormatInt(size, 10)
	const width = 16
	if len(s) >= width {
		return s
	}
	return strings.Repeat(" ", width-len(s)) + s
}

// ComputeMD5SHA256 hashes b and returns (md5hex, sha256hex, size).
func ComputeMD5SHA256(b []byte) (md5hex, sha256hex string, size int64) {
	m := md5.Sum(b)
	s := sha256.Sum256(b)
	return hex.EncodeToString(m[:]), hex.EncodeToString(s[:]), int64(len(b))
}

// reconstructControlParagraph emits a control paragraph from stored DEBPackage
// fields in the canonical dpkg order. Empty optional fields are omitted.
// Mandatory fields (Package, Version, Architecture, Maintainer, Description)
// are always emitted — callers must have populated them.
//
// Description preserves folding byte-for-byte if the stored value already
// uses the single-leading-space continuation convention. Callers feed the
// stored value straight in; we do not re-wrap.
//
// The canonical field order is:
//
//	Package, Version, Architecture, Maintainer, Installed-Size,
//	Depends, Pre-Depends, Recommends, Suggests, Conflicts, Provides, Replaces,
//	Section, Priority, Homepage, Description
//
// NOTE: Filename, Size, MD5sum, SHA256 are appended by WritePackages, not here.
func reconstructControlParagraph(p storedPkg) string {
	var buf strings.Builder
	emit := func(key, val string) {
		if val == "" {
			return
		}
		buf.WriteString(key)
		buf.WriteString(": ")
		buf.WriteString(val)
		buf.WriteByte('\n')
	}
	// Mandatory fields first.
	buf.WriteString("Package: ")
	buf.WriteString(p.Package)
	buf.WriteByte('\n')
	buf.WriteString("Version: ")
	buf.WriteString(p.Version)
	buf.WriteByte('\n')
	buf.WriteString("Architecture: ")
	buf.WriteString(p.Architecture)
	buf.WriteByte('\n')
	emit("Maintainer", p.Maintainer)
	if p.InstalledSize > 0 {
		fmt.Fprintf(&buf, "Installed-Size: %d\n", p.InstalledSize)
	}
	emit("Depends", p.Depends)
	emit("Pre-Depends", p.PreDepends)
	emit("Recommends", p.Recommends)
	emit("Suggests", p.Suggests)
	emit("Conflicts", p.Conflicts)
	emit("Provides", p.Provides)
	emit("Replaces", p.Replaces)
	emit("Section", p.Section)
	emit("Priority", p.Priority)
	emit("Homepage", p.Homepage)
	// Description LAST before the (WritePackages-appended) trailing fields —
	// matches dpkg-scanpackages output order. Preserve folding byte-for-byte.
	if p.Description != "" {
		buf.WriteString("Description: ")
		buf.WriteString(p.Description)
		if !strings.HasSuffix(p.Description, "\n") {
			buf.WriteByte('\n')
		}
	}
	return buf.String()
}

// storedPkg is the subset of metadata.DEBPackage (+ parse-only fields) that
// reconstructControlParagraph consumes. Defined here so the regen code path
// can populate it from a DEBPackage row while tests can populate it directly
// without touching the metadata package.
type storedPkg struct {
	Package       string
	Version       string
	Architecture  string
	Maintainer    string
	InstalledSize int64
	Depends       string
	PreDepends    string
	Recommends    string
	Suggests      string
	Conflicts     string
	Provides      string
	Replaces      string
	Section       string
	Priority      string
	Homepage      string
	Description   string
}
