package app

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/vladoportos/omnirepo/internal/auth"
	"github.com/vladoportos/omnirepo/internal/config"
	"github.com/vladoportos/omnirepo/internal/metadata"
)

// ErrBootstrap is the sentinel error class for bootstrap failures. main.go
// type-checks this to convert bootstrap failures into exit code 2 (per
// RESEARCH §Pitfall Mitigations "Bootstrap atomicity").
type ErrBootstrap struct {
	// Pointer is the JSON pointer of the offending field (e.g. "repos[2].type")
	// when the failure is a validation error. Empty for non-validation errors.
	Pointer string
	// Err is the underlying cause.
	Err error
}

func (e *ErrBootstrap) Error() string {
	if e.Pointer != "" {
		return fmt.Sprintf("bootstrap: %s: %v", e.Pointer, e.Err)
	}
	return fmt.Sprintf("bootstrap: %v", e.Err)
}
func (e *ErrBootstrap) Unwrap() error { return e.Err }

func bootstrapErr(pointer, format string, args ...any) error {
	return &ErrBootstrap{Pointer: pointer, Err: fmt.Errorf(format, args...)}
}

// Bootstrap is the in-memory shape of bootstrap.json.
type Bootstrap struct {
	SchemaVersion int                `json:"schema_version"`
	SuperAdmin    BootstrapUser      `json:"super_admin"`
	Users         []BootstrapUser    `json:"users"`
	Projects      []BootstrapProject `json:"projects"`
	Repos         []BootstrapRepo    `json:"repos"`
	APIKeys       []BootstrapAPIKey  `json:"api_keys"`
}

// BootstrapUser is a single user entry in bootstrap.json. Password is
// plaintext on disk; ApplyBootstrap hashes it with argon2id before persistence
// (BOOT-02). MustChangePassword defaults false (BOOT-03).
type BootstrapUser struct {
	Login              string `json:"login"`
	Email              string `json:"email"`
	Password           string `json:"password"`
	MustChangePassword bool   `json:"must_change_password"`
}

// BootstrapProject is a project + membership entry.
type BootstrapProject struct {
	Name          string   `json:"name"`
	DescriptionMD string   `json:"description_md"`
	Members       []string `json:"members"`
}

// BootstrapRepo is a repo entry scoped to a project name.
type BootstrapRepo struct {
	Project         string  `json:"project"`
	Type            string  `json:"type"`
	Name            string  `json:"name"`
	DescriptionMD   string  `json:"description_md"`
	AutoScan        *bool   `json:"auto_scan"`
	BlockOnSeverity *string `json:"block_on_severity"`
	PublicRead      *bool   `json:"public_read"`
}

// BootstrapAPIKey is an api-key entry. Token is plaintext on disk; parsed
// into (prefix, sha256) during ingest and stored per KEY-04 shape.
type BootstrapAPIKey struct {
	OwnerKind string `json:"owner_kind"` // "user" | "project"
	Owner     string `json:"owner"`      // login (user-kind) or project name (project-kind)
	Name      string `json:"name"`
	Token     string `json:"token"`
}

// BootstrapReport summarizes a successful ingest.
type BootstrapReport struct {
	UsersSeeded    int
	ProjectsSeeded int
	MembersSeeded  int
	ReposSeeded    int
	APIKeysSeeded  int
	Skipped        bool
}

// Allowed repo types mirror the DDL CHECK constraint.
var validRepoTypes = map[string]struct{}{
	"rpm": {}, "deb": {}, "pypi": {}, "docker": {}, "helm": {}, "git": {}, "raw": {},
}

// Allowed block_on_severity values mirror the DDL CHECK constraint.
var validSeverities = map[string]struct{}{
	"none": {}, "low": {}, "medium": {}, "high": {}, "critical": {},
}

