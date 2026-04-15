// Package pypi implements the PyPI protocol surface (PYPI-01..05, Phase 03
// Plan 03). PEP 503 Simple HTML index, PEP 691 JSON variant via content
// negotiation, canonical project-name normalization, twine legacy
// multipart upload at /legacy/, PEP 694 upload session state machine at
// /+upload/, wheel/sdist metadata parsing, and /simple/ regen via the
// Phase 3 coalescer.
package pypi

import (
	"regexp"
	"strings"
)

// normRe matches the PEP 503 normalization character class: runs of
// hyphen, underscore, and dot collapse into a single hyphen.
//
//nolint:gochecknoglobals // Canonical PEP 503 regex — single compile reused everywhere.
var normRe = regexp.MustCompile("[-_.]+")

// Normalize returns the PEP 503 canonical form of a project name (D-22):
//
//	Normalize(name) = strings.ToLower(regexp.MustCompile("[-_.]+").ReplaceAllString(name, "-"))
//
// Applied at every trust boundary: URL routing (`/simple/<norm>/`),
// pypi_files row key, pypi_fts index key, inbound uploads (stored under
// the normalized project even if the client supplied a differently-cased
// name). Idempotent: Normalize(Normalize(x)) == Normalize(x).
func Normalize(name string) string {
	return strings.ToLower(normRe.ReplaceAllString(name, "-"))
}
