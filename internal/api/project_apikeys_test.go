package api_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/vladoportos/omnirepo/internal/auth"
	"github.com/vladoportos/omnirepo/internal/metadata"
)

// setupProjectAPIKeyFixture wires the same alice/carol/super fixture as the
// S3 key tests but exposes the project-API-key endpoints.
func setupProjectAPIKeyFixture(t *testing.T) *s3KeyFixture {
	t.Helper()
	return setupS3KeyFixture(t)
}

func TestProjectAPIKeys_CreateListRevoke_HappyPath(t *testing.T) {
	f := setupProjectAPIKeyFixture(t)

	// Create — secret is shown ONCE here, never elsewhere.
	resp, buf := f.s.doBytes(t, "POST",
		"/api/v1/projects/"+f.projName+"/api-keys/",
		f.aliceCookie,
		map[string]any{"name": "ci-publisher"})
	if resp.StatusCode != 201 {
		t.Fatalf("create code=%d body=%s", resp.StatusCode, buf)
	}
	var created map[string]any
	if err := json.Unmarshal(buf, &created); err != nil {
		t.Fatal(err)
	}
	secret, ok := created["secret"].(string)
	if !ok || secret == "" {
		t.Fatalf("create response missing secret: %s", buf)
	}
	if !auth.APIKeyRegex.MatchString(secret) {
		t.Fatalf("secret does not match APIKeyRegex: %q", secret)
	}
	if !strings.HasPrefix(secret, "omr_p_") {
		t.Fatalf("project-key secret must start with omr_p_: %q", secret)
	}
	id := int64(created["id"].(float64))
	if id == 0 {
		t.Fatal("no id in response")
	}
	if created["name"] != "ci-publisher" {
		t.Fatalf("name mismatch: %s", buf)
	}

	// List — secret MUST NOT appear.
	resp, buf = f.s.doBytes(t, "GET",
		"/api/v1/projects/"+f.projName+"/api-keys/",
		f.aliceCookie, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("list code=%d", resp.StatusCode)
	}
	if strings.Contains(string(buf), secret) {
		t.Fatalf("list response leaked plaintext secret!")
	}
	var listed []map[string]any
	if err := json.Unmarshal(buf, &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 {
		t.Fatalf("list count=%d, want 1", len(listed))
	}
	if _, has := listed[0]["secret"]; has {
		t.Fatal("list item has secret field")
	}

	// Revoke
	resp, _ = f.s.doBytes(t, "DELETE",
		"/api/v1/projects/"+f.projName+"/api-keys/"+itoa(id),
		f.aliceCookie, nil)
	if resp.StatusCode != 204 {
		t.Fatalf("revoke code=%d", resp.StatusCode)
	}

	// List after revoke — should be empty.
	resp, buf = f.s.doBytes(t, "GET",
		"/api/v1/projects/"+f.projName+"/api-keys/",
		f.aliceCookie, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("list-after-revoke code=%d", resp.StatusCode)
	}
	if err := json.Unmarshal(buf, &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed) != 0 {
		t.Fatalf("list-after-revoke count=%d, want 0", len(listed))
	}
}

func TestProjectAPIKeys_NonMember_Returns403(t *testing.T) {
	f := setupProjectAPIKeyFixture(t)
	// carol is not a member of the project.
	resp, _ := f.s.doBytes(t, "POST",
		"/api/v1/projects/"+f.projName+"/api-keys/",
		f.carolCookie,
		map[string]any{"name": "rogue"})
	if resp.StatusCode != 403 {
		t.Fatalf("non-member POST code=%d, want 403", resp.StatusCode)
	}
}

func TestProjectAPIKeys_EmptyName_Returns422(t *testing.T) {
	f := setupProjectAPIKeyFixture(t)
	for _, name := range []string{"", "   "} {
		resp, _ := f.s.doBytes(t, "POST",
			"/api/v1/projects/"+f.projName+"/api-keys/",
			f.aliceCookie,
			map[string]any{"name": name})
		if resp.StatusCode != 422 {
			t.Fatalf("empty name=%q code=%d, want 422", name, resp.StatusCode)
		}
	}
}

func TestProjectAPIKeys_RevokeCrossProject_Returns404(t *testing.T) {
	// Create a key in the alice-fixture project, then try to revoke it via
	// a SECOND project's URL — must 404 (no cross-project leakage).
	f := setupProjectAPIKeyFixture(t)
	ctx := context.Background()

	// Mint a key under f.projName.
	resp, buf := f.s.doBytes(t, "POST",
		"/api/v1/projects/"+f.projName+"/api-keys/",
		f.aliceCookie,
		map[string]any{"name": "victim"})
	if resp.StatusCode != 201 {
		t.Fatalf("create code=%d body=%s", resp.StatusCode, buf)
	}
	var created map[string]any
	_ = json.Unmarshal(buf, &created)
	id := int64(created["id"].(float64))

	// Make alice a member of a second project.
	projects := metadata.NewProjectsRepo(f.s.db)
	members := metadata.NewMembersRepo(f.s.db)
	otherID, err := projects.Create(ctx, "other-proj", "")
	if err != nil {
		t.Fatal(err)
	}
	// Alice's user id is the only project-member in the f fixture.
	memberIDs, err := members.ListUserIDsInProject(ctx, f.projID)
	if err != nil {
		t.Fatal(err)
	}
	if len(memberIDs) == 0 {
		t.Fatal("alice missing from project membership")
	}
	if err := members.Add(ctx, otherID, memberIDs[0], "maintainer"); err != nil {
		t.Fatal(err)
	}

	// Revoke the f.projName key via the other-proj URL — must 404 (the key
	// owner project does NOT match the URL project).
	resp, _ = f.s.doBytes(t, "DELETE",
		"/api/v1/projects/other-proj/api-keys/"+itoa(id),
		f.aliceCookie, nil)
	if resp.StatusCode != 404 {
		t.Fatalf("cross-project revoke code=%d, want 404", resp.StatusCode)
	}
}
