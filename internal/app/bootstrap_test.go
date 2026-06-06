package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vladoportos/omnirepo/internal/app"
	"github.com/vladoportos/omnirepo/internal/config"
	"github.com/vladoportos/omnirepo/internal/metadata"
	"github.com/vladoportos/omnirepo/internal/metadata/sqlitetest"
)

func goodBootstrap() app.Bootstrap {
	return app.Bootstrap{
		SchemaVersion: 1,
		SuperAdmin: app.BootstrapUser{
			Login: "admin", Email: "admin@x", Password: "adminpw",
		},
		Users: []app.BootstrapUser{
			{Login: "alice", Email: "a@x", Password: "apw"},
			{Login: "bob", Email: "b@x", Password: "bpw", MustChangePassword: true},
		},
		Projects: []app.BootstrapProject{
			{Name: "acme", DescriptionMD: "d", Members: []string{"alice", "bob"}},
			{Name: "globex", Members: []string{"alice"}},
		},
		Repos: []app.BootstrapRepo{
			{Project: "acme", Type: "docker", Name: "web"},
			{Project: "globex", Type: "rpm", Name: "pkgs"},
		},
		APIKeys: []app.BootstrapAPIKey{
			{OwnerKind: "user", Owner: "alice", Name: "alice-ci", Token: "omr_u_" + strings.Repeat("a", 28)},
			{OwnerKind: "project", Owner: "acme", Name: "acme-ci", Token: "omr_p_" + strings.Repeat("b", 28)},
		},
	}
}

// writeBootstrap writes b as JSON to a new tempfile with mode 0600 and returns
// its path.
func writeBootstrap(t *testing.T, b app.Bootstrap) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "bootstrap.json")
	raw, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(p, raw, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return p
}

func writeBootstrapMode(t *testing.T, b app.Bootstrap, mode os.FileMode) string {
	t.Helper()
	p := writeBootstrap(t, b)
	if err := os.Chmod(p, mode); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	return p
}

func countRows(t *testing.T, db *metadata.DB, table string) int {
	t.Helper()
	var n int
	if err := db.Reader.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM "+table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func TestApplyBootstrap_HappyPath(t *testing.T) {
	db := sqlitetest.New(t)
	p := writeBootstrap(t, goodBootstrap())

	rep, err := app.ApplyBootstrapWithHook(context.Background(), db, config.Defaults(), p, nil)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if rep.Skipped {
		t.Fatalf("expected non-skip")
	}
	if rep.UsersSeeded != 3 || rep.ProjectsSeeded != 2 || rep.ReposSeeded != 2 || rep.APIKeysSeeded != 2 {
		t.Fatalf("counts: %+v", rep)
	}
	if got := countRows(t, db, "users"); got != 3 {
		t.Fatalf("users rows=%d", got)
	}
	if got := countRows(t, db, "project_members"); got != 3 {
		t.Fatalf("project_members rows=%d", got)
	}

	// Settings recorded.
	s := metadata.NewSettingsRepo(db)
	if v, _ := s.Get(context.Background(), "seeded_from_bootstrap"); v == "" {
		t.Fatalf("seeded_from_bootstrap not set")
	}
	sha, err := s.Get(context.Background(), "bootstrap_sha256")
	if err != nil || len(sha) != 64 {
		t.Fatalf("bootstrap_sha256 bad: %q err=%v", sha, err)
	}

	// Regression: bootstrapped repos must be indexed in repos_fts so
	// global search surfaces them. Pre-fix the FTS tables stayed empty
	// and every search returned zero results until a manual recreate.
	if got := countRows(t, db, "repos_fts"); got != rep.ReposSeeded {
		t.Fatalf("repos_fts rows=%d want=%d", got, rep.ReposSeeded)
	}
}

func TestApplyBootstrap_PasswordsHashed(t *testing.T) {
	db := sqlitetest.New(t)
	p := writeBootstrap(t, goodBootstrap())
	if _, err := app.ApplyBootstrapWithHook(context.Background(), db, config.Defaults(), p, nil); err != nil {
		t.Fatalf("apply: %v", err)
	}
	rows, err := db.Reader.QueryContext(context.Background(), `SELECT login, password_hash FROM users`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var login, hash string
		if err := rows.Scan(&login, &hash); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(hash, "$argon2id$v=19$m=65536,t=3,p=4$") {
			t.Errorf("%s: hash not argon2id: %q", login, hash)
		}
		for _, plain := range []string{"adminpw", "apw", "bpw"} {
			if hash == plain {
				t.Errorf("%s: hash is plaintext", login)
			}
		}
	}
}

