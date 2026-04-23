package pypi

import "testing"

// TestParseSdistFilename covers the PEP 440 / PEP 625 filename split,
// including the dashed pre-release form the original LastIndex("-") split
// mis-attributed to the version slot (F-07.5, post-v1.4).
//
// A valid sdist filename is {name}-{version}.{tar.gz|tgz|zip} where the
// version starts with a digit (PEP 440 release segment or epoch). Name
// segments may contain hyphens; version segments may too (`1.0.0-rc1`
// legacy form that canonicalizes to `1.0.0rc1`). The correct split is the
// FIRST `-` whose right-hand neighbour is a digit — not the last one.
func TestParseSdistFilename(t *testing.T) {
	cases := []struct {
		base        string
		wantName    string
		wantVersion string
		wantErr     bool
	}{
		// Canonical shapes.
		{"foo-1.0.0.tar.gz", "foo", "1.0.0", false},
		{"foo-1.0.0.tgz", "foo", "1.0.0", false},
		{"foo-1.0.0.zip", "foo", "1.0.0", false},

		// Dashed pre-release suffix — the F-07.5 regression case.
		// Before the fix, LastIndex("-") returned version="rc1" and
		// name="foo-1.0.0", polluting the Simple-index grouping.
		{"foo-1.0.0-rc1.tar.gz", "foo", "1.0.0-rc1", false},
		{"foo-1.0.0-a1.tar.gz", "foo", "1.0.0-a1", false},
		{"foo-1.0.0-b2.tar.gz", "foo", "1.0.0-b2", false},
		{"foo-1.0.0-dev3.tar.gz", "foo", "1.0.0-dev3", false},
		{"foo-1.0.0-post4.tar.gz", "foo", "1.0.0-post4", false},

		// Canonical PEP 440 pre-release (no dash) stays intact.
		{"foo-1.0.0a1.tar.gz", "foo", "1.0.0a1", false},
		{"foo-1.0.0.post1.tar.gz", "foo", "1.0.0.post1", false},

		// Hyphenated project names.
		{"foo-bar-1.2.3.tar.gz", "foo-bar", "1.2.3", false},
		{"zope-interface-5.5.2.tar.gz", "zope-interface", "5.5.2", false},

		// PEP 440 epoch segment still starts with a digit.
		{"foo-1!2.0.tar.gz", "foo", "1!2.0", false},

		// Degenerate inputs.
		{"foo.tar.gz", "", "", true},           // no dash → no split
		{"-1.0.tar.gz", "", "", true},          // empty name
		{"foo-.tar.gz", "", "", true},          // empty version
		{"foo-abc.tar.gz", "", "", true},       // version must start with digit
		{"foo-1.0.0.tar", "", "", true},        // unsupported ext
		{"foo-1.0.0", "", "", true},            // no ext
	}

	for _, c := range cases {
		gotName, gotVersion, err := parseSdistFilename(c.base)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseSdistFilename(%q) = (%q, %q, nil), want error", c.base, gotName, gotVersion)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseSdistFilename(%q) unexpected error: %v", c.base, err)
			continue
		}
		if gotName != c.wantName || gotVersion != c.wantVersion {
			t.Errorf("parseSdistFilename(%q) = (%q, %q); want (%q, %q)",
				c.base, gotName, gotVersion, c.wantName, c.wantVersion)
		}
	}
}

