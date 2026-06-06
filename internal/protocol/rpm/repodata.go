// Package rpm — repodata writers. Builds primary.xml.gz, filelists.xml.gz,
// other.xml.gz, and the repomd.xml index per the createrepo_c on-disk layout.
//
// Namespaces:
//
//	common: http://linux.duke.edu/metadata/common
//	rpm:    http://linux.duke.edu/metadata/rpm
//	filelists: http://linux.duke.edu/metadata/filelists
//	other:  http://linux.duke.edu/metadata/other
//	repomd: http://linux.duke.edu/metadata/repo
//
// All Write* functions return both the gzipped bytes (as written to disk) and
// the matching hash + size pairs the regen step needs to populate repomd.xml's
// <checksum>, <open-checksum>, <size> and <open-size> attributes.
package rpm

import (
	"bytes"
	"compress/gzip"
	"encoding/xml"
	"fmt"
	"sort"
	"time"

	omrcrypto "github.com/vladoportos/omnirepo/internal/crypto"
)

const (
	nsCommon    = "http://linux.duke.edu/metadata/common"
	nsRPM       = "http://linux.duke.edu/metadata/rpm"
	nsFilelists = "http://linux.duke.edu/metadata/filelists"
	nsOther     = "http://linux.duke.edu/metadata/other"
	nsRepomd    = "http://linux.duke.edu/metadata/repo"
)

// PrimaryRoot is the root element of primary.xml.
type PrimaryRoot struct {
	XMLName  xml.Name     `xml:"metadata"`
	Xmlns    string       `xml:"xmlns,attr"`
	XmlnsRpm string       `xml:"xmlns:rpm,attr"`
	Packages int          `xml:"packages,attr"`
	Pkgs     []PrimaryPkg `xml:"package"`
}

// PrimaryPkg is one <package> entry in primary.xml.
type PrimaryPkg struct {
	Type        string       `xml:"type,attr"`
	Name        string       `xml:"name"`
	Arch        string       `xml:"arch"`
	Version     PrimaryVer   `xml:"version"`
	Checksum    PrimaryCksum `xml:"checksum"`
	Summary     string       `xml:"summary"`
	Description string       `xml:"description"`
	Packager    string       `xml:"packager,omitempty"`
	URL         string       `xml:"url,omitempty"`
	Time        PrimaryTime  `xml:"time"`
	Size        PrimarySize  `xml:"size"`
	Location    PrimaryLoc   `xml:"location"`
	Format      PrimaryFmt   `xml:"format"`
}

type PrimaryVer struct {
	Epoch string `xml:"epoch,attr"`
	Ver   string `xml:"ver,attr"`
	Rel   string `xml:"rel,attr"`
}

type PrimaryCksum struct {
	Type  string `xml:"type,attr"`
	Pkgid string `xml:"pkgid,attr"`
	Value string `xml:",chardata"`
}

type PrimaryTime struct {
	File  int64 `xml:"file,attr"`
	Build int64 `xml:"build,attr"`
}

type PrimarySize struct {
	Package   int64 `xml:"package,attr"`
	Installed int64 `xml:"installed,attr"`
	Archive   int64 `xml:"archive,attr"`
}

type PrimaryLoc struct {
	Href string `xml:"href,attr"`
}

type PrimaryFmt struct {
	License   string         `xml:"rpm:license,omitempty"`
	Vendor    string         `xml:"rpm:vendor,omitempty"`
	SourceRPM string         `xml:"rpm:sourcerpm,omitempty"`
	BuildHost string         `xml:"rpm:buildhost,omitempty"`
	Provides  *PrimaryDepSet `xml:"rpm:provides,omitempty"`
	Requires  *PrimaryDepSet `xml:"rpm:requires,omitempty"`
	Conflicts *PrimaryDepSet `xml:"rpm:conflicts,omitempty"`
	Obsoletes *PrimaryDepSet `xml:"rpm:obsoletes,omitempty"`
}

type PrimaryDepSet struct {
	Entries []PrimaryDepEntry `xml:"rpm:entry"`
}

type PrimaryDepEntry struct {
	Name  string `xml:"name,attr"`
	Flags string `xml:"flags,attr,omitempty"`
	Epoch string `xml:"epoch,attr,omitempty"`
	Ver   string `xml:"ver,attr,omitempty"`
	Rel   string `xml:"rel,attr,omitempty"`
}

// FilelistsRoot is the root element of filelists.xml.
type FilelistsRoot struct {
	XMLName  xml.Name       `xml:"filelists"`
	Xmlns    string         `xml:"xmlns,attr"`
	Packages int            `xml:"packages,attr"`
	Pkgs     []FilelistsPkg `xml:"package"`
}

type FilelistsPkg struct {
	Pkgid   string         `xml:"pkgid,attr"`
	Name    string         `xml:"name,attr"`
	Arch    string         `xml:"arch,attr"`
	Version PrimaryVer     `xml:"version"`
	Files   []FilelistFile `xml:"file"`
}

type FilelistFile struct {
	Type string `xml:"type,attr,omitempty"`
	Path string `xml:",chardata"`
}

