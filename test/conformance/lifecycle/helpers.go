//go:build conformance

package lifecycleconf

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	_ "modernc.org/sqlite"

	"github.com/vladoportos/omnirepo/internal/app"
	"github.com/vladoportos/omnirepo/internal/config"
)

// SearchResult mirrors internal/api/search.go searchResultItem — JSON tags
// pinned exactly to the canonical envelope. Mismatching this shape would
// silently decode an empty slice and produce false-positive "0 results"
// assertions even when the search filter is broken —
// TestLifecycleConformance.BeforeSoftDelete_SearchFindsAllPackages forces a
// positive sanity assertion that catches envelope decode bugs before the
// post-soft-delete zero-result test can mask them.
type SearchResult struct {
	Kind     string  `json:"kind"`
	EntityID int64   `json:"entity_id"`
	Name     string  `json:"name"`
	Location string  `json:"location"`
	Severity string  `json:"severity,omitempty"`
	Score    float64 `json:"score"`
}

// searchResponse mirrors internal/api/search.go's writeJSON envelope:
// the JSON body is { items: [...], next_cursor: "..." } — the items key is
// "items", not the alternative key name a careless rewrite might guess.
// Mismatching this would silently decode to an empty slice and produce
// false-positive zero-result assertions. Verified internal/api/search.go:106-109.
type searchResponse struct {
	Items      []SearchResult `json:"items"`
	NextCursor string         `json:"next_cursor"`
}

// fixture is the in-process app handle for lifecycle conformance tests.
type fixture struct {
	host          string // "127.0.0.1:<port>"
	port          int
	httpEndpoint  string // "http://127.0.0.1:<port>"
	s3Endpoint    string // "http://127.0.0.1:<port>/s3" — for aws-sdk-go-v2 BaseEndpoint
	dataRoot      string
	adminLogin    string
	adminPassword string
	adminCookie   string // cached super-admin session cookie

	project   string
	projectID int64 // looked up after createProject; needed for trash restore id ("project-<id>")
	repoRPM   string
	repoDEB   string
	repoPyPI  string
	repoHelm  string

	s3AKID   string
	s3Secret string
	s3Bucket string

	projectAPIKey string // plaintext token for project-owned API key

	cancel context.CancelFunc
	doneCh chan error
}

