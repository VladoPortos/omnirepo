// Package common holds the handler scaffolding shared by OmniRepo's
// protocol handlers (rpm, deb, pypi, helm, raw, oci, git). Each helper
// here existed as a near-identical copy in every protocol package; the
// canonical behavior is defined once and the protocols delegate.
//
// This package may import auth/audit/metadata freely — unlike httpx,
// which auth itself imports and therefore must stay auth-agnostic.
package common

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/vladoportos/omnirepo/internal/audit"
	"github.com/vladoportos/omnirepo/internal/auth"
	"github.com/vladoportos/omnirepo/internal/httpx"
	"github.com/vladoportos/omnirepo/internal/metadata"
)

// SeverityGateFn is the scan-severity gate hook shared by the file-based
// protocol handlers. Plug nil for no-op (tests); app.Run wires a real gate
// that inspects repos.block_on_severity + scans. (OCI's gate has a
// different shape and lives in the oci package.)
type SeverityGateFn func(ctx context.Context, repoID int64, artifactKind, artifactID string) (blocked bool, severity string, scanID int64)

// AttachAnonymous wires an anonymous Actor into ctx (used by
// httpx.AnonymousReadOK; the callback indirection exists because httpx
// cannot import auth).
var AttachAnonymous httpx.AttachAnonymousFn = func(ctx context.Context) context.Context {
	return auth.WithActor(ctx, auth.Actor{Kind: auth.ActorKindAnonymous})
}

// SkipIfActor wraps a middleware so it pass-throughs when an Actor is
// already in ctx (the anonymous fast path set by AnonymousReadOK).
func SkipIfActor(mw func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		wrapped := mw(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, ok := auth.ActorFromContext(r.Context()); ok {
				next.ServeHTTP(w, r)
				return
			}
			wrapped.ServeHTTP(w, r)
		})
	}
}

// RepoPublicReadLookup returns an httpx.RepoLookupFn resolving
// (project, repoType, repo) → (public_read, found) via the metadata repos.
func RepoPublicReadLookup(projects *metadata.ProjectsRepo, repos *metadata.ReposRepo) httpx.RepoLookupFn {
	return func(ctx context.Context, project, repoType, repo string) (bool, bool) {
		if projects == nil || repos == nil {
			return false, false
		}
		p, err := projects.FindByName(ctx, project)
		if err != nil || p == nil {
			return false, false
		}
		rr, err := repos.FindByTriple(ctx, p.ID, repoType, repo)
		if err != nil || rr == nil {
			return false, false
		}
		return rr.PublicRead, true
	}
}

// RepoURLExtractor returns an httpx.RepoExtractorFn for the canonical
// /<project>/<repoType>/<repo>/... protocol mount shape. Used by
// httpx.AnonymousReadOK. (OCI's /v2/... shape has its own extractor.)
func RepoURLExtractor(repoType string) httpx.RepoExtractorFn {
	return func(r *http.Request) (project, typ, repo string, ok bool) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		parts := strings.SplitN(p, "/", 4)
		if len(parts) < 3 {
			return "", "", "", false
		}
		if parts[1] != repoType {
			return "", "", "", false
		}
		if parts[0] == "" || parts[2] == "" {
			return "", "", "", false
		}
		return parts[0], repoType, parts[2], true
	}
}

// AuditEvent records a protocol audit event with actor + request fields
// filled in uniformly. Best-effort: errors are swallowed, nil logger is a
// no-op. targetKind names the protocol's artifact row (e.g. "rpm_package").
func AuditEvent(logger audit.Logger, r *http.Request, kind audit.EventKind, targetKind, targetID, outcome string, details map[string]any) {
	if logger == nil {
		return
	}
	e := audit.Event{
		Kind:       kind,
		IP:         r.RemoteAddr,
		UserAgent:  r.Header.Get("User-Agent"),
		TargetKind: targetKind,
		TargetID:   targetID,
		Outcome:    outcome,
		Details:    details,
		OccurredAt: time.Now().UTC(),
	}
	if a, ok := auth.ActorFromContext(r.Context()); ok {
		switch a.Kind {
		case auth.ActorKindUser:
			id := a.ID
			e.ActorUserID = &id
		case auth.ActorKindAPIKey:
			id := a.APIKeyID
			e.ActorAPIKeyID = &id
		}
	}
	_ = logger.Record(r.Context(), e)
}

// RequireRepoWrite enforces the maintainer-required policy for artifact
// writes/deletes via auth.Can: super-admin bypasses, the project key's /
// user's role is resolved through ResolveMembership, and viewers +
// viewer-scoped keys are denied.
func RequireRepoWrite(ctx context.Context, actor auth.Actor, members *metadata.MembersRepo, projectID int64, action auth.Action) bool {
	mctx := auth.ResolveMembership(ctx, actor, members)
	allowed, _ := auth.Can(mctx, actor, action, auth.Target{
		Kind:      "repo",
		ProjectID: projectID,
	})
	return allowed
}

// ActorCanRead consults auth.Can for ActionRepoRead. Populates ctx with
// project membership so private repos enforce it.
func ActorCanRead(r *http.Request, members *metadata.MembersRepo, repo *metadata.Repo) bool {
	a, ok := auth.ActorFromContext(r.Context())
	if !ok {
		return false
	}
	ctx := auth.ResolveMembership(r.Context(), a, members)
	allowed, _ := auth.Can(ctx, a, auth.ActionRepoRead, auth.Target{
		Kind:       "repo",
		ProjectID:  repo.ProjectID,
		RepoID:     repo.ID,
		PublicRead: repo.PublicRead,
	})
	return allowed
}

// ValidateFilename rejects path traversal, NUL bytes, empty segments, and
// any filename containing a path separator. Protocol artifacts live in a
// single directory per repo — no nested layout. requiredSuffix, when
// non-empty, additionally pins the extension (e.g. ".rpm").
func ValidateFilename(raw, requiredSuffix string) (string, error) {
	if raw == "" {
		return "", errors.New("empty filename")
	}
	if strings.ContainsRune(raw, '\x00') {
		return "", errors.New("nul byte in filename")
	}
	if strings.ContainsAny(raw, "/\\") {
		return "", errors.New("filename must not contain separators")
	}
	if raw == "." || raw == ".." {
		return "", errors.New("invalid filename")
	}
	// Defense in depth: path.Clean should be a no-op.
	if path.Clean(raw) != raw {
		return "", errors.New("non-canonical filename")
	}
	if requiredSuffix != "" && !strings.HasSuffix(raw, requiredSuffix) {
		return "", fmt.Errorf("filename must end in %s", requiredSuffix)
	}
	return raw, nil
}

// WriteSeverityBlocked emits the 403 blocked_by_scan JSON shared by the
// file-based protocol severity gates. (OCI's variant carries cve_count
// and lives in the oci package.)
func WriteSeverityBlocked(w http.ResponseWriter, severity string, scanID int64) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusForbidden)
	fmt.Fprintf(w, `{"error":"blocked_by_scan","severity":%q,"scan_id":%d}`, severity, scanID)
}
