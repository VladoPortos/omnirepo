package deb

import (
	"github.com/vladoportos/omnirepo/internal/metadata"
	"testing"
)

func TestExistingPoolPathLegacyFallback(t *testing.T) {
	row := &metadata.DEBPackage{Filename: "pool/main/a/acme/acme.deb"}
	if got := existingPoolPath(row); got != row.Filename {
		t.Fatalf("legacy path=%q", got)
	}
	row.StoragePoolPath = "pool/main/a/acme/canonical.deb"
	if got := existingPoolPath(row); got != row.StoragePoolPath {
		t.Fatalf("canonical path=%q", got)
	}
}
