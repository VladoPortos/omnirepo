package api

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vladoportos/omnirepo/internal/auth"
	"github.com/vladoportos/omnirepo/internal/metadata"
	"github.com/vladoportos/omnirepo/internal/metadata/sqlitetest"
)

// Once setup is complete the endpoint must reject before decoding or hashing
// the submitted password. Besides making the one-shot contract explicit, this
// prevents an unauthenticated caller from using the retired setup endpoint as
// an unlimited Argon2 CPU/memory sink.
func TestSetupSuperAdminCompletedInstallRejectsBeforeBodyValidation(t *testing.T) {
	db := sqlitetest.New(t)
	users := metadata.NewUsersRepo(db)
	hash, err := auth.HashPassword("already-installed-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := users.Create(context.Background(), "pre-existing", "p@x.y", hash, false, false); err != nil {
		t.Fatal(err)
	}

	d := Deps{DB: db, Users: users}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/setup/superadmin", bytes.NewBufferString("not-json"))
	w := httptest.NewRecorder()
	d.handleSetupSuperAdmin(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("status=%d want 409 before request-body validation", w.Code)
	}
}
