package deb

// White-box tests in package deb (not deb_test) so they can reach
// reconstructControlParagraph + storedPkg directly.

import (
	"bytes"
	"compress/gzip"
	"io"
	"strings"
	"testing"
	"time"
)

func TestWritePackagesRoundTrip(t *testing.T) {
	entries := []PackagesEntry{{
		Control: "Package: mypkg\nVersion: 1.0-1\nArchitecture: amd64\n" +
			"Maintainer: Test <t@e.com>\nDescription: s\n line2\n",
		Filename: "pool/m/mypkg/mypkg_1.0-1_amd64.deb",
		Size:     1234,
		MD5:      "aaaa",
		SHA256:   "bbbb",
	}}
	out := WritePackages(entries)
	// Re-parse as a control paragraph — the trailing Filename/Size/etc show up
	// as ordinary fields when consumed by ParseControlParagraph.
	c, err := ParseControlParagraph(out)
	if err != nil {
		t.Fatalf("reparse: %v", err)
	}
	if c.Package != "mypkg" || c.Version != "1.0-1" {
		t.Errorf("round-trip lost fields: %+v", c)
	}
}

func TestWritePackagesTrailingNewlineMatch(t *testing.T) {
	// Anti-pattern guard: Packages.gz must gzip the SAME bytes as Packages.
	entries := []PackagesEntry{{
		Control: "Package: a\nVersion: 1\nArchitecture: amd64\nDescription: x\n",
		Filename: "pool/a/a/a_1_amd64.deb", Size: 1,
		MD5: "d", SHA256: "s",
	}}
	body := WritePackages(entries)
	var gzBuf bytes.Buffer
	gz := gzip.NewWriter(&gzBuf)
	if _, err := gz.Write(body); err != nil {
		t.Fatalf("gz write: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gz close: %v", err)
	}
	gr, err := gzip.NewReader(&gzBuf)
	if err != nil {
		t.Fatalf("gunzip: %v", err)
	}
	defer func() { _ = gr.Close() }()
	restored, _ := io.ReadAll(gr)
	if !bytes.Equal(restored, body) {
		t.Errorf("gzip/Packages mismatch: len(body)=%d len(gunzip)=%d", len(body), len(restored))
	}
}

func TestWriteReleaseMandatoryFields(t *testing.T) {
	rel := WriteRelease(ReleaseInfo{
		Origin: "OmniRepo", Label: "r", Suite: "stable", Codename: "stable",
		Description:   "desc",
		Architectures: []string{"amd64", "arm64"},
		Components:    []string{"main"},
		Date:          time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC),
		Files: []ReleaseFileEntry{
			{Path: "main/binary-amd64/Packages", Size: 123,
				MD5: "d41d8cd98f00b204e9800998ecf8427e",
				SHA256: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"},
		},
	})
	s := string(rel)
	for _, want := range []string{"Suite: stable", "Codename: stable", "Date:", "Architectures: amd64 arm64", "Components: main", "SHA256:", "MD5Sum:"} {
		if !strings.Contains(s, want) {
			t.Errorf("Release missing %q. body=\n%s", want, s)
		}
	}
}

func TestWritePackagesFieldOrder(t *testing.T) {
	p := storedPkg{
		Package: "mypkg", Version: "1.0-1", Architecture: "amd64",
		Maintainer: "Test <t@e.com>", InstalledSize: 99,
		Depends: "libc6", PreDepends: "dpkg (>= 1.15)",
		Recommends: "foo", Suggests: "bar",
		Conflicts: "old", Provides: "virtual", Replaces: "legacy",
		Section: "misc", Priority: "optional", Homepage: "https://x.y",
		Description: "s\n line2\n",
	}
	ctrl := reconstructControlParagraph(p)
	// Extract field order (lines that begin with a letter and contain a colon
	// before any space that precedes the colon).
	var keys []string
	for _, line := range strings.Split(strings.TrimRight(ctrl, "\n"), "\n") {
		if line == "" || line[0] == ' ' || line[0] == '\t' {
			continue
		}
		colon := strings.IndexByte(line, ':')
		if colon <= 0 {
			continue
		}
		keys = append(keys, line[:colon])
	}
	want := []string{
		"Package", "Version", "Architecture", "Maintainer", "Installed-Size",
		"Depends", "Pre-Depends", "Recommends", "Suggests",
		"Conflicts", "Provides", "Replaces",
		"Section", "Priority", "Homepage", "Description",
	}
	if len(keys) != len(want) {
		t.Fatalf("got %d keys %v, want %d %v", len(keys), keys, len(want), want)
	}
	for i, k := range keys {
		if k != want[i] {
			t.Errorf("pos %d: got %q want %q (all=%v)", i, k, want[i], keys)
		}
	}

	// Round-trip via ParseControlParagraph.
	rp, err := ParseControlParagraph([]byte(ctrl))
	if err != nil {
		t.Fatalf("reparse: %v", err)
	}
	if rp.Package != p.Package || rp.Version != p.Version ||
		rp.Architecture != p.Architecture || rp.Depends != p.Depends {
		t.Errorf("round-trip mismatch: got %+v", rp)
	}
}

func TestWritePackagesFieldOrderOmitsEmpty(t *testing.T) {
	p := storedPkg{
		Package: "mypkg", Version: "1.0-1", Architecture: "amd64",
		Maintainer: "Me <m@e.com>", Description: "desc\n",
	}
	ctrl := reconstructControlParagraph(p)
	if strings.Contains(ctrl, "Depends:") {
		t.Errorf("empty Depends should be omitted, got:\n%s", ctrl)
	}
	if strings.Contains(ctrl, "Installed-Size:") {
		t.Errorf("zero Installed-Size should be omitted, got:\n%s", ctrl)
	}
	if !strings.Contains(ctrl, "Package: mypkg") {
		t.Errorf("Package missing:\n%s", ctrl)
	}
}

func TestComputeMD5SHA256(t *testing.T) {
	md5h, shah, size := ComputeMD5SHA256([]byte("hello"))
	if md5h != "5d41402abc4b2a76b9719d911017c592" {
		t.Errorf("md5=%s", md5h)
	}
	if shah != "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824" {
		t.Errorf("sha256=%s", shah)
	}
	if size != 5 {
		t.Errorf("size=%d", size)
	}
}
