package pypi

import (
	"strings"
	"testing"
)

// TestValidate locks the PEP 440 public-version-identifier acceptance
// surface. Positive rows exercise every shape called out in
// 03-CONTEXT.md §D-01/D-03 (canonical release, epoch, pre-release —
// canonical + dashed legacy form, implicit post, dev, local with dot and
// underscore separators). Negative rows pin the rejection boundary,
// including the F-07.5 Q1 motivating case `2do-1.0.0` that the old
// `parseSdistFilename` heuristic mis-attributed to the version slot.
//
// Error shape is locked to the canonical prefix
// `pypi: invalid PEP 440 version:` per D-02 — callers wrap with their
// own context (pypi_sync: ..., pypi: malformed sdist filename: ...).
func TestValidate(t *testing.T) {
	const wantErrPrefix = "pypi: invalid PEP 440 version:"

	cases := []struct {
		v       string
		wantErr bool
	}{
		// Positive: canonical releases.
		{"1.0", false},
		{"1.0.0", false},
		{"1.0.0.0", false}, // 4-segment release
		{"0.0.1", false},

		// Positive: epoch (N!).
		{"1!2.0", false},
		{"1!2.0.0", false}, // epoch + multi-segment release

		// Positive: pre-release (canonical concatenated form).
		{"1.0a1", false},
		{"1.0.0a1", false},
		{"1.0.0b2", false},
		{"1.0.0rc1", false},

		// Positive: dev / post (canonical dotted forms).
		{"1.0.0.dev1", false},
		{"1.0.0.post1", false},

		// Positive: hyphenated pre-release (legacy form, F-07.5 regression).
		{"1.0.0-rc1", false},

		// Positive: implicit post-release (legacy `-N` form).
		{"1.0-1", false},

		// Positive: dev-release dotted shorthand.
		{"1.0.dev3", false},

		// Positive: local version segment (`+` body).
		{"1.0.0+local", false},
		{"1.0.0+abc.def", false}, // dot-separated local body
		{"1.0.0+abc_def", false}, // underscore-separated local body

		// Negative: empty / whitespace framing.
		{"", true},
		{" 1.0", true}, // leading whitespace (anchoring)
		{"1.0 ", true}, // trailing whitespace (anchoring)

		// Negative: letter-prefixed / malformed shapes.
		{"abc", true},
		{"1.0.0+", true},    // trailing + with no local body
		{"2do-1.0.0", true}, // F-07.5 Q1 motivating case
		{"1.0.0!2.0", true}, // epoch in wrong position (post-release slot)
		{"v1.0", true},      // leading `v` — caller strips if desired
	}

	for _, c := range cases {
		err := Validate(c.v)
		if c.wantErr {
			if err == nil {
				t.Errorf("Validate(%q) = nil, want error", c.v)
				continue
			}
			if !strings.Contains(err.Error(), wantErrPrefix) {
				t.Errorf("Validate(%q) error = %v, want prefix %q",
					c.v, err, wantErrPrefix)
			}
			continue
		}
		if err != nil {
			t.Errorf("Validate(%q) = %v, want nil", c.v, err)
		}
	}
}
