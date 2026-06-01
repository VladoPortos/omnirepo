package rpm_test

// H-7 regression: the per-package file index is parsed at upload but must be
// PERSISTED (rpm_packages.files_json) and RESTORED at regen, or filelists.xml
// is emitted with zero <file> entries and dnf cannot resolve file-based
// dependencies (Requires: /bin/sh, /usr/bin/python3, ...).

import (
	"bytes"
	"compress/gzip"
	"io"
	"testing"

	rpm "github.com/vladoportos/omnirepo/internal/protocol/rpm"
)

func TestFilelists_PersistedFilesRoundTripIntoXML(t *testing.T) {
	files := []rpm.File{
		{Name: "/usr/bin/foo", Mode: 0o100755},
		{Name: "/etc/foo", Mode: 0o100644},
		{Name: "/etc", Mode: 0o040755}, // directory
	}

	// Persist (upload path) -> restore (regen path).
	restored := rpm.UnmarshalFiles(rpm.MarshalFiles(files))
	if len(restored) != len(files) || restored[0].Name != "/usr/bin/foo" {
		t.Fatalf("file index did not round-trip through JSON: %+v", restored)
	}

	// regen feeds the restored files to WriteFilelists.
	gz, _, _, _, _, err := rpm.WriteFilelists([]*rpm.Parsed{{
		Name: "foo", Version: "1.0", Release: "1", Arch: "x86_64",
		Digest: "deadbeef", Files: restored,
	}})
	if err != nil {
		t.Fatalf("WriteFilelists: %v", err)
	}

	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		t.Fatalf("gzip: %v", err)
	}
	xmlBytes, _ := io.ReadAll(zr)
	xml := string(xmlBytes)

	for _, want := range []string{"/usr/bin/foo", "/etc/foo"} {
		if !bytes.Contains(xmlBytes, []byte(want)) {
			t.Errorf("filelists.xml missing file %q; got:\n%s", want, xml)
		}
	}
	if !bytes.Contains(xmlBytes, []byte(`type="dir"`)) {
		t.Errorf("filelists.xml missing directory entry (type=\"dir\"); got:\n%s", xml)
	}

	// Sanity: an empty file index (the pre-H-7 state / packages with no files)
	// still produces valid filelists with zero entries, not an error.
	if _, _, _, _, _, err := rpm.WriteFilelists([]*rpm.Parsed{{
		Name: "bar", Version: "2", Release: "1", Arch: "noarch", Digest: "x",
		Files: rpm.UnmarshalFiles(""),
	}}); err != nil {
		t.Fatalf("WriteFilelists with empty files: %v", err)
	}
}