// OtherRoot is the root element of other.xml.
type OtherRoot struct {
	XMLName  xml.Name   `xml:"otherdata"`
	Xmlns    string     `xml:"xmlns,attr"`
	Packages int        `xml:"packages,attr"`
	Pkgs     []OtherPkg `xml:"package"`
}

type OtherPkg struct {
	Pkgid     string           `xml:"pkgid,attr"`
	Name      string           `xml:"name,attr"`
	Arch      string           `xml:"arch,attr"`
	Version   PrimaryVer       `xml:"version"`
	Changelog []OtherChangelog `xml:"changelog"`
}

type OtherChangelog struct {
	Author string `xml:"author,attr,omitempty"`
	Date   int64  `xml:"date,attr,omitempty"`
	Text   string `xml:",chardata"`
}

// RepomdRoot is the root element of repomd.xml.
type RepomdRoot struct {
	XMLName  xml.Name     `xml:"repomd"`
	Xmlns    string       `xml:"xmlns,attr"`
	XmlnsRpm string       `xml:"xmlns:rpm,attr"`
	Revision int64        `xml:"revision"`
	Data     []RepomdData `xml:"data"`
}

// RepomdData is one <data> entry inside repomd.xml.
type RepomdData struct {
	Type         string      `xml:"type,attr"`
	Checksum     RepomdCksum `xml:"checksum"`
	OpenChecksum RepomdCksum `xml:"open-checksum"`
	Location     RepomdLoc   `xml:"location"`
	Timestamp    int64       `xml:"timestamp"`
	Size         int64       `xml:"size"`
	OpenSize     int64       `xml:"open-size"`
}

type RepomdCksum struct {
	Type  string `xml:"type,attr"`
	Value string `xml:",chardata"`
}

type RepomdLoc struct {
	Href string `xml:"href,attr"`
}

// sortPackages orders packages by (Name, Arch, Epoch DESC, Version DESC,
// Release DESC) so that successive Write* runs over the same input produce
// byte-identical output (determinism).
func sortPackages(pkgs []*Parsed) []*Parsed {
	out := make([]*Parsed, len(pkgs))
	copy(out, pkgs)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		if out[i].Arch != out[j].Arch {
			return out[i].Arch < out[j].Arch
		}
		if out[i].Epoch != out[j].Epoch {
			return out[i].Epoch > out[j].Epoch
		}
		if out[i].Version != out[j].Version {
			return out[i].Version > out[j].Version
		}
		return out[i].Release > out[j].Release
	})
	return out
}

// epochString stringifies the integer epoch the way createrepo does (omits
// leading zeros via plain %d; default 0 if Epoch is zero).
func epochString(epoch int) string { return fmt.Sprintf("%d", epoch) }

// gzipDeterministic gzip-compresses src into a deterministic byte stream:
// no modtime, no filename, default compression. The same src bytes always
// yield the same gzipped bytes — required for content-hash naming.
func gzipDeterministic(src []byte) ([]byte, error) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	gz.ModTime = time.Time{}
	gz.Name = ""
	gz.Comment = ""
	if _, err := gz.Write(src); err != nil {
		_ = gz.Close()
		return nil, fmt.Errorf("rpm: gzip write: %w", err)
	}
	if err := gz.Close(); err != nil {
		return nil, fmt.Errorf("rpm: gzip close: %w", err)
	}
	return buf.Bytes(), nil
}

// WritePrimary builds primary.xml from pkgs and returns the gzipped bytes,
// the gzipped sha256 (as hex), the uncompressed sha256, the uncompressed
// size, and the gzipped size.
func WritePrimary(pkgs []*Parsed) (gz []byte, gzSum, openSum string, openSize, gzSize int64, err error) {
	sorted := sortPackages(pkgs)
	root := PrimaryRoot{
		Xmlns:    nsCommon,
		XmlnsRpm: nsRPM,
		Packages: len(sorted),
	}
	for _, p := range sorted {
		entry := PrimaryPkg{
			Type:        "rpm",
			Name:        p.Name,
			Arch:        p.Arch,
			Version:     PrimaryVer{Epoch: epochString(p.Epoch), Ver: p.Version, Rel: p.Release},
			Checksum:    PrimaryCksum{Type: "sha256", Pkgid: "YES", Value: p.Digest},
			Summary:     p.Summary,
			Description: p.Description,
			URL:         p.URL,
			Time:        PrimaryTime{File: p.BuildTime.Unix(), Build: p.BuildTime.Unix()},
			Size:        PrimarySize{Package: int64(p.Size), Installed: int64(p.Size), Archive: int64(p.Size)},
			Location:    PrimaryLoc{Href: "packages/" + p.canonicalFilename()},
			Format: PrimaryFmt{
				License:   p.License,
				SourceRPM: p.SourceRPM,
				Provides:  buildDepSet(p.Provides),
				Requires:  buildDepSet(p.Requires),
				Conflicts: buildDepSet(p.Conflicts),
				Obsoletes: buildDepSet(p.Obsoletes),
			},
		}
		root.Pkgs = append(root.Pkgs, entry)
	}
	open, err := xmlMarshalDocument(&root)
	if err != nil {
		return nil, "", "", 0, 0, err
	}
	gz, err = gzipDeterministic(open)
	if err != nil {
		return nil, "", "", 0, 0, err
	}
	return gz, omrcrypto.SHA256Hex(gz), omrcrypto.SHA256Hex(open), int64(len(open)), int64(len(gz)), nil
}

