package rpm_test

import (
	"bytes"
	"compress/gzip"
	"encoding/xml"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/dxc-internal/omnirepo/internal/protocol/rpm"
)

func sample(name, ver string) *rpm.Parsed {
	return &rpm.Parsed{
		Name:        name,
		Version:     ver,
		Release:     "1.el7",
		Arch:        "x86_64",
		Epoch:       0,
		Summary:     name + " summary",
		Description: name + " description",
		License:     "MIT",
		URL:         "https://example.com/" + name,
		SourceRPM:   name + "-" + ver + "-1.el7.src.rpm",
		BuildTime:   time.Unix(1700000000, 0),
		Size:        1234,
		Digest:      strings.Repeat("a", 64),
		Files: []rpm.File{
			{Name: "/usr/bin/" + name, Size: 100, Mode: 0o100755},
			{Name: "/etc/" + name, Size: 0, Mode: 0o040755}, // dir
		},
		Changelog: []string{"first release"},
	}
}

func TestWritePrimaryStableHash(t *testing.T) {
	pkgs := []*rpm.Parsed{sample("alpha", "1.0"), sample("beta", "2.0")}
	gz1, sum1, openSum1, openSize1, gzSize1, err := rpm.WritePrimary(pkgs)
	if err != nil {
		t.Fatalf("WritePrimary 1: %v", err)
	}
	gz2, sum2, openSum2, openSize2, gzSize2, err := rpm.WritePrimary(pkgs)
	if err != nil {
		t.Fatalf("WritePrimary 2: %v", err)
	}
	if !bytes.Equal(gz1, gz2) {
		t.Fatalf("non-deterministic gz: len %d vs %d", len(gz1), len(gz2))
	}
	if sum1 != sum2 {
		t.Errorf("gzSum differs: %s vs %s", sum1, sum2)
	}
	if openSum1 != openSum2 {
		t.Errorf("openSum differs")
	}
	if openSize1 != openSize2 || gzSize1 != gzSize2 {
		t.Errorf("size differs")
	}
}

func TestWritePrimaryNamespaceAttrs(t *testing.T) {
	gz, _, _, _, _, err := rpm.WritePrimary([]*rpm.Parsed{sample("alpha", "1.0")})
	if err != nil {
		t.Fatalf("WritePrimary: %v", err)
	}
	body := gunzip(t, gz)
	// String inspection (xmlns:rpm attr is not easily round-tripped via Go's
	// encoding/xml, so check raw bytes for the namespace declarations).
	if !bytes.Contains(body, []byte(`xmlns="http://linux.duke.edu/metadata/common"`)) {
		t.Errorf("missing common xmlns in:\n%s", body)
	}
	if !bytes.Contains(body, []byte(`xmlns:rpm="http://linux.duke.edu/metadata/rpm"`)) {
		t.Errorf("missing rpm xmlns")
	}
	// Sanity: at least one <package> element + <location href="packages/...">
	if !bytes.Contains(body, []byte("<package")) {
		t.Errorf("missing <package> elements")
	}
	if !bytes.Contains(body, []byte(`href="packages/`)) {
		t.Errorf("missing packages/ location href")
	}
}

func TestWriteFilelistsContents(t *testing.T) {
	gz, _, _, _, _, err := rpm.WriteFilelists([]*rpm.Parsed{sample("alpha", "1.0")})
	if err != nil {
		t.Fatalf("WriteFilelists: %v", err)
	}
	body := gunzip(t, gz)
	if !bytes.Contains(body, []byte("/usr/bin/alpha")) {
		t.Errorf("missing file entry: %s", body)
	}
	if !bytes.Contains(body, []byte(`type="dir"`)) {
		t.Errorf("missing dir type attr")
	}
}

func TestWriteOtherChangelog(t *testing.T) {
	gz, _, _, _, _, err := rpm.WriteOther([]*rpm.Parsed{sample("alpha", "1.0")})
	if err != nil {
		t.Fatalf("WriteOther: %v", err)
	}
	body := gunzip(t, gz)
	if !bytes.Contains(body, []byte("first release")) {
		t.Errorf("missing changelog entry")
	}
}

func TestWriteRepomdReferencesAllThree(t *testing.T) {
	primary := &rpm.RepomdData{
		Checksum:     rpm.RepomdCksum{Type: "sha256", Value: "aaa"},
		OpenChecksum: rpm.RepomdCksum{Type: "sha256", Value: "bbb"},
		Location:     rpm.RepomdLoc{Href: "repodata/primary-aaa.xml.gz"},
		Timestamp:    100, Size: 50, OpenSize: 200,
	}
	filelists := &rpm.RepomdData{
		Checksum:     rpm.RepomdCksum{Type: "sha256", Value: "ccc"},
		OpenChecksum: rpm.RepomdCksum{Type: "sha256", Value: "ddd"},
		Location:     rpm.RepomdLoc{Href: "repodata/filelists-ccc.xml.gz"},
		Timestamp:    100, Size: 30, OpenSize: 100,
	}
	other := &rpm.RepomdData{
		Checksum:     rpm.RepomdCksum{Type: "sha256", Value: "eee"},
		OpenChecksum: rpm.RepomdCksum{Type: "sha256", Value: "fff"},
		Location:     rpm.RepomdLoc{Href: "repodata/other-eee.xml.gz"},
		Timestamp:    100, Size: 20, OpenSize: 80,
	}
	body, err := rpm.WriteRepomd(primary, filelists, other)
	if err != nil {
		t.Fatalf("WriteRepomd: %v", err)
	}
	// Parse back to count <data> elements.
	var root rpm.RepomdRoot
	if err := xml.Unmarshal(body, &root); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(root.Data) != 3 {
		t.Fatalf("got %d data, want 3", len(root.Data))
	}
	types := map[string]bool{}
	for _, d := range root.Data {
		types[d.Type] = true
	}
	for _, want := range []string{"primary", "filelists", "other"} {
		if !types[want] {
			t.Errorf("missing data type %q in repomd: %v", want, types)
		}
	}
	// repomd namespace declared.
	if !bytes.Contains(body, []byte(`xmlns="http://linux.duke.edu/metadata/repo"`)) {
		t.Errorf("missing repomd xmlns")
	}
}

func gunzip(t *testing.T, gz []byte) []byte {
	t.Helper()
	r, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		t.Fatalf("gunzip: %v", err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	_ = r.Close()
	return out
}
