// Package git — middleware chain for the Git Smart-HTTP surface (D-30).
//
// The chain runs in this order on every /git/... request:
//
//  1. BasicOrAPIKey — auth (handled upstream, not in this file)
//  2. ResolveRepoFromURL — parse URL, look up project + repo, stash on ctx
//  3. RequireGitPermission — read action from URL path, check auth.Can
//  4. PerRepoMutex — write-path-only per-repo lock (D-32)
//  5. PushSizeLimit — MaxBytesReader cap (D-33/34/35) — see pushcap.go
//  6. Audit — defer-style capture of method/status/bytes
//
// Plan 09 implements steps 2-4 + 6; step 5 is in pushcap.go.
package git
