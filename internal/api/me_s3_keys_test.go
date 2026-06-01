package api_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/vladoportos/omnirepo/internal/metadata"
)

// TestMeS3Keys_HappyPath covers the full profile-page lifecycle:
// POST {project_id} → 201 with shown-once secret_access_key, GET returns
// the new key scoped to the actor, DELETE revokes it, GET-after is empty.
func TestMeS3Keys_HappyPath(t *testing.T) {
	f := setupS3KeyFixture(t)

	// Create via /me/s3-keys (frontend profile-page contract).
	resp, buf := f.s.doBytes(t, "POST", "/api/v1/me/s3-keys/",
		f.aliceCookie, map[string]any{"project_id": f.projID})
	if resp.StatusCode != 201 {
		t.Fatalf("create: code=%d body=%s", resp.StatusCode, buf)
	}
	var created map[string]any
	if err := json.Unmarshal(buf, &created); err != nil {
		t.Fatal(err)
	}
	// Contract: field is secret_access_key (not "secret" as the
	// project-scoped endpoint uses).
	secret, ok := created["secret_access_key"].(string)
	if !ok || secret == "" {
		t.Fatalf("missing secret_access_key: %s", buf)
	}
	akid, ok := created["access_key_id"].(string)
	if !ok || !strings.HasPrefix(akid, "AKIA") {
		t.Fatalf("bad access_key_id: %s", buf)
	}
	if int64(created["project_id"].(float64)) != f.projID {
		t.Fatalf("project_id mismatch: %s", buf)
	}

	// List via /me/s3-keys: returns {items:[...]} with no secret.
	resp, buf = f.s.doBytes(t, "GET", "/api/v1/me/s3-keys/",
		f.aliceCookie, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("list: code=%d", resp.StatusCode)
	}
	if strings.Contains(string(buf), secret) {
		t.Fatalf("list response leaked secret")
	}
	var list struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(buf, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("want 1 item, got %d", len(list.Items))
	}
	if list.Items[0]["access_key_id"] != akid {
		t.Fatalf("list akid mismatch: %v", list.Items[0])
	}
	if _, has := list.Items[0]["secret_access_key"]; has {
		t.Fatal("list leaked secret_access_key field")
	}

	// Delete via /me/s3-keys/{id}.
	id := int64(created["id"].(float64))
	resp, _ = f.s.doBytes(t, "DELETE", "/api/v1/me/s3-keys/"+itoa(id),
		f.aliceCookie, nil)
	if resp.StatusCode != 204 {
		t.Fatalf("delete: code=%d", resp.StatusCode)
	}

	// List after revoke is empty.
	resp, buf = f.s.doBytes(t, "GET", "/api/v1/me/s3-keys/",
		f.aliceCookie, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("list-after-delete: code=%d", resp.StatusCode)
	}
	if err := json.Unmarshal(buf, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 0 {
		t.Fatalf("want empty, got %d", len(list.Items))
	}
}

// TestMeS3Keys_NonMember_Returns403 ensures a user without project
// membership cannot create an S3 key for that project via /me/s3-keys,
// matching the project-scoped endpoint's authorization behavior.
func TestMeS3Keys_NonMember_Returns403(t *testing.T) {
	f := setupS3KeyFixture(t)
	resp, _ := f.s.doBytes(t, "POST", "/api/v1/me/s3-keys/",
		f.carolCookie, map[string]any{"project_id": f.projID})
	if resp.StatusCode != 403 {
		t.Fatalf("non-member: code=%d, want 403", resp.StatusCode)
	}
}

// TestMeS3Keys_Delete_OtherUsersKey_Returns404 ensures a user cannot
// revoke a key created by someone else through /me/s3-keys. The error
// must collapse to 404 so the id isn't probeable.
func TestMeS3Keys_Delete_OtherUsersKey_Returns404(t *testing.T) {
	f := setupS3KeyFixture(t)

	// Super-admin creates a key through the project-scoped endpoint.
	resp, buf := f.s.doBytes(t, "POST",
		"/api/v1/projects/"+f.projName+"/s3-access-keys/",
		f.superCookie, map[string]any{"label": "admin-key"})
	if resp.StatusCode != 201 {
		t.Fatalf("super create: code=%d body=%s", resp.StatusCode, buf)
	}
	var created map[string]any
	_ = json.Unmarshal(buf, &created)
	id := int64(created["id"].(float64))

	// Alice tries to delete super's key through /me/s3-keys.
	resp, _ = f.s.doBytes(t, "DELETE", "/api/v1/me/s3-keys/"+itoa(id),
		f.aliceCookie, nil)
	if resp.StatusCode != 404 {
		t.Fatalf("cross-user delete: code=%d, want 404", resp.StatusCode)
	}
	// Verify the key is still live.
	s3Repo := metadata.NewS3KeysRepo(f.s.db)
	row, err := s3Repo.FindByID(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if row.RevokedAt != nil {
		t.Fatalf("key revoked despite 404 from cross-user delete")
	}
}

// TestMeS3Keys_MissingProjectID_Returns422 guards the explicit
// validation for the mandatory project_id field.
func TestMeS3Keys_MissingProjectID_Returns422(t *testing.T) {
	f := setupS3KeyFixture(t)
	resp, _ := f.s.doBytes(t, "POST", "/api/v1/me/s3-keys/",
		f.aliceCookie, map[string]any{})
	if resp.StatusCode != 422 {
		t.Fatalf("missing project_id: code=%d, want 422", resp.StatusCode)
	}
}