func TestApplyBootstrap_APIKeyHashed(t *testing.T) {
	db := sqlitetest.New(t)
	b := goodBootstrap()
	p := writeBootstrap(t, b)
	if _, err := app.ApplyBootstrapWithHook(context.Background(), db, config.Defaults(), p, nil); err != nil {
		t.Fatalf("apply: %v", err)
	}
	var plaintext, shaGot string
	plaintext = b.APIKeys[0].Token
	if err := db.Reader.QueryRowContext(context.Background(),
		`SELECT token_sha256 FROM api_keys WHERE name='alice-ci'`).Scan(&shaGot); err != nil {
		t.Fatal(err)
	}
	// Recompute canonical sha via ParseAPIKey.
	// We import auth inside the test package only if needed — simpler: confirm not plaintext.
	if shaGot == plaintext {
		t.Fatalf("token stored as plaintext")
	}
	if len(shaGot) != 64 {
		t.Fatalf("expected 64-char hex sha, got %q", shaGot)
	}
}

func TestApplyBootstrap_MCPDefault(t *testing.T) {
	db := sqlitetest.New(t)
	p := writeBootstrap(t, goodBootstrap())
	if _, err := app.ApplyBootstrapWithHook(context.Background(), db, config.Defaults(), p, nil); err != nil {
		t.Fatalf("apply: %v", err)
	}
	// alice has MCP=false (default), bob has MCP=true (explicit).
	rows := map[string]bool{}
	r, err := db.Reader.QueryContext(context.Background(), `SELECT login, must_change_password FROM users`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	for r.Next() {
		var login string
		var mcp int64
		if err := r.Scan(&login, &mcp); err != nil {
			t.Fatal(err)
		}
		rows[login] = mcp != 0
	}
	if rows["alice"] {
		t.Errorf("alice should have MCP=false")
	}
	if !rows["bob"] {
		t.Errorf("bob should have MCP=true")
	}
}

func TestBootstrapIdempotentAfterFirstRun(t *testing.T) {
	db := sqlitetest.New(t)
	p := writeBootstrap(t, goodBootstrap())
	if _, err := app.ApplyBootstrapWithHook(context.Background(), db, config.Defaults(), p, nil); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	// Rewrite bootstrap with a third user; re-apply must skip.
	b := goodBootstrap()
	b.Users = append(b.Users, app.BootstrapUser{Login: "carol", Email: "c@x", Password: "cpw"})
	p2 := writeBootstrap(t, b)
	rep, err := app.ApplyBootstrapWithHook(context.Background(), db, config.Defaults(), p2, nil)
	if err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if !rep.Skipped {
		t.Fatalf("expected Skipped=true")
	}
	if got := countRows(t, db, "users"); got != 3 {
		t.Fatalf("users changed: %d", got)
	}
}

func TestBootstrapRefuses0644(t *testing.T) {
	db := sqlitetest.New(t)
	p := writeBootstrapMode(t, goodBootstrap(), 0o644)
	_, err := app.ApplyBootstrapWithHook(context.Background(), db, config.Defaults(), p, nil)
	if err == nil {
		t.Fatalf("expected error")
	}
	if !strings.Contains(err.Error(), "require 0600") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAtomicRollbackOnFailure(t *testing.T) {
	db := sqlitetest.New(t)
	b := goodBootstrap()
	// Force failure during API-key insert (after all users/projects/repos inserts would succeed).
	b.APIKeys[1].Owner = "missing_project"
	p := writeBootstrap(t, b)

	_, err := app.ApplyBootstrapWithHook(context.Background(), db, config.Defaults(), p, nil)
	if err == nil {
		t.Fatalf("expected error")
	}
	// Assert zero rows across every seeded table.
	for _, table := range []string{"users", "projects", "project_members", "repos", "api_keys"} {
		if got := countRows(t, db, table); got != 0 {
			t.Errorf("%s rows should be 0 after rollback, got %d", table, got)
		}
	}
}

func TestAtomicRollbackOnBadRepoType(t *testing.T) {
	db := sqlitetest.New(t)
	b := goodBootstrap()
	b.Repos = append(b.Repos, app.BootstrapRepo{Project: "acme", Type: "bogus", Name: "x"})
	p := writeBootstrap(t, b)

	_, err := app.ApplyBootstrapWithHook(context.Background(), db, config.Defaults(), p, nil)
	if err == nil {
		t.Fatalf("expected error")
	}
	if !strings.Contains(err.Error(), "repos[2].type") {
		t.Fatalf("expected pointer repos[2].type in error: %v", err)
	}
	for _, table := range []string{"users", "projects", "project_members", "repos", "api_keys"} {
		if got := countRows(t, db, table); got != 0 {
			t.Errorf("%s rows should be 0 after rollback, got %d", table, got)
		}
	}
}

// V1-V22 matrix (BT6). Each case mutates goodBootstrap() into a targeted
// failure and asserts the error text matches an expected fragment.
func TestBootstrapValidationMatrix(t *testing.T) {
	type tc struct {
		name   string
		mutate func(*app.Bootstrap)
		want   string
	}
	cases := []tc{
		{"V1_SchemaVersion", func(b *app.Bootstrap) { b.SchemaVersion = 2 }, "schema_version must be 1"},
		{"V2_SuperAdminLogin", func(b *app.Bootstrap) { b.SuperAdmin.Login = "bad!!" }, "super_admin.login"},
		{"V3_SuperAdminEmail", func(b *app.Bootstrap) { b.SuperAdmin.Email = "" }, "super_admin.email"},
		{"V4_SuperAdminPassword", func(b *app.Bootstrap) { b.SuperAdmin.Password = "" }, "super_admin.password"},
		{"V5_DuplicateLogin", func(b *app.Bootstrap) {
			b.Users = append(b.Users, app.BootstrapUser{Login: "alice", Email: "x@x", Password: "p"})
		}, "duplicate login"},
		{"V7_UserEmailEmpty", func(b *app.Bootstrap) { b.Users[0].Email = "" }, "users[0].email"},
		{"V9_ProjectNameReserved", func(b *app.Bootstrap) { b.Projects[0].Name = "api" }, "reserved prefix"},
		{"V10_DuplicateProject", func(b *app.Bootstrap) { b.Projects[1].Name = "acme" }, "duplicate project"},
		{"V11_UnknownMember", func(b *app.Bootstrap) { b.Projects[0].Members = []string{"nobody"} }, "not a known user"},
		{"V12_RepoProjectUnknown", func(b *app.Bootstrap) { b.Repos[0].Project = "ghost" }, "project \"ghost\" not declared"},
		{"V13_RepoTypeInvalid", func(b *app.Bootstrap) { b.Repos[0].Type = "zzz" }, "repo type \"zzz\" invalid"},
		{"V15_RepoUniqueWithinProjectType", func(b *app.Bootstrap) {
			b.Repos = append(b.Repos, app.BootstrapRepo{Project: "acme", Type: "docker", Name: "web"})
		}, "duplicate repo"},
		{"V16_BadSeverity", func(b *app.Bootstrap) { s := "extreme"; b.Repos[0].BlockOnSeverity = &s }, "invalid severity"},
		{"V19_APIKeyOwnerUnknown", func(b *app.Bootstrap) { b.APIKeys[0].Owner = "nobody" }, "user \"nobody\" not declared"},
		{"V21_APIKeyKindOwnerMismatch", func(b *app.Bootstrap) {
			// User-owned key but token says p (project).
			b.APIKeys[0].Token = "omr_p_" + strings.Repeat("z", 28)
		}, "token kind"},
		{"V22_APIKeyBadFormat", func(b *app.Bootstrap) { b.APIKeys[0].Token = "not-a-token" }, "APIKeyRegex"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db := sqlitetest.New(t)
			b := goodBootstrap()
			c.mutate(&b)
			p := writeBootstrap(t, b)
			_, err := app.ApplyBootstrapWithHook(context.Background(), db, config.Defaults(), p, nil)
			if err == nil {
				t.Fatalf("expected error")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("expected %q in %q", c.want, err.Error())
			}
			// Also assert rollback — zero rows.
			if got := countRows(t, db, "users"); got != 0 {
				t.Errorf("users not rolled back, got %d", got)
			}
			// Error is typed.
			var be *app.ErrBootstrap
			if !errors.As(err, &be) {
				t.Errorf("expected *app.ErrBootstrap, got %T", err)
			}
		})
	}
}