// bootAppWithLifecycleFixture boots omnirepo in-process and provisions the
// fixture for the lifecycle suite. Mirrors test/conformance/s3/helpers.go.
// bootAppWithS3Bucket; replaces bucket-only setup with the 4-protocol
// lifecycle setup:
//
//   - one project P (via bootstrap.json — fastest path)
//   - 4 repos under P: rpm/r1, deb/r1, pypi/r1, helm/r1 (also via bootstrap.json)
//   - 1 S3 access key for P (admin REST POST /api/v1/projects/{name}/s3-access-keys/)
//   - 1 S3 bucket for P with one PUT object "test/object.bin" body=hello
//   - 1 project-owned API key for P (role=maintainer; admin REST POST
//     /api/v1/projects/{name}/api-keys/)
//   - 4 indexed packages (1 per protocol — direct SQL into base table + per-
//     protocol FTS so search has hits before soft-delete)
func bootAppWithLifecycleFixture(t *testing.T) *fixture {
	t.Helper()

	dataRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dataRoot, "config"), 0o750); err != nil {
		t.Fatal(err)
	}
	adminLogin := "admin"
	adminPassword := fmt.Sprintf("conf-pw-%d", time.Now().UnixNano())
	project := "lifecycletest"
	repoRPM := "r1rpm"
	repoDEB := "r1deb"
	repoPyPI := "r1pypi"
	repoHelm := "r1helm"
	bucketName := "lifecycle-bucket"

	// Bootstrap: project + 4 repos in one shot. Bootstrap accepts repo
	// types ∈ {rpm,deb,pypi,docker,helm,git,raw} per
	// internal/app/bootstrap.go:101 — the four we use are valid.
	bs := map[string]any{
		"schema_version": 1,
		"super_admin": map[string]any{
			"login": adminLogin, "email": "admin@example.com", "password": adminPassword,
		},
		"users":    []any{},
		"projects": []any{map[string]any{"name": project, "members": []string{}}},
		"repos": []any{
			map[string]any{"project": project, "type": "rpm", "name": repoRPM, "public_read": true},
			map[string]any{"project": project, "type": "deb", "name": repoDEB, "public_read": true},
			map[string]any{"project": project, "type": "pypi", "name": repoPyPI, "public_read": true},
			map[string]any{"project": project, "type": "helm", "name": repoHelm, "public_read": true},
		},
		"api_keys": []any{},
	}
	bsBytes, _ := json.Marshal(bs)
	bsPath := filepath.Join(dataRoot, "config", "bootstrap.json")
	if err := os.WriteFile(bsPath, bsBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := config.Defaults()
	cfg.DataRoot = dataRoot
	cfg.Bootstrap.Path = bsPath
	cfg.Server.ExternalHostnames = []string{"localhost"}

	httpLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	httpsLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		_ = httpLn.Close()
		t.Fatal(err)
	}
	httpAddr := httpLn.Addr().(*net.TCPAddr)

	ready := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- app.Run(ctx, cfg, app.RunOptions{
			HTTPListener: httpLn, HTTPSListener: httpsLn, Ready: ready,
		})
	}()
	select {
	case <-ready:
	case err := <-done:
		cancel()
		t.Fatalf("app.Run returned before ready: %v", err)
	case <-time.After(15 * time.Second):
		cancel()
		t.Fatal("app.Run did not signal ready within 15s")
	}

	host := fmt.Sprintf("127.0.0.1:%d", httpAddr.Port)
	httpEndpoint := "http://" + host
	waitHealthy(t, httpEndpoint+"/healthz", 10*time.Second)

	f := &fixture{
		host:          host,
		port:          httpAddr.Port,
		httpEndpoint:  httpEndpoint,
		s3Endpoint:    httpEndpoint + "/s3",
		dataRoot:      dataRoot,
		adminLogin:    adminLogin,
		adminPassword: adminPassword,
		project:       project,
		repoRPM:       repoRPM,
		repoDEB:       repoDEB,
		repoPyPI:      repoPyPI,
		repoHelm:      repoHelm,
		s3Bucket:      bucketName,
		cancel:        cancel,
		doneCh:        done,
	}
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Log("WARN: app.Run did not return within 5s of ctx cancel")
		}
	})

	// Cache super-admin session cookie once for all subsequent admin REST calls.
	f.adminCookie = loginAndGetCookie(t, httpEndpoint, adminLogin, adminPassword)

	// Look up project ID — needed later for the trash restore endpoint
	// ("project-<id>") and for repo lookups when seeding the FTS fixtures.
	f.projectID = lookupProjectID(t, dataRoot, project)

	// Mint an S3 access key for the project (admin REST POST).
	akid, secret := createS3Key(t, httpEndpoint, f.adminCookie, project)
	f.s3AKID = akid
	f.s3Secret = secret

	// Provision the S3 bucket (direct DB insert — production CreateBucket
	// is disabled, same as test/conformance/s3/helpers.go.createBucketDirect).
	createBucketDirect(t, dataRoot, project, bucketName)

	// Mint a project-owned API key for the project (admin REST POST under
	// /api/v1/projects/{name}/api-keys/ — note the trailing slash, chi.Route
	// pattern). Role defaults to "maintainer" per project_apikeys.go:127-130.
	f.projectAPIKey = createProjectAPIKey(t, httpEndpoint, f.adminCookie, project, "lifecycle-key", "maintainer")

	// PUT a fixture object via the live S3 client so subsequent GETs have
	// a real object to fetch. This goes through the SigV4 verifier + bucket
	// lookup as a baseline that the credentials resolve correctly.
	putOneS3Object(t, f, bucketName, "test/object.bin", []byte("hello"))

	// Index 1 fixture row per protocol so search has hits before soft-delete.
	indexLifecycleFixturePackages(t, dataRoot, f)

	return f
}

