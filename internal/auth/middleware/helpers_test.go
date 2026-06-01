package middleware_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/vladoportos/omnirepo/internal/auth"
	"github.com/vladoportos/omnirepo/internal/auth/middleware"
	"github.com/vladoportos/omnirepo/internal/metadata"
	"github.com/vladoportos/omnirepo/internal/metadata/sqlitetest"
)

// testEnv bundles the seeded DB + repos + deps used by middleware tests.
type testEnv struct {
	DB       *metadata.DB
	Users    *metadata.UsersRepo
	Sessions *metadata.SessionsRepo
	APIKeys  *metadata.APIKeysRepo
	Deps     middleware.Deps

	// Seeded fixtures.
	AliceID         int64  // normal user, password=swordfish
	AlicePwPlain    string // swordfish
	CarolID         int64  // MCP user, password=please-change
	CarolPwPlain    string
	AliceAPIKey     auth.APIKey // user key belonging to Alice
	AliceSessionTok string      // plaintext session cookie value for Alice
	AliceSessionID  int64
}

func newEnv(t *testing.T) *testEnv {
	t.Helper()
	db := sqlitetest.New(t)
	users := metadata.NewUsersRepo(db)
	sessions := metadata.NewSessionsRepo(db)
	apikeys := metadata.NewAPIKeysRepo(db)

	e := &testEnv{
		DB:       db,
		Users:    users,
		Sessions: sessions,
		APIKeys:  apikeys,
		Deps: middleware.Deps{
			Users:    users,
			Sessions: sessions,
			APIKeys:  apikeys,
			Clock:    func() time.Time { return time.Unix(1_700_000_000, 0).UTC() },
		},
	}

	// Seed Alice (normal).
	e.AlicePwPlain = "swordfish"
	hash, err := auth.HashPassword(e.AlicePwPlain)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	e.AliceID, err = users.Create(context.Background(), "alice", "alice@x", hash, false, false)
	if err != nil {
		t.Fatalf("seed alice: %v", err)
	}

	// Seed Carol (must-change-password).
	e.CarolPwPlain = "please-change"
	hash2, err := auth.HashPassword(e.CarolPwPlain)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	e.CarolID, err = users.Create(context.Background(), "carol", "carol@x", hash2, false, true)
	if err != nil {
		t.Fatalf("seed carol: %v", err)
	}

	// Seed user-scoped API key for Alice.
	k, err := auth.GenerateAPIKey(auth.APIKeyKindUser)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	if _, err := apikeys.CreateUserKey(context.Background(), e.AliceID, "dev", k.Prefix, k.SHA256); err != nil {
		t.Fatalf("seed apikey: %v", err)
	}
	e.AliceAPIKey = k

	// Seed a session for Alice.
	s, err := auth.GenerateSession()
	if err != nil {
		t.Fatalf("gen session: %v", err)
	}
	id, err := sessions.Create(context.Background(), e.AliceID, s.Prefix, s.SHA256,
		time.Now().UTC(), time.Now().Add(24*time.Hour).UTC())
	if err != nil {
		t.Fatalf("seed session: %v", err)
	}
	e.AliceSessionTok = s.Plaintext
	e.AliceSessionID = id

	return e
}

// okHandler writes 200 + "ok,login=<login>". Used as the terminal handler
// in middleware chains to confirm Actor reached the handler.
func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a, ok := auth.ActorFromContext(r.Context())
		if !ok {
			http.Error(w, "no actor", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok,login=" + a.Login))
	})
}
