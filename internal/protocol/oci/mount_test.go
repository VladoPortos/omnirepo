package oci

import "testing"

// TestParseFromRepoShapes exercises the `from=` query-param parser that
// accepts cross-repo mount references.
//
// Real-world: `docker push` sends the full push-target URL minus `/v2/`
// as the `from=` hint when it cross-mounts a blob it knows is already
// present on this registry. OmniRepo's OCI push target is 4-segment
// (project/type/repo/image), so the `from=` field is 4-segment too.
// Older clients also emit 2- or 3-segment forms; all three shapes must
// resolve to the same (project, type, repo) triple.
func TestParseFromRepoShapes(t *testing.T) {
	cases := []struct {
		raw                             string
		wantProject, wantType, wantRepo string
		wantOK                          bool
	}{
		{"proj/app", "proj", "docker", "app", true},
		{"proj/docker/app", "proj", "docker", "app", true},
		{"proj/docker/app/nginx", "proj", "docker", "app", true},
		{"proj/helm/charts/mychart", "proj", "helm", "charts", true},
		// Rejected shapes.
		{"", "", "", "", false},
		{"proj", "", "", "", false},
		{"/proj/app", "", "", "", false},
		{"proj/app/", "", "", "", false},
		{"proj//app", "", "", "", false},
		{"proj/docker/app/nginx/extra", "", "", "", false},
		{"proj/docker/app/", "", "", "", false},
	}
	for _, tc := range cases {
		proj, typ, repo, ok := parseFromRepo(tc.raw)
		if ok != tc.wantOK || proj != tc.wantProject || typ != tc.wantType || repo != tc.wantRepo {
			t.Errorf("parseFromRepo(%q) = (%q,%q,%q,%v); want (%q,%q,%q,%v)",
				tc.raw, proj, typ, repo, ok,
				tc.wantProject, tc.wantType, tc.wantRepo, tc.wantOK)
		}
	}
}