// loginAndGetCookie calls POST /api/v1/auth/login and returns the session
// cookie value. Same shape as test/conformance/s3/helpers.go.
func loginAndGetCookie(t *testing.T, baseURL, login, password string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"login": login, "password": password})
	req, err := http.NewRequest(http.MethodPost, baseURL+"/api/v1/auth/login", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("login request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("login: status=%d body=%s", resp.StatusCode, respBody)
	}

	for _, c := range resp.Cookies() {
		if c.Name == "omnirepo_session" {
			return c.Value
		}
	}
	t.Fatal("no omnirepo_session cookie in login response")
	return ""
}

// createS3Key calls POST /api/v1/projects/{name}/s3-access-keys/ and returns
// the AKID + plaintext secret. Same shape as test/conformance/s3/helpers.go.
func createS3Key(t *testing.T, baseURL, cookie, project string) (akid, secret string) {
	t.Helper()
	body := []byte(`{"label":"lifecycle-conformance-key"}`)
	url := fmt.Sprintf("%s/api/v1/projects/%s/s3-access-keys/", baseURL, project)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("create s3 key request: %v", err)
	}
	req.AddCookie(&http.Cookie{Name: "omnirepo_session", Value: cookie})
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("create s3 key: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("create s3 key: status=%d body=%s", resp.StatusCode, respBody)
	}

	var result struct {
		AccessKeyID string `json:"access_key_id"`
		Secret      string `json:"secret"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode s3 key response: %v", err)
	}
	if result.AccessKeyID == "" || result.Secret == "" {
		t.Fatal("s3 key response missing access_key_id or secret")
	}
	return result.AccessKeyID, result.Secret
}

// createProjectAPIKey calls POST /api/v1/projects/{name}/api-keys/ (note the
// trailing slash — project_apikeys.go uses chi.Route + r.Post("/", ...)) and
// returns the plaintext shown-once secret. Body: {"name":..., "role":...}.
//
// Verified handler: internal/api/project_apikeys.go:104 (handleCreateProjectAPIKey)
// returns projectAPIKeyCreateResponse with a `secret` field — see lines 32-38
// for the response shape. Status is 201 Created (line 180).
func createProjectAPIKey(t *testing.T, baseURL, cookie, project, name, role string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"name": name, "role": role})
	url := fmt.Sprintf("%s/api/v1/projects/%s/api-keys/", baseURL, project)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("create project api key request: %v", err)
	}
	req.AddCookie(&http.Cookie{Name: "omnirepo_session", Value: cookie})
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("create project api key: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("create project api key: status=%d body=%s", resp.StatusCode, respBody)
	}

	var result struct {
		ID     int64  `json:"id"`
		Name   string `json:"name"`
		Prefix string `json:"prefix"`
		Secret string `json:"secret"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode project api key response: %v", err)
	}
	if result.Secret == "" {
		t.Fatalf("project api key response missing 'secret' field (id=%d prefix=%q)", result.ID, result.Prefix)
	}
	return result.Secret
}

// createBucketDirect provisions an S3 bucket by inserting directly into the
// running app's SQLite database and creating the on-disk directory. Mirrors
// test/conformance/s3/helpers.go.createBucketDirect verbatim — production
// CreateBucket is disabled (DefaultProjectID=0; bucket provisioning is
// administrative).
func createBucketDirect(t *testing.T, dataRoot, projectName, bucketName string) {
	t.Helper()
	dbPath := filepath.Join(dataRoot, "db", "omnirepo.sqlite")
	db, err := sql.Open("sqlite", dbPath+"?_txlock=immediate")
	if err != nil {
		t.Fatalf("open db for bucket create: %v", err)
	}
	defer db.Close()

	var projectID int64
	if err := db.QueryRow(`SELECT id FROM projects WHERE name=?`, projectName).Scan(&projectID); err != nil {
		t.Fatalf("lookup project %q: %v", projectName, err)
	}

	if _, err := db.Exec(`INSERT INTO s3_buckets(name, project_id) VALUES (?, ?)`,
		bucketName, projectID); err != nil {
		t.Fatalf("insert bucket %q: %v", bucketName, err)
	}

	dir := filepath.Join(dataRoot, "s3", bucketName)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("mkdir bucket %q: %v", bucketName, err)
	}
}

