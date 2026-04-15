# go-git v6 Spike Results (Phase 1, D-38 / D-39)

**Ran:** 2026-04-15
**go-git/v6 version:** `v6.0.0-alpha.1.0.20260414225401-98cdae44aed0`
**Go toolchain:** `go1.25.0 linux/amd64`
**git CLI version:** `git version 2.34.1`
**Command:** `go test -mod=vendor -tags=spike ./internal/protocol/git/spike/... -count=1`

## Outcome

- [x] `git clone` (empty bare) exited 0
- [x] `git push origin HEAD:refs/heads/main` exited 0
- [x] `git fetch origin` exited 0
- [x] second `git clone` exited 0
- [x] `clone2/README.md` contained `"hello"`

End-to-end **PASS** — `go test -mod=vendor -tags=spike ./internal/protocol/git/spike/...` exits 0 in ~42 ms.

## Deviations from RESEARCH.md §A2

**Significant deviation.** RESEARCH.md §A2 assumed the import
`github.com/go-git/go-git/v6/backend` with a `backend.New(loader)` constructor
returning a ready-made `http.Handler`. **No such package exists** in the
pseudo-version we vendor (`v6.0.0-alpha.1.0.20260414225401-98cdae44aed0`).

What v6 actually ships (under `plumbing/transport`):

- `transport.Loader` interface: `Load(u *url.URL) (storage.Storer, error)` —
  the loader signature guess in RESEARCH §A2 was *close* (url-based rather
  than name-based, but conceptually identical).
- `transport.NewFilesystemLoader(billy.FS, strict bool)` — a ready-made
  loader we delegate to per resolved URL.
- `transport.AdvertiseRefs(ctx, storer, w, service, smart bool) error` —
  handles the Smart-HTTP SmartReply preamble internally when `smart=true`.
- `transport.UploadPack(ctx, storer, r, w, opts)` — clone / fetch service.
- `transport.ReceivePack(ctx, storer, r, w, opts)` — push service.

There is **no bundled HTTP handler**; the spike wrote ~150 lines of Smart-HTTP
glue in `internal/protocol/git/spike/spike.go` wrapping those primitives:

- Routes `GET  …/info/refs?service=<svc>` → `AdvertiseRefs(smart=true)`.
- Routes `POST …/git-upload-pack` → `UploadPack(StatelessRPC=true)`.
- Routes `POST …/git-receive-pack` → `ReceivePack(StatelessRPC=true)`.
- Handles `Content-Encoding: gzip` request bodies (git CLI gzips larger pushes).
- Sets `application/x-<svc>-advertisement` / `-result` response Content-Types.

One false start during the spike: we initially hand-wrote the `# service=<svc>`
pktline preamble before calling `AdvertiseRefs`, producing a duplicate that
made `git clone` fail with *"protocol error: unexpected '# service='"*.
`AdvertiseRefs(smart=true)` already emits that preamble internally — removing
our duplicate fixed the clone. Phase 4 implementers should note this.

Second false start: `git init --bare` picked `master` as default HEAD; the
test pushed to `refs/heads/main`, so the second clone checked out an empty
tree. Fixed with `git init --bare --initial-branch=main`.

## Recommendation for Phase 4

**PASS — Proceed with go-git v6 as the primary Git backend in Phase 4**, but
with concrete caveats:

1. Phase 4 must **budget ~200-300 LOC for the Smart-HTTP wrapper** that this
   spike prototyped. It is not as simple as the `backend.New(loader)` one-liner
   RESEARCH §A2 assumed. The spike `spike.go` is a reasonable starting point;
   production will add: auth hooks (pre-`Load` authorization via `auth.Can`),
   audit events (`git.clone` / `git.push` success/failure + bytes), per-repo
   locking through `internal/storage/locks.go`, request-size / timeout guards,
   and structured error responses.
2. **Keep the `sosedoff/gitkit` fallback available behind a config flag**
   (`server.git_backend: gogit | gitkit`) as D-38 originally specified. The v6
   API surface we depend on (`plumbing/transport.AdvertiseRefs`/`UploadPack`/
   `ReceivePack`) is alpha-tagged; a breaking change before v6 ships stable
   is plausible. The fallback is cheap insurance and matches the design
   decision already captured in STATE.md.
3. **Pin** `github.com/go-git/go-git/v6` to the exact pseudo-version used in
   this spike (`v6.0.0-alpha.1.0.20260414225401-98cdae44aed0`) until we
   deliberately upgrade. `go mod vendor` has already captured this.
4. The spike **does not** exercise large packfile pushes, concurrent clones,
   or shallow/partial clones. Phase 4 must add those tests before declaring
   the Git subsystem complete — the acceptance matrix from the spec §11 (Git)
   drives that work.

## Unresolved questions for Phase 4 kick-off

- Do we prefer storing bare repos at `/var/lib/omnirepo/repos/<project>/git/<repo>.git`
  directly, or in the existing `repos/<project>/<type>/<repo>/` layout from D-08?
  (Answer likely: the D-08 layout, with the `.git` suffix appended at storage
  time — but Phase 4 should confirm before migration 00X lands.)
- What HTTP status do we want for AdvertiseRefs write failures? The spike
  currently discards them best-effort; production may want structured logging
  even when headers are already on the wire.
- SSH Git (spec §11 mentions HTTPS only for v1) — explicitly out of scope for
  Phase 4 per REQUIREMENTS.md. Revisit in v2.
