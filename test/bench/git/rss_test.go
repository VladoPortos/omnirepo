//go:build bench

package gitbench

import (
	"testing"
)

func TestParseFieldKB(t *testing.T) {
	// Real /proc/<pid>/status snippet.
	sample := []byte(`Name:	omnirepo
VmPeak:	  524288 kB
VmSize:	  524288 kB
VmRSS:	  131072 kB
VmSwap:	       0 kB
`)
	rss := parseFieldKB(sample, "VmRSS:")
	if rss != 131072 {
		t.Fatalf("VmRSS: got %d, want 131072", rss)
	}
	peak := parseFieldKB(sample, "VmPeak:")
	if peak != 524288 {
		t.Fatalf("VmPeak: got %d, want 524288", peak)
	}
	missing := parseFieldKB(sample, "VmData:")
	if missing != 0 {
		t.Fatalf("VmData: got %d, want 0", missing)
	}
}