// ApplyBootstrap ingests cfg.Bootstrap.Path into db atomically.
//
// Semantics (BOOT-01..BOOT-05 + pitfall "Bootstrap atomicity"):
//
//  1. Idempotency: if any user row already exists, returns (Skipped=true, nil)
//     and performs NO writes.
//  2. File mode: refuses a file whose permission bits are not 0600 (V24).
//  3. Validation: all V1-V23 checks pass BEFORE any INSERT. Any failure returns
//     an error whose message names the offending JSON pointer.
//  4. Atomicity: all INSERTs run inside one metadata.WriteTx; any failure
//     rolls back every row.
//  5. Passwords and tokens never persist as plaintext.
//  6. settings.seeded_from_bootstrap and settings.bootstrap_sha256 are written
//     inside the same tx so the audit trail is immediate and atomic.
//
// RepoCreateHookFn is the signature for the composed repo-create hook.
// When non-nil, ApplyBootstrap calls it inside the same tx for each repo so
// that git bare repos get initialized, signing keys get generated, etc.
type RepoCreateHookFn func(ctx context.Context, tx *sql.Tx, repoID int64, repoType, projectName, repoName string) (map[string]any, error)

func ApplyBootstrap(ctx context.Context, db *metadata.DB, cfg config.Config, path string) (*BootstrapReport, error) {
	return ApplyBootstrapWithHook(ctx, db, cfg, path, nil)
}

