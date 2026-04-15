// Package rpm implements the RPM repository protocol (RPM-01..05, Phase 3
// Plan 04). The handler mounted at /<project>/rpm/<repo>/... accepts native
// .rpm uploads, serves repodata/ + repomd.xml + repomd.xml.asc, and exposes
// the repo's armored OpenPGP public key at /public-key.asc.
//
// This file: header parse via cavaliergopher/rpm into the in-package Parsed
// shape used by the upload writer tx and the regen primary.xml.gz builder.
package rpm

import (
	"fmt"
	"time"

	rpmpkg "github.com/cavaliergopher/rpm"
)

// Parsed is the in-package view of a .rpm header. Exposes the NEVRA + the
// metadata fields the rpm_packages row and the regen primary.xml.gz builder
// need; raw cavaliergopher/rpm types are not re-exported.
type Parsed struct {
	Name        string
	Version     string
	Release     string
	Arch        string
	Epoch       int
	Summary     string
	Description string
	License     string
	URL         string
	SourceRPM   string
	BuildTime   time.Time
	Size        uint64

	Requires  []Dep
	Provides  []Dep
	Conflicts []Dep
	Obsoletes []Dep
	Files     []File
	Changelog []string

	// Digest is the sha256 hex of the file contents (no "sha256:" prefix).
	// Set by the caller post-hash, not by Parse.
	Digest string
}

// Dep is one rpm dependency entry.
type Dep struct {
	Name    string
	Epoch   int
	Version string
	Release string
	Flags   int
}

// File is one entry from the rpm header's file index.
type File struct {
	Name   string
	Size   int64
	Mode   uint32
	Digest string
	Flags  int64
}

// Parse opens the .rpm at path via cavaliergopher/rpm and returns a Parsed
// snapshot. Returns a wrapped error on parse failure; callers surface as
// 400 invalid_package.
//
// SOURCERPM (tag 1044) is read explicitly via Header.GetTag because the
// package-level wrapper returns the source-rpm filename via SourceRPM().
func Parse(path string) (*Parsed, error) {
	p, err := rpmpkg.Open(path)
	if err != nil {
		return nil, fmt.Errorf("rpm: open: %w", err)
	}
	out := &Parsed{
		Name:        p.Name(),
		Epoch:       p.Epoch(),
		Version:     p.Version(),
		Release:     p.Release(),
		Arch:        p.Architecture(),
		Summary:     p.Summary(),
		Description: p.Description(),
		License:     p.License(),
		URL:         p.URL(),
		Size:        p.Size(),
		BuildTime:   p.BuildTime(),
		Changelog:   p.ChangeLog(),
	}
	if t := p.Header.GetTag(1044); t != nil { // RPMTAG_SOURCERPM
		out.SourceRPM = t.String()
	}
	if out.SourceRPM == "" {
		out.SourceRPM = p.SourceRPM()
	}

	for _, d := range p.Requires() {
		out.Requires = append(out.Requires, depFrom(d))
	}
	for _, d := range p.Provides() {
		out.Provides = append(out.Provides, depFrom(d))
	}
	for _, d := range p.Conflicts() {
		out.Conflicts = append(out.Conflicts, depFrom(d))
	}
	for _, d := range p.Obsoletes() {
		out.Obsoletes = append(out.Obsoletes, depFrom(d))
	}
	for _, f := range p.Files() {
		out.Files = append(out.Files, File{
			Name:   f.Name(),
			Size:   f.Size(),
			Mode:   uint32(f.Mode()),
			Digest: f.Digest(),
			Flags:  f.Flags(),
		})
	}
	return out, nil
}

func depFrom(d rpmpkg.Dependency) Dep {
	return Dep{
		Name:    d.Name(),
		Epoch:   d.Epoch(),
		Version: d.Version(),
		Release: d.Release(),
		Flags:   d.Flags(),
	}
}
