package deb_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"strings"
	"testing"

	"github.com/blakesmith/ar"
	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"

	"github.com/vladoportos/omnirepo/internal/protocol/deb"
)

// sampleControl is the inner ./control file body for the synthesized fixture.
const sampleControl = `Package: mypkg
Version: 1.0-1
Architecture: amd64
Maintainer: Test <test@example.com>
Installed-Size: 42
Depends: libc6
Section: misc
Priority: optional
Description: one-line summary
 First continuation line.
 .
 Second paragraph.
`

// buildControlTar packages "./control" with sampleControl as a tar archive.
func buildControlTar(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	body := []byte(sampleControl)
	if err := tw.WriteHeader(&tar.Header{
		Name: "./control",
		Mode: 0o644,
		Size: int64(len(body)),
	}); err != nil {
		t.Fatalf("tar header: %v", err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatalf("tar write: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	return buf.Bytes()
}

// gzCompress returns gzip(data).
func gzCompress(t *testing.T, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write(data); err != nil {
		t.Fatalf("gz write: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gz close: %v", err)
	}
	return buf.Bytes()
}

func xzCompress(t *testing.T, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	xw, err := xz.NewWriter(&buf)
	if err != nil {
		t.Fatalf("xz writer: %v", err)
	}
	if _, err := xw.Write(data); err != nil {
		t.Fatalf("xz write: %v", err)
	}
	if err := xw.Close(); err != nil {
		t.Fatalf("xz close: %v", err)
	}
	return buf.Bytes()
}

func zstdCompress(t *testing.T, data []byte) []byte {
	t.Helper()
	zw, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatalf("zstd writer: %v", err)
	}
	defer zw.Close()
	return zw.EncodeAll(data, nil)
}

// buildDeb assembles a minimal .deb: outer ar with three members
//
//	debian-binary   (4 bytes "2.0\n")
//	control.tar.<suffix>
//	data.tar.<suffix>  (empty placeholder)
func buildDeb(t *testing.T, controlName string, controlBlob []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	aw := ar.NewWriter(&buf)
	if err := aw.WriteGlobalHeader(); err != nil {
		t.Fatalf("ar global: %v", err)
	}
	writeMember := func(name string, body []byte) {
		hdr := &ar.Header{Name: name, Size: int64(len(body)), Mode: 0o644}
		if err := aw.WriteHeader(hdr); err != nil {
			t.Fatalf("ar hdr %s: %v", name, err)
		}
		if _, err := aw.Write(body); err != nil {
			t.Fatalf("ar write %s: %v", name, err)
		}
	}
	writeMember("debian-binary", []byte("2.0\n"))
	writeMember(controlName, controlBlob)
	// Empty data.tar to round out the archive; ParseDeb should not require it.
	writeMember("data.tar", []byte{})
	return buf.Bytes()
}

func TestParseDebGzip(t *testing.T) {
	ctl := buildControlTar(t)
	debBytes := buildDeb(t, "control.tar.gz", gzCompress(t, ctl))
	c, err := deb.ParseDeb(bytes.NewReader(debBytes))
	if err != nil {
		t.Fatalf("ParseDeb: %v", err)
	}
	if c.Package != "mypkg" || c.Version != "1.0-1" || c.Architecture != "amd64" {
		t.Errorf("got %+v", c)
	}
	if c.InstalledSize != 42 {
		t.Errorf("installed_size=%d", c.InstalledSize)
	}
}

func TestParseDebXz(t *testing.T) {
	ctl := buildControlTar(t)
	debBytes := buildDeb(t, "control.tar.xz", xzCompress(t, ctl))
	c, err := deb.ParseDeb(bytes.NewReader(debBytes))
	if err != nil {
		t.Fatalf("ParseDeb: %v", err)
	}
	if c.Package != "mypkg" {
		t.Errorf("package=%q", c.Package)
	}
}

func TestParseDebZstd(t *testing.T) {
	ctl := buildControlTar(t)
	debBytes := buildDeb(t, "control.tar.zst", zstdCompress(t, ctl))
	c, err := deb.ParseDeb(bytes.NewReader(debBytes))
	if err != nil {
		t.Fatalf("ParseDeb: %v", err)
	}
	if c.Package != "mypkg" {
		t.Errorf("package=%q", c.Package)
	}
}

func TestParseDebRejectsMissingControl(t *testing.T) {
	var buf bytes.Buffer
	aw := ar.NewWriter(&buf)
	if err := aw.WriteGlobalHeader(); err != nil {
		t.Fatalf("ar global: %v", err)
	}
	body := []byte("2.0\n")
	if err := aw.WriteHeader(&ar.Header{Name: "debian-binary", Size: int64(len(body))}); err != nil {
		t.Fatalf("hdr: %v", err)
	}
	if _, err := aw.Write(body); err != nil {
		t.Fatalf("w: %v", err)
	}
	// No control.tar.* member.
	_, err := deb.ParseDeb(bytes.NewReader(buf.Bytes()))
	if err == nil || !strings.Contains(err.Error(), "control.tar") {
		t.Fatalf("want 'control.tar.* not found', got %v", err)
	}
}

func TestParseControlParagraphFolding(t *testing.T) {
	body := []byte("Package: x\nVersion: 1\nArchitecture: amd64\n" +
		"Description: short summary\n" +
		" line 2\n" +
		" line 3\n")
	c, err := deb.ParseControlParagraph(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !strings.Contains(c.Description, "short summary") {
		t.Errorf("desc=%q", c.Description)
	}
	if !strings.Contains(c.Description, "\n line 2") {
		t.Errorf("folding lost: %q", c.Description)
	}
	if !strings.Contains(c.Raw, "line 3") {
		t.Errorf("raw missing line 3: %q", c.Raw)
	}
}

func TestParseDebRejectsUnknownCompression(t *testing.T) {
	// Synthesize an ar with a bogus control.tar.foo member.
	ctl := buildControlTar(t)
	debBytes := buildDeb(t, "control.tar.foo", ctl)
	_, err := deb.ParseDeb(bytes.NewReader(debBytes))
	if err == nil {
		t.Fatalf("expected unknown-compression error, got nil")
	}
	if !strings.Contains(err.Error(), "unknown compression") {
		t.Errorf("err=%v", err)
	}
}

// Ensure parse.go uses the reader interface it claims (no unused vars).
var _ io.Reader = (*bytes.Reader)(nil)