// TestParseWheelFilename exercises PEP 427 split — no change intended in
// the F-07.5 fix, but locks current behaviour so the sync_handler.go
// reroute doesn't regress wheel parsing silently.
func TestParseWheelFilename(t *testing.T) {
	cases := []struct {
		base        string
		wantName    string
		wantVersion string
		wantErr     bool
	}{
		{"foo-1.0.0-py3-none-any.whl", "foo", "1.0.0", false},
		{"Flask-2.3.0-py3-none-any.whl", "Flask", "2.3.0", false},
		// PEP 427 build tag slot is optional (5 or 6 segments). Both
		// shapes give the same name/version because they're positional.
		{"foo-1.0.0-1-py3-none-any.whl", "foo", "1.0.0", false},
		// Case-insensitive extension match (Codex Q5, post-v1.4) —
		// isInstallableExt accepts uppercase .WHL so the parser must too.
		{"REQUESTS-2.33.1-PY3-NONE-ANY.WHL", "REQUESTS", "2.33.1", false},
		{"foo-1.0.0-py3-none-any.Whl", "foo", "1.0.0", false},

		{"foo-1.0.0.whl", "", "", true},   // only 2 segments
		{"foo-1.0.0.tar.gz", "", "", true}, // wrong ext
		{"foo.whl", "", "", true},
	}
	for _, c := range cases {
		gotName, gotVersion, err := parseWheelFilename(c.base)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseWheelFilename(%q) = (%q, %q, nil), want error", c.base, gotName, gotVersion)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseWheelFilename(%q) unexpected error: %v", c.base, err)
			continue
		}
		if gotName != c.wantName || gotVersion != c.wantVersion {
			t.Errorf("parseWheelFilename(%q) = (%q, %q); want (%q, %q)",
				c.base, gotName, gotVersion, c.wantName, c.wantVersion)
		}
	}
}

// TestIsSafeMirrorFilename (F-07.6) — upstream-fed filenames reach
// PathStore.Put as the last path segment under
// {proj}/pypi/{repo}/packages/. encodeURIComponent on the web side and
// chi's URL parsing block XSS, but a hostile upstream could engineer
// disposition-header-influencing bytes, path traversal segments, or
// control characters. The allowlist rejects anything outside a narrow
// PEP 427 / PEP 625-compatible charset before bytes ever hit disk.
func TestIsSafeMirrorFilename(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		// Canonical shapes every mirror must accept.
		{"foo-1.0.0.tar.gz", true},
		{"Flask-2.3.0-py3-none-any.whl", true},
		{"foo_bar-1.0.0.tar.gz", true},
		{"foo.bar-1.0.0.tar.gz", true},
		{"requests-2.23.0-py2.7-none-any.whl", true},
		{"zope.interface-5.5.2.tar.gz", true},

		// Path-traversal / directory-separator attempts.
		{"../etc/passwd", false},
		{"foo/1.0.0.tar.gz", false},
		{"foo\\1.0.0.tar.gz", false},
		{"./foo-1.0.0.tar.gz", false},

		// Control / whitespace / null bytes.
		{"foo 1.0.0.tar.gz", false},
		{"foo\t1.0.0.tar.gz", false},
		{"foo\n1.0.0.tar.gz", false},
		{"foo\x001.0.0.tar.gz", false},
		{"foo\r\n1.0.0.tar.gz", false},

		// Header-injection / disposition-attack characters.
		{`foo";evil.tar.gz`, false},
		{"foo<script>-1.0.0.tar.gz", false},
		{"foo%00.tar.gz", false},

		// Leading dot (hidden files) is allowed at the OS level but
		// shouldn't be a valid PEP filename — reject for mirror safety.
		{".hidden-1.0.0.tar.gz", false},

		// Empty / degenerate.
		{"", false},
	}
	for _, c := range cases {
		if got := isSafeMirrorFilename(c.name); got != c.want {
			t.Errorf("isSafeMirrorFilename(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestIsSafeMirrorFilename_LengthCap — a hostile upstream could pump a
// multi-kilobyte filename; filesystem limits + proxy header limits get
// unfriendly well before that. Cap keeps us conservative.
func TestIsSafeMirrorFilename_LengthCap(t *testing.T) {
	// 256 chars is already past NAME_MAX on ext4 (255). Validator caps
	// below that so we fail early with a clear error rather than with a
	// late ENAMETOOLONG from the OS.
	long := make([]byte, 256)
	for i := range long {
		long[i] = 'a'
	}
	longName := string(long) + ".tar.gz"
	if isSafeMirrorFilename(longName) {
		t.Errorf("isSafeMirrorFilename(%d-char name) = true, want false", len(longName))
	}
}
