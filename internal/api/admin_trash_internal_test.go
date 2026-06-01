package api

import (
	"testing"
	"time"
)

// TestFormatRetention guards the coarse-label helper that the admin Trash
// UI shows for the retention countdown. The sub-minute negative branch
// handles an entry that has just tipped into GC-eligible territory, which
// used to render "-0m" — reading as a no-op.
func TestFormatRetention(t *testing.T) {
	cases := []struct {
		name string
		d    time.Duration
		want string
	}{
		{"days + hours", 6*24*time.Hour + 23*time.Hour, "6d 23h"},
		{"hours + minutes", 2*time.Hour + 15*time.Minute, "2h 15m"},
		{"minutes only", 37 * time.Minute, "37m"},
		{"sub-minute positive", 30 * time.Second, "<1m"},
		{"large negative", -2*24*time.Hour - 5*time.Hour, "-2d 5h"},
		{"sub-minute negative", -20 * time.Second, "<1m past"},
		{"exactly zero", 0, "<1m"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := formatRetention(tc.d)
			if got != tc.want {
				t.Fatalf("formatRetention(%v) = %q, want %q", tc.d, got, tc.want)
			}
		})
	}
}
