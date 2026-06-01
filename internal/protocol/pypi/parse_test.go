package pypi_test

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"

	"github.com/vladoportos/omnirepo/internal/protocol/pypi"
)

// makeWheel builds an in-memory wheel zip with a minimal METADATA entry.
// The returned bytes are written to disk and the path is returned.
func makeWheel(t *testing.T, dir string, name, version, requiresPython, summary string) string {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	distInfoDir := name + "-" + version + ".dist-info"
	w, err := zw.Create(distInfoDir + "/METADATA")
	if err != nil {
		t.Fatalf("zip create: %v", err)
	}
	body := "Metadata-Version: 2.1\n" +
		"Name: " + name + "\n" +
		"Version: " + version + "\n"
	if requiresPython != "" {
		body += "Requires-Python: " + requiresPython + "\n"
	}
	if summary != "" {
		body += "Summary: " + summary + "\n"
	}
	body += "Classifier: Programming Language :: Python :: 3\n"
	body += "Classifier: License :: OSI Approved :: MIT License\n"
	body += "\nFull description here.\n"
	if _, err := w.Write([]byte(body)); err != nil {
		t.Fatalf("zip write metadata: %v", err)
	}
	// Add an empty record so the wheel "looks" valid; loaders may not check.
	rw, err := zw.Create(distInfoDir + "/RECORD")
	if err != nil {
		t.Fatalf("zip create RECORD: %v", err)
	}
	_, _ = rw.Write([]byte(""))
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	fname := name + "-" + version + "-py3-none-any.whl"
	p := filepath.Join(dir, fname)
	if err := os.WriteFile(p, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write wheel: %v", err)
	}
	return p
}

// makeSdist builds an in-memory sdist (.tar.gz) with a top-level
// PKG-INFO entry; returns the on-disk path.
func makeSdist(t *testing.T, dir string, name, version, requiresPython, summary string) string {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	top := name + "-" + version
	body := "Metadata-Version: 2.1\n" +
		"Name: " + name + "\n" +
		"Version: " + version + "\n"
	if requiresPython != "" {
		body += "Requires-Python: " + requiresPython + "\n"
	}
	if summary != "" {
		body += "Summary: " + summary + "\n"
	}
	body += "\nFull description here.\n"
	pkgInfo := []byte(body)
	if err := tw.WriteHeader(&tar.Header{
		Name: top + "/PKG-INFO", Mode: 0o644, Size: int64(len(pkgInfo)),
	}); err != nil {
		t.Fatalf("tar header: %v", err)
	}
	if _, err := tw.Write(pkgInfo); err != nil {
		t.Fatalf("tar write: %v", err)
	}
	// A junk file alongside.
	junk := []byte("# README\n")
	if err := tw.WriteHeader(&tar.Header{
		Name: top + "/README.md", Mode: 0o644, Size: int64(len(junk)),
	}); err != nil {
		t.Fatalf("tar header readme: %v", err)
	}
	_, _ = tw.Write(junk)
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gz close: %v", err)
	}
	fname := top + ".tar.gz"
	p := filepath.Join(dir, fname)
	if err := os.WriteFile(p, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write sdist: %v", err)
	}
	return p
}

func TestParseWheel(t *testing.T) {
	dir := t.TempDir()
	p := makeWheel(t, dir, "Flask", "2.3.0", ">=3.8", "A microframework")
	f, err := pypi.ParseWheel(p)
	if err != nil {
		t.Fatalf("ParseWheel: %v", err)
	}
	if f.Kind != "wheel" {
		t.Fatalf("Kind=%q want wheel", f.Kind)
	}
	if f.ProjectNormalized != "flask" {
		t.Fatalf("ProjectNormalized=%q want flask", f.ProjectNormalized)
	}
	if f.Version != "2.3.0" {
		t.Fatalf("Version=%q", f.Version)
	}
	if f.RequiresPython != ">=3.8" {
		t.Fatalf("RequiresPython=%q", f.RequiresPython)
	}
	if f.Summary != "A microframework" {
		t.Fatalf("Summary=%q", f.Summary)
	}
	// Multi-valued field promoted to []string.
	cls, ok := f.CoreMetadata["Classifier"].([]string)
	if !ok || len(cls) != 2 {
		t.Fatalf("Classifier should be []string len 2, got %T %v", f.CoreMetadata["Classifier"], f.CoreMetadata["Classifier"])
	}
	if got := f.MarshalCoreMetadata(); got == "{}" || got == "" {
		t.Fatalf("MarshalCoreMetadata empty: %q", got)
	}
}

func TestParseWheel_MissingMetadata(t *testing.T) {
	dir := t.TempDir()
	// Build a zip without dist-info/METADATA.
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("foo/bar.txt")
	_, _ = w.Write([]byte("x"))
	_ = zw.Close()
	p := filepath.Join(dir, "weird-1.0-py3-none-any.whl")
	if err := os.WriteFile(p, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := pypi.ParseWheel(p); err == nil {
		t.Fatal("ParseWheel should reject wheel missing METADATA")
	}
}

func TestParseSdist(t *testing.T) {
	dir := t.TempDir()
	p := makeSdist(t, dir, "zope.interface", "5.5.2", ">=2.7", "Zope-style interfaces")
	f, err := pypi.ParseSdist(p)
	if err != nil {
		t.Fatalf("ParseSdist: %v", err)
	}
	if f.Kind != "sdist" {
		t.Fatalf("Kind=%q want sdist", f.Kind)
	}
	if f.ProjectNormalized != "zope-interface" {
		t.Fatalf("ProjectNormalized=%q", f.ProjectNormalized)
	}
	if f.Version != "5.5.2" {
		t.Fatalf("Version=%q", f.Version)
	}
	if f.RequiresPython != ">=2.7" {
		t.Fatalf("RequiresPython=%q", f.RequiresPython)
	}
}

func TestParseSdist_MissingPKGINFO(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	body := []byte("hello")
	_ = tw.WriteHeader(&tar.Header{Name: "foo-1.0/README", Mode: 0o644, Size: int64(len(body))})
	_, _ = tw.Write(body)
	_ = tw.Close()
	_ = gz.Close()
	p := filepath.Join(dir, "foo-1.0.tar.gz")
	_ = os.WriteFile(p, buf.Bytes(), 0o644)
	if _, err := pypi.ParseSdist(p); err == nil {
		t.Fatal("ParseSdist should reject sdist missing PKG-INFO")
	}
}
