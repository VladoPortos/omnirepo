package git_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vladoportos/omnirepo/internal/audit"
	"github.com/vladoportos/omnirepo/internal/auth"
	"github.com/vladoportos/omnirepo/internal/metadata"
	gitpkg "github.com/vladoportos/omnirepo/internal/protocol/git"
)

// TestGitFetchAudit_AttributesActorViaBox proves the AuditMiddleware actor box
// bridges the authenticated actor — added by the downstream auth middleware to
// a derived context — back to RecordGitRequest, which runs in the wrapping
// AuditMiddleware and therefore never sees that derived context directly.
// Anonymous fetches stay unattributed.
func TestGitFetchAudit_AttributesActorViaBox(t *testing.T) {
	uid := int64(77)
	keyID := int64(88)
	cases := []struct {
		name         string
		actor        *auth.Actor // nil → anonymous (auth never sets a principal)
		wantUserID   *int64
		wantAPIKeyID *int64
	}{
		{"user", &auth.Actor{ID: uid, Kind: auth.ActorKindUser, Login: "alice"}, &uid, nil},
		{"apikey", &auth.Actor{APIKeyID: keyID, Kind: auth.ActorKindAPIKey}, nil, &keyID},
		{"anonymous", nil, nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logger := &fakeAuditLogger{}
			h := gitpkg.New(gitpkg.Deps{Audit: logger})

			inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.actor != nil {
					// Stand in for the downstream auth middleware: WithActor
					// fills the box AuditMiddleware seeded.
					_ = auth.WithActor(r.Context(), *tc.actor)
				}
				w.WriteHeader(http.StatusOK)
			})
			mw := gitpkg.AuditMiddleware(h)(inner)

			req := httptest.NewRequest(http.MethodPost, "/p/git/secret.git/git-upload-pack", nil)
			// ResolveRepoFromURL runs OUTSIDE AuditMiddleware, so the repo is on
			// the request context before the box is seeded.
			req = req.WithContext(gitpkg.WithRepo(req.Context(), &metadata.Repo{ID: 5, Name: "secret"}))
			mw.ServeHTTP(httptest.NewRecorder(), req)

			var fetch *audit.Event
			for i := range logger.events {
				if logger.events[i].Kind == audit.EvtGitFetch {
					fetch = &logger.events[i]
				}
			}
			if fetch == nil {
				t.Fatalf("no git.fetch event emitted; got %+v", logger.events)
			}
			if !eqInt64Ptr(fetch.ActorUserID, tc.wantUserID) {
				t.Fatalf("ActorUserID = %v, want %v", deref(fetch.ActorUserID), deref(tc.wantUserID))
			}
			if !eqInt64Ptr(fetch.ActorAPIKeyID, tc.wantAPIKeyID) {
				t.Fatalf("ActorAPIKeyID = %v, want %v", deref(fetch.ActorAPIKeyID), deref(tc.wantAPIKeyID))
			}
		})
	}
}

func eqInt64Ptr(a, b *int64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func deref(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}
