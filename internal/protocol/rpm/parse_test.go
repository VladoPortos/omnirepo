package rpm_test

import (
	"testing"

	"github.com/vladoportos/omnirepo/internal/protocol/rpm"
)

func TestParseRPMRealFixture(t *testing.T) {
	p, err := rpm.Parse("testdata/sample.rpm")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if p.Name == "" {
		t.Errorf("empty Name")
	}
	if p.Version == "" {
		t.Errorf("empty Version")
	}
	if p.Release == "" {
		t.Errorf("empty Release")
	}
	if p.Arch == "" {
		t.Errorf("empty Arch")
	}
	if p.Summary == "" {
		t.Errorf("empty Summary")
	}
	// CentOS source-rpm tag should populate.
	if p.SourceRPM == "" {
		t.Errorf("empty SourceRPM")
	}
}

func TestParseRPMBadInput(t *testing.T) {
	_, err := rpm.Parse("testdata/nonexistent-file.rpm")
	if err == nil {
		t.Fatalf("expected error on missing file")
	}
}