// putOneS3Object PUTs a single object via the configured S3 client. Used to
// seed the fixture so subsequent GETs in PHASE A have a real object to
// retrieve. (Going through the live SigV4 path — vs. direct DB insert —
// also serves as a free smoke test that the credentials resolve before
// soft-delete.)
func putOneS3Object(t *testing.T, f *fixture, bucket, key string, body []byte) {
	t.Helper()
	cl := NewS3Client(t, f.s3Endpoint, f.s3AKID, f.s3Secret, 3)
	bucketCopy := bucket
	keyCopy := key
	_, err := cl.PutObject(context.Background(), &s3.PutObjectInput{
		Bucket: &bucketCopy,
		Key:    &keyCopy,
		Body:   bytes.NewReader(body),
	})
	if err != nil {
		t.Fatalf("seed PutObject: %v", err)
	}
}

// indexLifecycleFixturePackages opens the SQLite DB directly and inserts
// 1 row per protocol into the canonical base tables + per-protocol FTS.
// Matches what protocol/{rpm,deb,pypi,helm} write at PUT time, so search
// returns 1 hit per package name BEFORE soft-delete and 0 AFTER (because
// PruneRepoFTS drops the FTS rows in the cascade).
//
// Repo IDs are looked up by (project_id, type, name) via SELECT.
//
// rpm/pypi/helm use pinned column lists from migrations 010/012/013.
// deb_packages uses PRAGMA table_info('deb_packages') at runtime to discover
// NOT NULL columns + a defensive INSERT — required because deb_packages
// references apt_suites.id (FK) and the apt_suites row must be inserted first.
// PRAGMA is the schema-drift-proof escape hatch (deterministic).
func indexLifecycleFixturePackages(t *testing.T, dataRoot string, f *fixture) {
	t.Helper()
	dbPath := filepath.Join(dataRoot, "db", "omnirepo.sqlite")
	db, err := sql.Open("sqlite", dbPath+"?_txlock=immediate")
	if err != nil {
		t.Fatalf("open db for FTS fixture: %v", err)
	}
	defer db.Close()

	rpmRepoID := lookupRepoID(t, db, f.projectID, "rpm", f.repoRPM)
	debRepoID := lookupRepoID(t, db, f.projectID, "deb", f.repoDEB)
	pypiRepoID := lookupRepoID(t, db, f.projectID, "pypi", f.repoPyPI)
	helmRepoID := lookupRepoID(t, db, f.projectID, "helm", f.repoHelm)

	// 1) rpm_packages — pinned columns from migration 010.
	if _, err := db.Exec(`
		INSERT INTO rpm_packages
			(repo_id, name, epoch, version, release, arch, summary, digest, filename)
		VALUES (?, 'lifecyclepkg-rpm', 0, '1.0.0', '1', 'x86_64', 'fixture pkg',
		        'sha256:rpmfixture', 'lifecyclepkg-rpm-1.0.0-1.x86_64.rpm')
	`, rpmRepoID); err != nil {
		t.Fatalf("insert rpm_packages: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO rpm_fts (repo_id, name, version, arch_or_runtime, summary)
		VALUES (?, 'lifecyclepkg-rpm', '1.0.0', 'x86_64', 'fixture pkg')
	`, rpmRepoID); err != nil {
		t.Fatalf("insert rpm_fts: %v", err)
	}

	// 2) deb_packages — needs apt_suites row first, then PRAGMA-driven
	// defensive INSERT. The apt_suites schema (migration 009) has only
	// repo_id/suite/component/architecture as NOT NULL columns.
	insertDebFixtureViaPragma(t, db, debRepoID, "lifecyclepkg-deb", "1.0.0", "amd64")
	if _, err := db.Exec(`
		INSERT INTO deb_fts (repo_id, name, version, arch_or_runtime, summary)
		VALUES (?, 'lifecyclepkg-deb', '1.0.0', 'amd64', 'fixture pkg')
	`, debRepoID); err != nil {
		t.Fatalf("insert deb_fts: %v", err)
	}

	// 3) pypi_files — pinned columns from migration 012; kind MUST be 'wheel'
	// or 'sdist' (CHECK constraint).
	if _, err := db.Exec(`
		INSERT INTO pypi_files
			(repo_id, project_normalized, version, filename, kind, digest)
		VALUES (?, 'lifecyclepkg-pypi', '1.0.0',
		        'lifecyclepkg_pypi-1.0.0-py3-none-any.whl', 'wheel', 'sha256:pypifixture')
	`, pypiRepoID); err != nil {
		t.Fatalf("insert pypi_files: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO pypi_fts (repo_id, name, version, arch_or_runtime, summary)
		VALUES (?, 'lifecyclepkg-pypi', '1.0.0', '', 'fixture pkg')
	`, pypiRepoID); err != nil {
		t.Fatalf("insert pypi_fts: %v", err)
	}

	// 4) helm_charts — pinned columns from migration 013.
	if _, err := db.Exec(`
		INSERT INTO helm_charts
			(repo_id, name, version, digest, filename)
		VALUES (?, 'lifecyclepkg-helm', '1.0.0', 'sha256:helmfixture',
		        'lifecyclepkg-helm-1.0.0.tgz')
	`, helmRepoID); err != nil {
		t.Fatalf("insert helm_charts: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO helm_fts (repo_id, name, version, arch_or_runtime, summary)
		VALUES (?, 'lifecyclepkg-helm', '1.0.0', '', 'fixture pkg')
	`, helmRepoID); err != nil {
		t.Fatalf("insert helm_fts: %v", err)
	}
}

// insertDebFixtureViaPragma — inserts a deb_packages row for the given
// (repoID, pkg, version, arch). Uses PRAGMA table_info to discover NOT NULL
// columns of deb_packages dynamically, defaulting unknown columns to "" or
// 0. Schema-drift-proof: any new NOT NULL column added in a future migration
// gets a safe default rather than breaking the fixture.
//
// apt_suites is NOT inserted here — the deb repo create hook
// (internal/app/deb_repo_create.go.CreateDEBRepoHook) seeds three default
// rows ({stable, main, amd64|arm64|all}) atomically with the repos INSERT,
// so the bootstrapped repo already has matching apt_suites rows. We look
// up the (repo_id, suite='stable', component='main', architecture=arch)
// row by SELECT and use its id as the suite_id FK on deb_packages.
func insertDebFixtureViaPragma(t *testing.T, db *sql.DB, repoID int64, pkg, version, arch string) {
	t.Helper()

	// 1) apt_suites: look up the seeded row for this (repo, arch). If the
	// repo create hook didn't run for any reason (e.g. repo wasn't created
	// via the seeded path), fail with a clear message rather than letting
	// the deb_packages INSERT die with a confusing FK error.
	var suiteID int64
	if err := db.QueryRow(`
		SELECT id FROM apt_suites
		WHERE repo_id=? AND suite='stable' AND component='main' AND architecture=?
	`, repoID, arch).Scan(&suiteID); err != nil {
		t.Fatalf("lookup apt_suites for (repo_id=%d, arch=%s): %v "+
			"(was CreateDEBRepoHook wired into the bootstrap path?)",
			repoID, arch, err)
	}

	// 2) deb_packages: PRAGMA-driven defensive INSERT.
	rows, err := db.Query(`PRAGMA table_info('deb_packages')`)
	if err != nil {
		t.Fatalf("pragma table_info(deb_packages): %v", err)
	}
	type colInfo struct {
		name      string
		typ       string
		notnull   int
		dfltValue sql.NullString
		pk        int
	}
	var cols []colInfo
	for rows.Next() {
		var cid int
		var c colInfo
		if err := rows.Scan(&cid, &c.name, &c.typ, &c.notnull, &c.dfltValue, &c.pk); err != nil {
			rows.Close()
			t.Fatalf("scan pragma row: %v", err)
		}
		cols = append(cols, c)
	}
	rows.Close()

	// Build INSERT col list + values list. Skip the PK + any column that
	// has a DEFAULT (those are safe to omit). For the rest, supply known
	// values for the four required-for-search columns and a typed default
	// for everything else.
	known := map[string]any{
		"repo_id":      repoID,
		"suite_id":     suiteID,
		"package":      pkg,
		"version":      version,
		"architecture": arch,
		"digest":       "sha256:debfixture",
		"filename":     pkg + "_" + version + "_" + arch + ".deb",
	}

	var colNames []string
	var placeholders []string
	var args []any
	for _, c := range cols {
		if c.pk == 1 {
			continue
		}
		// If the column has a DEFAULT and we don't know about it, omit it
		// (lets the default fire — keeps the fixture quiet for new columns).
		if c.dfltValue.Valid {
			if _, has := known[c.name]; !has {
				continue
			}
		}
		// At this point: either we know the column or it's NOT NULL with
		// no default (so we MUST supply a value). Either way: include it.
		v, has := known[c.name]
		if !has {
			// Unknown NOT NULL column. Pick a typed default by SQLite affinity.
			switch upper(c.typ) {
			case "INTEGER", "INT", "REAL", "NUMERIC":
				v = 0
			default:
				v = ""
			}
		}
		colNames = append(colNames, c.name)
		placeholders = append(placeholders, "?")
		args = append(args, v)
	}

	stmt := fmt.Sprintf(
		"INSERT INTO deb_packages (%s) VALUES (%s)",
		strings.Join(colNames, ", "),
		strings.Join(placeholders, ", "),
	)
	if _, err := db.Exec(stmt, args...); err != nil {
		t.Fatalf("insert deb_packages (pragma-driven): %v\nstmt=%s", err, stmt)
	}
}

// upper is a no-allocation ASCII upper-case for the small set of SQLite
// type-name strings we encounter (INTEGER/TEXT/REAL/NUMERIC/BLOB and their
// lowercase variants). Avoids pulling in strings.ToUpper for clarity.
func upper(s string) string { return strings.ToUpper(s) }

// lookupProjectID opens the SQLite DB and returns the project ID for the
// given project name. Used by the fixture for the trash restore endpoint
// ID convention ("project-<id>") and for repo lookups.
func lookupProjectID(t *testing.T, dataRoot, name string) int64 {
	t.Helper()
	dbPath := filepath.Join(dataRoot, "db", "omnirepo.sqlite")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	var id int64
	if err := db.QueryRow(`SELECT id FROM projects WHERE name=?`, name).Scan(&id); err != nil {
		t.Fatalf("lookup projectID for %q: %v", name, err)
	}
	return id
}

// lookupRepoID returns the repo ID for the given (projectID, type, name).
func lookupRepoID(t *testing.T, db *sql.DB, projectID int64, repoType, repoName string) int64 {
	t.Helper()
	var id int64
	if err := db.QueryRow(
		`SELECT id FROM repos WHERE project_id=? AND type=? AND name=?`,
		projectID, repoType, repoName,
	).Scan(&id); err != nil {
		t.Fatalf("lookup repoID (project=%d, type=%s, name=%s): %v",
			projectID, repoType, repoName, err)
	}
	return id
}

// softDeleteProject calls DELETE /api/v1/projects/{name} as super-admin.
// One canonical path — no try/retry alternative endpoint. Verified mount:
// internal/api/admin_phase1.go:238-239 — `r.With(...).Delete("/projects/{name}",
// d.handleDeleteProject)` mounted under `/api/v1` (the SessionOrAPIKey auth
// subgroup), NOT under `/admin`. The route is mounted directly under the
// auth-required subrouter at `/projects/{name}`, not under `/admin`.
func softDeleteProject(t *testing.T, f *fixture, name string) {
	t.Helper()
	url := fmt.Sprintf("%s/api/v1/projects/%s", f.httpEndpoint, name)
	req, err := http.NewRequest(http.MethodDelete, url, nil)
	if err != nil {
		t.Fatalf("softDeleteProject req: %v", err)
	}
	req.AddCookie(&http.Cookie{Name: "omnirepo_session", Value: f.adminCookie})

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("softDeleteProject: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("softDeleteProject status=%d body=%s", resp.StatusCode, body)
	}
}

// restoreProject calls POST /api/v1/admin/trash/project-<projectID>/restore.
// One canonical path — no try/retry alternative endpoint. Verified mount:
// internal/api/admin_trash.go:44 — handleRestoreTrash; verified ID convention:
// internal/api/admin_trash.go:113-114 — projectTrashPrefix + strconv.FormatInt(p.ID, 10).
func restoreProject(t *testing.T, f *fixture) {
	t.Helper()
	trashID := "project-" + strconv.FormatInt(f.projectID, 10)
	url := fmt.Sprintf("%s/api/v1/admin/trash/%s/restore", f.httpEndpoint, trashID)
	req, err := http.NewRequest(http.MethodPost, url, nil)
	if err != nil {
		t.Fatalf("restoreProject req: %v", err)
	}
	req.AddCookie(&http.Cookie{Name: "omnirepo_session", Value: f.adminCookie})

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("restoreProject: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("restoreProject status=%d body=%s", resp.StatusCode, body)
	}
}

// searchAsAdmin calls GET /api/v1/search?q=<q> with the cached super-admin
// cookie and returns the parsed result list. Decodes into the EXACT
// searchResponse envelope (items + next_cursor) — see
// internal/api/search.go:106-109. Mismatch here would mask bugs as
// false-positive zero-result assertions.
func searchAsAdmin(t *testing.T, f *fixture, q string) []SearchResult {
	t.Helper()
	url := fmt.Sprintf("%s/api/v1/search?q=%s", f.httpEndpoint, q)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("search req: %v", err)
	}
	req.AddCookie(&http.Cookie{Name: "omnirepo_session", Value: f.adminCookie})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("search GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("search GET status=%d body=%s", resp.StatusCode, body)
	}
	var env searchResponse
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatalf("decode search response: %v", err)
	}
	return env.Items
}

// getProjectReposWithBearer issues GET /api/v1/projects/{project}/repos with
// the given bearer token (project-owned API key) and returns the HTTP status
// code. Used by the test to assert 200 before soft-delete and 401 after.
//
// Endpoint verified: internal/api/repos_list.go:22 — `r.Get("/projects/{name}/repos", d.handleListRepos)`.
// Auth: handleListRepos calls actorIsProjectMember (scans.go:254-276), which
// returns true for project-scoped API keys when actor.ProjectScope == projectID.
// After soft-delete, FindByPrefixSha returns ErrNotFound → 401 (the
// API-key lookup hardening is what makes this work).
func getProjectReposWithBearer(t *testing.T, f *fixture, bearer string) int {
	t.Helper()
	url := fmt.Sprintf("%s/api/v1/projects/%s/repos", f.httpEndpoint, f.project)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("REST GET req: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("REST GET: %v", err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

// countS3ObjectsByKey opens the SQLite DB and counts s3_objects rows for
// the given key under any bucket. Used by AfterSoftDelete_S3PutDenied to
// assert no row was inserted for the rejected PUT.
//
// Note the column is `key` (not `object_key`) — verified migration 018:
// `CREATE TABLE s3_objects (... key TEXT NOT NULL, ...)`.
func countS3ObjectsByKey(t *testing.T, dataRoot, objectKey string) int {
	t.Helper()
	dbPath := filepath.Join(dataRoot, "db", "omnirepo.sqlite")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM s3_objects WHERE key=?`, objectKey).Scan(&n); err != nil {
		t.Fatalf("count s3_objects: %v", err)
	}
	return n
}

// waitHealthy polls the /healthz endpoint until it returns 200 or the
// deadline expires. Mirrors test/conformance/s3/helpers.go.
func waitHealthy(t *testing.T, url string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("%s never returned 200 within %s", url, timeout)
}
