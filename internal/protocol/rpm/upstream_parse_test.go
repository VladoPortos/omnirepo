package rpm_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"
	"github.com/vladoportos/omnirepo/internal/protocol/rpm"
)

func makeUpstreamFixture(t *testing.T) (repomdXML, primaryGZ []byte) {
	t.Helper()
	primary := rpm.PrimaryRoot{
		Xmlns:    "http://linux.duke.edu/metadata/common",
		XmlnsRpm: "http://linux.duke.edu/metadata/rpm",
		Packages: 2,
		Pkgs: []rpm.PrimaryPkg{
			{
				Type: "rpm",
				Name: "foo", Arch: "x86_64",
				Version:  rpm.PrimaryVer{Epoch: "0", Ver: "1.0", Rel: "1.el9"},
				Checksum: rpm.PrimaryCksum{Type: "sha256", Pkgid: "YES", Value: "deadbeef"},
				Summary:  "test foo",
				Time:     rpm.PrimaryTime{File: 1700000000, Build: 1700000000},
				Size:     rpm.PrimarySize{Package: 1234},
				Location: rpm.PrimaryLoc{Href: "Packages/foo-1.0-1.el9.x86_64.rpm"},
			},
			{
				Type: "rpm",
				Name: "bar", Arch: "noarch",
				Version:  rpm.PrimaryVer{Epoch: "0", Ver: "2.0", Rel: "1"},
				Checksum: rpm.PrimaryCksum{Type: "sha256", Pkgid: "YES", Value: "cafef00d"},
				Summary:  "test bar",
				Time:     rpm.PrimaryTime{File: 1700000000, Build: 1700000000},
				Size:     rpm.PrimarySize{Package: 5678},
				Location: rpm.PrimaryLoc{Href: "Packages/bar-2.0-1.noarch.rpm"},
			},
		},
	}
	primaryBytes, err := xml.Marshal(&primary)
	if err != nil {
		t.Fatalf("marshal primary: %v", err)
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	_, _ = gz.Write(primaryBytes)
	_ = gz.Close()
	primaryGZ = buf.Bytes()

	repomd := rpm.RepomdRoot{
		Xmlns:    "http://linux.duke.edu/metadata/repo",
		XmlnsRpm: "http://linux.duke.edu/metadata/rpm",
		Revision: 1,
		Data: []rpm.RepomdData{
			{
				Type:     "primary",
				Checksum: rpm.RepomdCksum{Type: "sha256", Value: "abc"},
				Location: rpm.RepomdLoc{Href: "repodata/primary.xml.gz"},
				Size:     int64(len(primaryGZ)),
			},
		},
	}
	repomdXML, err = xml.Marshal(&repomd)
	if err != nil {
		t.Fatalf("marshal repomd: %v", err)
	}
	return
}

func newRPMUpstream(t *testing.T, requireBasicAuth bool) *httptest.Server {
	t.Helper()
	repomdXML, primaryGZ := makeUpstreamFixture(t)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requireBasicAuth {
			u, p, ok := r.BasicAuth()
			if !ok || u != "alice" || p != "s3cret" {
				http.Error(w, "auth required", http.StatusUnauthorized)
				return
			}
		}
		switch r.URL.Path {
		case "/repodata/repomd.xml":
			w.Header().Set("Content-Type", "application/xml")
			_, _ = w.Write(repomdXML)
		case "/repodata/primary.xml.gz":
			w.Header().Set("Content-Type", "application/gzip")
			_, _ = w.Write(primaryGZ)
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestRPMParseUpstreamYieldsPackages(t *testing.T) {
	srv := newRPMUpstream(t, false)
	defer srv.Close()

	var got []rpm.UpstreamEntry
	count, err := rpm.ParseUpstream(context.Background(), srv.Client(), srv.URL,
		rpm.AuthCreds{}, rpm.SyncFilter{},
		func(e rpm.UpstreamEntry) error { got = append(got, e); return nil })
	if err != nil {
		t.Fatalf("ParseUpstream: %v", err)
	}
	if count != 2 || len(got) != 2 {
		t.Fatalf("expected 2 entries, got %d", count)
	}
	for _, e := range got {
		if !strings.HasPrefix(e.Digest, "sha256:") {
			t.Fatalf("missing digest: %+v", e)
		}
		if e.Path == "" || e.Filename == "" {
			t.Fatalf("missing path/filename: %+v", e)
		}
	}
}

func TestRPMParseUpstreamWithCreds(t *testing.T) {
	srv := newRPMUpstream(t, true)
	defer srv.Close()
	if _, err := rpm.ParseUpstream(context.Background(), srv.Client(), srv.URL,
		rpm.AuthCreds{}, rpm.SyncFilter{},
		func(rpm.UpstreamEntry) error { return nil }); err == nil {
		t.Fatal("expected 401")
	}
	if _, err := rpm.ParseUpstream(context.Background(), srv.Client(), srv.URL,
		rpm.AuthCreds{User: "alice", Password: "s3cret"}, rpm.SyncFilter{},
		func(rpm.UpstreamEntry) error { return nil }); err != nil {
		t.Fatalf("with creds: %v", err)
	}
}

func TestRPMParseUpstreamFilter(t *testing.T) {
	srv := newRPMUpstream(t, false)
	defer srv.Close()
	var got []string
	_, err := rpm.ParseUpstream(context.Background(), srv.Client(), srv.URL,
		rpm.AuthCreds{}, rpm.SyncFilter{Names: []string{"foo"}},
		func(e rpm.UpstreamEntry) error { got = append(got, e.Filename); return nil })
	if err != nil {
		t.Fatalf("ParseUpstream: %v", err)
	}
	if len(got) != 1 || !strings.Contains(got[0], "foo") {
		t.Fatalf("filter wrong: %v", got)
	}
}

// Every modern Fedora/EPEL/Rocky/Alma mirror ships primary as `.xml.xz`.
// Pin xz support so a regression to gzip-only breaks loudly. Mirrors
// `TestRPMParseUpstreamYieldsPackages` but the upstream advertises
// `.xml.xz` and serves an xz-compressed body.
func TestRPMParseUpstream_XZPrimary(t *testing.T) {
	primary := rpm.PrimaryRoot{
		Xmlns: "http://linux.duke.edu/metadata/common", XmlnsRpm: "http://linux.duke.edu/metadata/rpm",
		Packages: 1,
		Pkgs: []rpm.PrimaryPkg{{
			Type: "rpm", Name: "foo", Arch: "x86_64",
			Version:  rpm.PrimaryVer{Epoch: "0", Ver: "1.0", Rel: "1.el9"},
			Checksum: rpm.PrimaryCksum{Type: "sha256", Pkgid: "YES", Value: "deadbeef"},
			Time:     rpm.PrimaryTime{File: 1700000000, Build: 1700000000},
			Size:     rpm.PrimarySize{Package: 1234},
			Location: rpm.PrimaryLoc{Href: "Packages/foo-1.0-1.el9.x86_64.rpm"},
		}},
	}
	primaryBytes, err := xml.Marshal(&primary)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	xzw, err := xz.NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := xzw.Write(primaryBytes); err != nil {
		t.Fatal(err)
	}
	if err := xzw.Close(); err != nil {
		t.Fatal(err)
	}
	primaryXZ := buf.Bytes()

	repomd := rpm.RepomdRoot{
		Xmlns: "http://linux.duke.edu/metadata/repo", XmlnsRpm: "http://linux.duke.edu/metadata/rpm",
		Revision: 1,
		Data: []rpm.RepomdData{{
			Type:     "primary",
			Checksum: rpm.RepomdCksum{Type: "sha256", Value: "abc"},
			Location: rpm.RepomdLoc{Href: "repodata/abc-primary.xml.xz"},
			Size:     int64(len(primaryXZ)),
		}},
	}
	repomdXML, err := xml.Marshal(&repomd)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repodata/repomd.xml":
			_, _ = w.Write(repomdXML)
		case "/repodata/abc-primary.xml.xz":
			_, _ = w.Write(primaryXZ)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	var got []string
	count, err := rpm.ParseUpstream(context.Background(), srv.Client(), srv.URL,
		rpm.AuthCreds{}, rpm.SyncFilter{},
		func(e rpm.UpstreamEntry) error { got = append(got, e.Filename); return nil })
	if err != nil {
		t.Fatalf("ParseUpstream xz: %v", err)
	}
	if count != 1 || len(got) != 1 || !strings.Contains(got[0], "foo") {
		t.Fatalf("xz primary not parsed: count=%d got=%v", count, got)
	}
}

// Docker CE / Microsoft / a few corporate mirrors ship primary as
// `.xml.zst`. Same shape test as `_XZPrimary`, different codec. Pin so a
// regression to xz-only breaks loudly.
func TestRPMParseUpstream_ZSTPrimary(t *testing.T) {
	primary := rpm.PrimaryRoot{
		Xmlns: "http://linux.duke.edu/metadata/common", XmlnsRpm: "http://linux.duke.edu/metadata/rpm",
		Packages: 1,
		Pkgs: []rpm.PrimaryPkg{{
			Type: "rpm", Name: "foo", Arch: "x86_64",
			Version:  rpm.PrimaryVer{Epoch: "0", Ver: "1.0", Rel: "1.el9"},
			Checksum: rpm.PrimaryCksum{Type: "sha256", Pkgid: "YES", Value: "deadbeef"},
			Time:     rpm.PrimaryTime{File: 1700000000, Build: 1700000000},
			Size:     rpm.PrimarySize{Package: 1234},
			Location: rpm.PrimaryLoc{Href: "Packages/foo-1.0-1.el9.x86_64.rpm"},
		}},
	}
	primaryBytes, err := xml.Marshal(&primary)
	if err != nil {
		t.Fatal(err)
	}
	zw, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	primaryZST := zw.EncodeAll(primaryBytes, nil)
	_ = zw.Close()

	repomd := rpm.RepomdRoot{
		Xmlns: "http://linux.duke.edu/metadata/repo", XmlnsRpm: "http://linux.duke.edu/metadata/rpm",
		Revision: 1,
		Data: []rpm.RepomdData{{
			Type:     "primary",
			Checksum: rpm.RepomdCksum{Type: "sha256", Value: "abc"},
			Location: rpm.RepomdLoc{Href: "repodata/abc-primary.xml.zst"},
			Size:     int64(len(primaryZST)),
		}},
	}
	repomdXML, err := xml.Marshal(&repomd)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repodata/repomd.xml":
			_, _ = w.Write(repomdXML)
		case "/repodata/abc-primary.xml.zst":
			_, _ = w.Write(primaryZST)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	count, err := rpm.ParseUpstream(context.Background(), srv.Client(), srv.URL,
		rpm.AuthCreds{}, rpm.SyncFilter{}, func(rpm.UpstreamEntry) error { return nil })
	if err != nil {
		t.Fatalf("ParseUpstream zst: %v", err)
	}
	if count != 1 {
		t.Fatalf("zst primary not parsed: count=%d", count)
	}
}

// Uncompressed `.xml` should also work (rare but spec-legal).
func TestRPMParseUpstream_PlainXMLPrimary(t *testing.T) {
	primary := rpm.PrimaryRoot{
		Xmlns: "http://linux.duke.edu/metadata/common", XmlnsRpm: "http://linux.duke.edu/metadata/rpm",
		Packages: 1,
		Pkgs: []rpm.PrimaryPkg{{
			Type: "rpm", Name: "foo", Arch: "x86_64",
			Version:  rpm.PrimaryVer{Epoch: "0", Ver: "1.0", Rel: "1.el9"},
			Checksum: rpm.PrimaryCksum{Type: "sha256", Pkgid: "YES", Value: "deadbeef"},
			Time:     rpm.PrimaryTime{File: 1700000000, Build: 1700000000},
			Size:     rpm.PrimarySize{Package: 1234},
			Location: rpm.PrimaryLoc{Href: "Packages/foo-1.0-1.el9.x86_64.rpm"},
		}},
	}
	primaryBytes, err := xml.Marshal(&primary)
	if err != nil {
		t.Fatal(err)
	}
	repomd := rpm.RepomdRoot{
		Xmlns: "http://linux.duke.edu/metadata/repo", XmlnsRpm: "http://linux.duke.edu/metadata/rpm",
		Revision: 1,
		Data: []rpm.RepomdData{{
			Type:     "primary",
			Checksum: rpm.RepomdCksum{Type: "sha256", Value: "abc"},
			Location: rpm.RepomdLoc{Href: "repodata/primary.xml"},
			Size:     int64(len(primaryBytes)),
		}},
	}
	repomdXML, err := xml.Marshal(&repomd)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repodata/repomd.xml":
			_, _ = w.Write(repomdXML)
		case "/repodata/primary.xml":
			_, _ = w.Write(primaryBytes)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	count, err := rpm.ParseUpstream(context.Background(), srv.Client(), srv.URL,
		rpm.AuthCreds{}, rpm.SyncFilter{}, func(rpm.UpstreamEntry) error { return nil })
	if err != nil {
		t.Fatalf("ParseUpstream plain xml: %v", err)
	}
	if count != 1 {
		t.Fatalf("plain xml primary not parsed: count=%d", count)
	}
}

func TestRPMParseUpstreamContextCancel(t *testing.T) {
	hold := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-hold }))
	defer srv.Close()
	defer close(hold)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := rpm.ParseUpstream(ctx, srv.Client(), srv.URL,
		rpm.AuthCreds{}, rpm.SyncFilter{}, func(rpm.UpstreamEntry) error { return nil }); err == nil {
		t.Fatal("expected timeout")
	}
}