// WriteFilelists builds filelists.xml from pkgs.
func WriteFilelists(pkgs []*Parsed) (gz []byte, gzSum, openSum string, openSize, gzSize int64, err error) {
	sorted := sortPackages(pkgs)
	root := FilelistsRoot{
		Xmlns:    nsFilelists,
		Packages: len(sorted),
	}
	for _, p := range sorted {
		entry := FilelistsPkg{
			Pkgid:   p.Digest,
			Name:    p.Name,
			Arch:    p.Arch,
			Version: PrimaryVer{Epoch: epochString(p.Epoch), Ver: p.Version, Rel: p.Release},
		}
		for _, f := range p.Files {
			ftype := ""
			// Bit 0040000 (S_IFDIR) marks a directory.
			if f.Mode&0o040000 != 0 {
				ftype = "dir"
			}
			entry.Files = append(entry.Files, FilelistFile{Type: ftype, Path: f.Name})
		}
		root.Pkgs = append(root.Pkgs, entry)
	}
	open, err := xmlMarshalDocument(&root)
	if err != nil {
		return nil, "", "", 0, 0, err
	}
	gz, err = gzipDeterministic(open)
	if err != nil {
		return nil, "", "", 0, 0, err
	}
	return gz, omrcrypto.SHA256Hex(gz), omrcrypto.SHA256Hex(open), int64(len(open)), int64(len(gz)), nil
}

// WriteOther builds other.xml from pkgs (changelog only in v1).
func WriteOther(pkgs []*Parsed) (gz []byte, gzSum, openSum string, openSize, gzSize int64, err error) {
	sorted := sortPackages(pkgs)
	root := OtherRoot{
		Xmlns:    nsOther,
		Packages: len(sorted),
	}
	for _, p := range sorted {
		entry := OtherPkg{
			Pkgid:   p.Digest,
			Name:    p.Name,
			Arch:    p.Arch,
			Version: PrimaryVer{Epoch: epochString(p.Epoch), Ver: p.Version, Rel: p.Release},
		}
		for _, c := range p.Changelog {
			entry.Changelog = append(entry.Changelog, OtherChangelog{Text: c})
		}
		root.Pkgs = append(root.Pkgs, entry)
	}
	open, err := xmlMarshalDocument(&root)
	if err != nil {
		return nil, "", "", 0, 0, err
	}
	gz, err = gzipDeterministic(open)
	if err != nil {
		return nil, "", "", 0, 0, err
	}
	return gz, omrcrypto.SHA256Hex(gz), omrcrypto.SHA256Hex(open), int64(len(open)), int64(len(gz)), nil
}

// WriteRepomd builds repomd.xml referencing the three data blocks. Each
// argument carries the hashes/sizes returned by the matching Write* call
// plus the on-disk Location href the regen has chosen.
func WriteRepomd(primary, filelists, other *RepomdData) ([]byte, error) {
	primary.Type = "primary"
	filelists.Type = "filelists"
	other.Type = "other"
	root := RepomdRoot{
		Xmlns:    nsRepomd,
		XmlnsRpm: nsRPM,
		Revision: 0, // overwritten by caller via SetRevision (see regen)
		Data:     []RepomdData{*primary, *filelists, *other},
	}
	return xmlMarshalDocument(&root)
}

// xmlMarshalDocument emits a fully-formed XML 1.0 document with header.
func xmlMarshalDocument(v any) ([]byte, error) {
	body, err := xml.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("rpm: xml marshal: %w", err)
	}
	out := append([]byte(xml.Header), body...)
	return out, nil
}

// canonicalFilename returns "<name>-<version>-<release>.<arch>.rpm".
func (p *Parsed) canonicalFilename() string {
	return fmt.Sprintf("%s-%s-%s.%s.rpm", p.Name, p.Version, p.Release, p.Arch)
}

// buildDepSet copies our Dep slice into the encoding-friendly form. Returns
// nil (omitted in XML) when the slice is empty.
func buildDepSet(deps []Dep) *PrimaryDepSet {
	if len(deps) == 0 {
		return nil
	}
	out := &PrimaryDepSet{}
	for _, d := range deps {
		out.Entries = append(out.Entries, PrimaryDepEntry{
			Name:  d.Name,
			Epoch: epochOmit(d.Epoch),
			Ver:   d.Version,
			Rel:   d.Release,
		})
	}
	return out
}

// epochOmit returns "" when epoch is zero so the attribute is omitted in XML.
func epochOmit(epoch int) string {
	if epoch == 0 {
		return ""
	}
	return fmt.Sprintf("%d", epoch)
}
