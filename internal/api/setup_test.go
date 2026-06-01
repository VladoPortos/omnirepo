package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"

	"github.com/vladoportos/omnirepo/internal/api"
	"github.com/vladoportos/omnirepo/internal/metadata"
)

// postSetup is a raw POST /api/v1/setup/superadmin with no auth, returning the
// status code and decoded response (decoded even on error for inspection).
func postSetup(t *testing.T, s *testServer, body any) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", s.ts.URL+"/api/v1/setup/superadmin", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	out := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func getSetupStatus(t *testing.T, s *testServer) (int, bool) {
	t.Helper()
	resp, err := http.Get(s.ts.URL + "/api/v1/setup/status")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var got api.SetupStatusResponse
	_ = json.NewDecoder(resp.Body).Decode(&got)
	return resp.StatusCode, got.NeedsSetup
}

func TestSetup_Status_EmptyDB_NeedsSetup(t *testing.T) {
	s := newTestServer(t)
	code, needs := getSetupStatus(t, s)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if !needs {
		t.Fatalf("needs_setup = false on empty DB, want true")
	}
}

func TestSetup_Status_NonEmptyDB_DoesNotNeedSetup(t *testing.T) {
	s := newTestServer(t)
	seedTestUser(t, s.db, "alice", "a@x.y", false, false)
	code, needs := getSetupStatus(t, s)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if needs {
		t.Fatalf("needs_setup = true with user present, want false")
	}
}

func TestSetup_SuperAdmin_CreatesUserAndLogsAudit(t *testing.T) {
	s := newTestServer(t)

	code, body := postSetup(t, s, api.SetupSuperAdminRequest{
		Login:    "root",
		Email:    "root@example.com",
		Password: "correct horse battery staple",
	})
	if code != http.StatusOK {
		t.Fatalf("create setup: code = %d, body = %+v", code, body)
	}
	if body["login"] != "root" {
		t.Fatalf("login = %v, want root", body["login"])
	}
	if body["is_super_admin"] != true {
		t.Fatalf("is_super_admin = %v, want true", body["is_super_admin"])
	}

	// The new user must be queryable, marked super-admin, and must NOT have
	// must_change_password set (bootstrap semantics: first operator starts
	// with the password they just chose).
	u, err := metadata.NewUsersRepo(s.db).FindByLogin(context.Background(), "root")
	if err != nil {
		t.Fatalf("FindByLogin after setup: %v", err)
	}
	if !u.IsSuperAdmin {
		t.Fatalf("is_super_admin persisted = false, want true")
	}
	if u.MustChangePassword {
		t.Fatalf("must_change_password persisted = true, want false")
	}

	// Second call must 409 — the endpoint is one-shot.
	code2, _ := postSetup(t, s, api.SetupSuperAdminRequest{
		Login: "other", Email: "o@x.y", Password: "another-long-password",
	})
	if code2 != http.StatusConflict {
		t.Fatalf("second setup call: code = %d, want 409", code2)
	}

	// Status endpoint must now report needs_setup=false.
	_, needs := getSetupStatus(t, s)
	if needs {
		t.Fatalf("needs_setup after successful setup = true, want false")
	}
}

func TestSetup_SuperAdmin_ValidationErrors(t *testing.T) {
	cases := []struct {
		name string
		req  api.SetupSuperAdminRequest
		want int
	}{
		{"empty login", api.SetupSuperAdminRequest{Login: "", Email: "x@y.z", Password: "longenough"}, http.StatusUnprocessableEntity},
		{"bad login chars", api.SetupSuperAdminRequest{Login: "root user!", Email: "x@y.z", Password: "longenough"}, http.StatusUnprocessableEntity},
		{"empty email", api.SetupSuperAdminRequest{Login: "root", Email: "", Password: "longenough"}, http.StatusUnprocessableEntity},
		{"short password", api.SetupSuperAdminRequest{Login: "root", Email: "x@y.z", Password: "short"}, http.StatusUnprocessableEntity},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newTestServer(t)
			code, _ := postSetup(t, s, c.req)
			if code != c.want {
				t.Fatalf("code = %d, want %d", code, c.want)
			}
			// Verify nothing was persisted.
			n, _ := metadata.NewUsersRepo(s.db).Count(context.Background())
			if n != 0 {
				t.Fatalf("users count after failed validation = %d, want 0", n)
			}
		})
	}
}

func TestSetup_SuperAdmin_RejectedWhenUsersExist(t *testing.T) {
	s := newTestServer(t)
	seedTestUser(t, s.db, "pre-existing", "p@x.y", false, false)

	code, _ := postSetup(t, s, api.SetupSuperAdminRequest{
		Login: "root", Email: "root@x.y", Password: "long-enough-pw",
	})
	if code != http.StatusConflict {
		t.Fatalf("code = %d, want 409", code)
	}
}

func TestSetup_SuperAdmin_ConcurrentRequestsCreateExactlyOneUser(t *testing.T) {
	// Regression guard: a naive pre-flight empty check + separate create tx
	// let two concurrent bootstrap requests with different logins both
	// succeed. With the empty-check inlined into the write tx, only one
	// request wins; the loser must get 409 and the database must contain
	// exactly one user.
	s := newTestServer(t)

	const N = 8
	var wg sync.WaitGroup
	codes := make([]int, N)
	wg.Add(N)
	for i := 0; i < N; i++ {
		i := i
		go func() {
			defer wg.Done()
			login := "admin" + string(rune('0'+i))
			code, _ := postSetup(t, s, api.SetupSuperAdminRequest{
				Login: login, Email: login + "@x.y", Password: "longsecret123",
			})
			codes[i] = code
		}()
	}
	wg.Wait()

	ok, conflict := 0, 0
	for _, c := range codes {
		switch c {
		case http.StatusOK:
			ok++
		case http.StatusConflict:
			conflict++
		default:
			t.Errorf("unexpected status code %d", c)
		}
	}
	if ok != 1 {
		t.Fatalf("expected exactly one 200, got %d (codes=%v)", ok, codes)
	}
	if conflict != N-1 {
		t.Fatalf("expected %d 409s, got %d (codes=%v)", N-1, conflict, codes)
	}
	n, _ := metadata.NewUsersRepo(s.db).Count(context.Background())
	if n != 1 {
		t.Fatalf("users.Count after concurrent setup = %d, want 1", n)
	}
}

func TestSetup_SuperAdmin_CanThenLogin(t *testing.T) {
	// End-to-end: after setup, /auth/login with the new credentials must
	// succeed and return is_super_admin=true.
	s := newTestServer(t)

	pw := "my-strong-password"
	code, _ := postSetup(t, s, api.SetupSuperAdminRequest{
		Login: "root", Email: "root@x.y", Password: pw,
	})
	if code != http.StatusOK {
		t.Fatalf("setup: code = %d", code)
	}

	_, loginResp, lcode := s.login(t, "root", pw)
	if lcode != http.StatusOK {
		t.Fatalf("login after setup: code = %d", lcode)
	}
	if !loginResp.IsSuperAdmin {
		t.Fatalf("login response is_super_admin = false, want true")
	}
	if loginResp.MustChangePassword {
		t.Fatalf("login response must_change_password = true, want false")
	}
}