func ApplyBootstrapWithHook(ctx context.Context, db *metadata.DB, cfg config.Config, path string, repoHook RepoCreateHookFn) (*BootstrapReport, error) {
	// 1. Idempotent fast-path.
	empty, err := usersTableEmpty(ctx, db)
	if err != nil {
		return nil, &ErrBootstrap{Err: fmt.Errorf("count users: %w", err)}
	}
	if !empty {
		return &BootstrapReport{Skipped: true}, nil
	}

	// 2. File-mode enforcement (V24, pitfall #14).
	info, err := os.Stat(path)
	if err != nil {
		return nil, &ErrBootstrap{Err: fmt.Errorf("stat %q: %w", path, err)}
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		return nil, bootstrapErr("<file>", "refuse to read %q with mode %o (require 0600)", path, mode)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, &ErrBootstrap{Err: fmt.Errorf("read %q: %w", path, err)}
	}
	var b Bootstrap
	if err := json.Unmarshal(raw, &b); err != nil {
		return nil, &ErrBootstrap{Err: fmt.Errorf("parse: %w", err)}
	}

	// 3. Validation pass (V1..V23). No DB writes yet.
	if err := validate(&b); err != nil {
		return nil, err
	}

	// 4. Atomic ingest.
	sum := sha256.Sum256(raw)
	sha := hex.EncodeToString(sum[:])
	nowRFC := time.Now().UTC().Format(time.RFC3339)

	var report BootstrapReport
	err = db.WriteTx(ctx, func(tx *sql.Tx) error {
		// Track login → id, project name → id for member/repo/api_key linking.
		userIDByLogin := map[string]int64{}
		projectIDByName := map[string]int64{}

		// 4a. Super-admin.
		saID, err := insertUser(ctx, tx, b.SuperAdmin, true)
		if err != nil {
			return err
		}
		userIDByLogin[b.SuperAdmin.Login] = saID
		report.UsersSeeded++

		// 4b. Regular users.
		for _, u := range b.Users {
			uid, err := insertUser(ctx, tx, u, false)
			if err != nil {
				return err
			}
			userIDByLogin[u.Login] = uid
			report.UsersSeeded++
		}

		// 4c. Projects.
		for _, p := range b.Projects {
			pid, err := insertProject(ctx, tx, p)
			if err != nil {
				return err
			}
			projectIDByName[p.Name] = pid
			report.ProjectsSeeded++

			for _, member := range p.Members {
				uid, ok := userIDByLogin[member]
				if !ok {
					return bootstrapErr(
						fmt.Sprintf("projects[%q].members", p.Name),
						"member %q not found", member,
					)
				}
				if _, err := tx.ExecContext(ctx,
					`INSERT INTO project_members(project_id, user_id) VALUES (?, ?)`,
					pid, uid,
				); err != nil {
					return bootstrapErr(
						fmt.Sprintf("projects[%q].members", p.Name),
						"insert member %q: %w", member, err,
					)
				}
				report.MembersSeeded++
			}
		}

		// 4d. Repos.
		for i, rr := range b.Repos {
			pid, ok := projectIDByName[rr.Project]
			if !ok {
				return bootstrapErr(fmt.Sprintf("repos[%d].project", i), "project %q not found", rr.Project)
			}
			repoID, err := insertRepoReturningID(ctx, tx, pid, rr)
			if err != nil {
				return err
			}
			// Index the repo in FTS so search surfaces it immediately — the
			// bootstrap path previously skipped this because it inserted rows
			// directly (not via the repo create handler that wires IndexRepo).
			if err := metadata.IndexRepo(ctx, tx, repoID, rr.Name, rr.Project, rr.DescriptionMD, rr.Type); err != nil {
				return bootstrapErr(fmt.Sprintf("repos[%d].fts_index", i), "%w", err)
			}
			if repoHook != nil {
				if _, err := repoHook(ctx, tx, repoID, rr.Type, rr.Project, rr.Name); err != nil {
					return bootstrapErr(fmt.Sprintf("repos[%d].hook", i), "%w", err)
				}
			}
			report.ReposSeeded++
		}

		// 4e. API keys — BOOT-02: plaintext tokens hashed to (prefix, sha256)
		// before persistence.
		for i, k := range b.APIKeys {
			kind, prefix, sha, perr := auth.ParseAPIKey(k.Token)
			if perr != nil {
				return bootstrapErr(fmt.Sprintf("api_keys[%d].token", i), "%w", perr)
			}
			var ownerUserID, ownerProjectID sql.NullInt64
			switch k.OwnerKind {
			case "user":
				if string(kind) != "u" {
					return bootstrapErr(fmt.Sprintf("api_keys[%d]", i), "owner_kind=user but token kind=%q", kind)
				}
				uid, ok := userIDByLogin[k.Owner]
				if !ok {
					return bootstrapErr(fmt.Sprintf("api_keys[%d].owner", i), "user %q not found", k.Owner)
				}
				ownerUserID = sql.NullInt64{Int64: uid, Valid: true}
			case "project":
				if string(kind) != "p" {
					return bootstrapErr(fmt.Sprintf("api_keys[%d]", i), "owner_kind=project but token kind=%q", kind)
				}
				pid, ok := projectIDByName[k.Owner]
				if !ok {
					return bootstrapErr(fmt.Sprintf("api_keys[%d].owner", i), "project %q not found", k.Owner)
				}
				ownerProjectID = sql.NullInt64{Int64: pid, Valid: true}
			default:
				return bootstrapErr(fmt.Sprintf("api_keys[%d].owner_kind", i), "must be user|project, got %q", k.OwnerKind)
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO api_keys(owner_kind, owner_user_id, owner_project_id, name, token_prefix, token_sha256)
				VALUES (?, ?, ?, ?, ?, ?)
			`, k.OwnerKind, ownerUserID, ownerProjectID, k.Name, prefix, sha); err != nil {
				return bootstrapErr(fmt.Sprintf("api_keys[%d]", i), "insert: %w", err)
			}
			report.APIKeysSeeded++
		}

		// 4f. Settings — OQ-6 hash + seeded_from_bootstrap.
		if err := setSettingTx(ctx, tx, "seeded_from_bootstrap", nowRFC); err != nil {
			return err
		}
		if err := setSettingTx(ctx, tx, "bootstrap_sha256", sha); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		// Ensure we always return an *ErrBootstrap so main.go's exit-2 branch fires.
		var be *ErrBootstrap
		if !errors.As(err, &be) {
			return nil, &ErrBootstrap{Err: err}
		}
		return nil, err
	}
	return &report, nil
}

// usersTableEmpty returns true if the users table has zero rows (regardless of
// soft-delete state).
func usersTableEmpty(ctx context.Context, db *metadata.DB) (bool, error) {
	var n int
	err := db.Reader.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	if err != nil {
		return false, err
	}
	return n == 0, nil
}

// insertUser hashes the plaintext password (BOOT-02) and inserts one user row.
func insertUser(ctx context.Context, tx *sql.Tx, u BootstrapUser, isSuperAdmin bool) (int64, error) {
	hash, err := auth.HashPassword(u.Password)
	if err != nil {
		return 0, bootstrapErr(fmt.Sprintf("user[%q]", u.Login), "hash password: %w", err)
	}
	res, err := tx.ExecContext(ctx, `
		INSERT INTO users(login, email, password_hash, is_super_admin, must_change_password)
		VALUES (?, ?, ?, ?, ?)
	`, u.Login, u.Email, hash, boolToInt(isSuperAdmin), boolToInt(u.MustChangePassword))
	if err != nil {
		return 0, bootstrapErr(fmt.Sprintf("user[%q]", u.Login), "insert: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, bootstrapErr(fmt.Sprintf("user[%q]", u.Login), "last insert id: %w", err)
	}
	return id, nil
}

func insertProject(ctx context.Context, tx *sql.Tx, p BootstrapProject) (int64, error) {
	res, err := tx.ExecContext(ctx,
		`INSERT INTO projects(name, description_md) VALUES (?, ?)`,
		p.Name, p.DescriptionMD)
	if err != nil {
		return 0, bootstrapErr(fmt.Sprintf("projects[%q]", p.Name), "insert: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, bootstrapErr(fmt.Sprintf("projects[%q]", p.Name), "last insert id: %w", err)
	}
	return id, nil
}

func insertRepoReturningID(ctx context.Context, tx *sql.Tx, projectID int64, r BootstrapRepo) (int64, error) {
	autoScan := int64(1)
	if r.AutoScan != nil {
		autoScan = boolToInt(*r.AutoScan)
	}
	bos := "none"
	if r.BlockOnSeverity != nil && *r.BlockOnSeverity != "" {
		bos = *r.BlockOnSeverity
	}
	pr := int64(0)
	if r.PublicRead != nil {
		pr = boolToInt(*r.PublicRead)
	}
	res, err := tx.ExecContext(ctx, `
		INSERT INTO repos(project_id, type, name, description_md, auto_scan, block_on_severity, public_read)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, projectID, r.Type, r.Name, r.DescriptionMD, autoScan, bos, pr)
	if err != nil {
		return 0, bootstrapErr(fmt.Sprintf("repos[project=%s,type=%s,name=%s]", r.Project, r.Type, r.Name), "insert: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, bootstrapErr(fmt.Sprintf("repos[project=%s,type=%s,name=%s]", r.Project, r.Type, r.Name), "last insert id: %w", err)
	}
	return id, nil
}

func setSettingTx(ctx context.Context, tx *sql.Tx, key, value string) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO settings(key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=CURRENT_TIMESTAMP
	`, key, value)
	if err != nil {
		return bootstrapErr(fmt.Sprintf("settings[%s]", key), "set: %w", err)
	}
	return nil
}

func boolToInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// validate runs V1..V23 on the parsed bootstrap. File mode (V24) is enforced
// earlier in ApplyBootstrap. Returns nil on success; otherwise an
// *ErrBootstrap whose Pointer names the offending field.
func validate(b *Bootstrap) error {
	// V1: schema_version must be 1.
	if b.SchemaVersion != 1 {
		return bootstrapErr("schema_version", "schema_version must be 1, got %d", b.SchemaVersion)
	}
	// V2-V4: super_admin basics.
	if err := auth.LoginValid(b.SuperAdmin.Login); err != nil {
		return bootstrapErr("super_admin.login", "%v", err)
	}
	if b.SuperAdmin.Email == "" {
		return bootstrapErr("super_admin.email", "must not be empty")
	}
	if b.SuperAdmin.Password == "" {
		return bootstrapErr("super_admin.password", "must not be empty")
	}

	// V5-V8: users.
	seenLogin := map[string]struct{}{b.SuperAdmin.Login: {}}
	for i, u := range b.Users {
		if err := auth.LoginValid(u.Login); err != nil {
			return bootstrapErr(fmt.Sprintf("users[%d].login", i), "%v", err)
		}
		if _, dup := seenLogin[u.Login]; dup {
			return bootstrapErr(fmt.Sprintf("users[%d].login", i), "duplicate login %q", u.Login)
		}
		seenLogin[u.Login] = struct{}{}
		if u.Email == "" {
			return bootstrapErr(fmt.Sprintf("users[%d].email", i), "must not be empty")
		}
		if u.Password == "" {
			return bootstrapErr(fmt.Sprintf("users[%d].password", i), "must not be empty")
		}
	}

	// V9-V11: projects.
	seenProject := map[string]struct{}{}
	for i, p := range b.Projects {
		if err := auth.ProjectNameValid(p.Name); err != nil {
			return bootstrapErr(fmt.Sprintf("projects[%d].name", i), "%v", err)
		}
		if _, dup := seenProject[p.Name]; dup {
			return bootstrapErr(fmt.Sprintf("projects[%d].name", i), "duplicate project %q", p.Name)
		}
		seenProject[p.Name] = struct{}{}
		for j, m := range p.Members {
			if _, ok := seenLogin[m]; !ok {
				return bootstrapErr(
					fmt.Sprintf("projects[%d].members[%d]", i, j),
					"member %q is not a known user login", m,
				)
			}
		}
	}

	// V12-V18: repos.
	type repoKey struct{ project, typ, name string }
	seenRepo := map[repoKey]struct{}{}
	for i, r := range b.Repos {
		if _, ok := seenProject[r.Project]; !ok {
			return bootstrapErr(fmt.Sprintf("repos[%d].project", i), "project %q not declared", r.Project)
		}
		if _, ok := validRepoTypes[r.Type]; !ok {
			return bootstrapErr(fmt.Sprintf("repos[%d].type", i), "repo type %q invalid (must be rpm|deb|pypi|docker|helm|git|raw)", r.Type)
		}
		if err := auth.ProjectNameValid(r.Name); err != nil {
			// Re-uses ProjectNameValid for defense-in-depth slug enforcement on repo names.
			return bootstrapErr(fmt.Sprintf("repos[%d].name", i), "%v", err)
		}
		key := repoKey{r.Project, r.Type, r.Name}
		if _, dup := seenRepo[key]; dup {
			return bootstrapErr(fmt.Sprintf("repos[%d]", i), "duplicate repo (project=%s, type=%s, name=%s)", r.Project, r.Type, r.Name)
		}
		seenRepo[key] = struct{}{}
		if r.BlockOnSeverity != nil && *r.BlockOnSeverity != "" {
			if _, ok := validSeverities[*r.BlockOnSeverity]; !ok {
				return bootstrapErr(fmt.Sprintf("repos[%d].block_on_severity", i), "invalid severity %q", *r.BlockOnSeverity)
			}
		}
	}

	// V19-V23: api_keys.
	for i, k := range b.APIKeys {
		switch k.OwnerKind {
		case "user":
			if _, ok := seenLogin[k.Owner]; !ok {
				return bootstrapErr(fmt.Sprintf("api_keys[%d].owner", i), "user %q not declared", k.Owner)
			}
		case "project":
			if _, ok := seenProject[k.Owner]; !ok {
				return bootstrapErr(fmt.Sprintf("api_keys[%d].owner", i), "project %q not declared", k.Owner)
			}
		default:
			return bootstrapErr(fmt.Sprintf("api_keys[%d].owner_kind", i), "must be user|project, got %q", k.OwnerKind)
		}
		if k.Name == "" {
			return bootstrapErr(fmt.Sprintf("api_keys[%d].name", i), "must not be empty")
		}
		if !auth.APIKeyRegex.MatchString(k.Token) {
			return bootstrapErr(fmt.Sprintf("api_keys[%d].token", i), "does not match APIKeyRegex")
		}
		// Kind prefix inside token must match owner_kind.
		m := auth.APIKeyRegex.FindStringSubmatch(k.Token)
		expectedPrefix := "u"
		if k.OwnerKind == "project" {
			expectedPrefix = "p"
		}
		if m[1] != expectedPrefix {
			return bootstrapErr(fmt.Sprintf("api_keys[%d]", i), "token kind %q does not match owner_kind %q", m[1], k.OwnerKind)
		}
	}

	return nil
}
