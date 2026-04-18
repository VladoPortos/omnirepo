# OmniRepo 1.0 — End-to-End Acceptance Test Plan

Status: **draft, not yet executed**. This file is the starting point for a
fresh Claude Code session; read it top to bottom, then execute the phases
in order.

## 1. Purpose

Validate OmniRepo behaves correctly as a production-style deployment, not
just as the `make dev` loop we've been building against. Specifically:

1. **Real container:** run OmniRepo from its own Docker image with a
   mounted persistent volume — the path a homelab operator would take.
2. **Real protocols under load:** seed the instance with enough real-world
   artifacts (thousands of packages, dozens of images) that the Scan
   queue, SQLite metadata, disk layout, and UI get stressed.
3. **Real client bootstrap:** reconfigure the host OS so its only
   package source is OmniRepo, then install a package and pull a Docker
   image end-to-end. If a fresh Ubuntu 22.04 host can finish an
   `apt install` and a `docker pull` entirely through OmniRepo, with
   cert + auth + protocol parity working, we ship.

The intent is exploratory — we're looking for things that *only show up at
scale or in production config*: DB contention, disk layout cliffs, missing
`InRelease` signatures, broken digest redirects, scan pool stuck states,
UI pagination issues, etc.

## 2. Environment

Confirm at session start:
- Host: Ubuntu 22.04 Jammy on WSL2 (`cat /etc/os-release`).
- Free disk on `/`: currently ~63 GB. The plan assumes 60 GB usable; drop
  Phase 4 (Ubuntu mirror) first if the budget gets tight.
- Docker: 29.4.0 already installed (`docker --version`). Must have
  `docker login` not set to anything that blocks anonymous Docker Hub
  pulls.
- Go 1.25, Node 22, `make` all available (they built `f63ce9c` fine).
- Repo cloned at `/home/vladoportos/omnirepo`.

Allocate a data volume up front:

```bash
mkdir -p /tmp/omnirepo-e2e/{data,logs,mirrors,backup}
```

`/tmp/omnirepo-e2e/data` is the OmniRepo persistent volume. `mirrors/`
holds downloaded packages before upload. `backup/` holds files we need
to restore at the end (apt sources, etc.). `logs/` for captured output.

## 3. Phases

Each phase has: **goal**, **commands**, **observations** (what to capture
before moving on), **exit criteria** (go / no-go).

---

### Phase 1 — Build and run from the real Docker image

**Goal:** deploy OmniRepo the way a user would: `docker run` against the
image produced by `Dockerfile`, with a mounted volume, a real self-signed
cert, a real bootstrap admin, and HTTPS on 8443.

**Commands:**

```bash
cd /home/vladoportos/omnirepo

# Build image (uses vendored deps so it works without internet during build)
docker build -t omnirepo:e2e .

# Run with persistent volume. Expose 8080 (HTTP) and 8443 (HTTPS).
docker run -d --name omnirepo-e2e \
  -p 8080:8080 -p 8443:8443 \
  -v /tmp/omnirepo-e2e/data:/var/lib/omnirepo \
  --restart=unless-stopped \
  omnirepo:e2e

# Wait for health, then capture startup log.
for i in 1 2 3 4 5 6 7 8 9 10; do
  if curl -sk https://localhost:8443/api/v1/setup/status > /dev/null; then break; fi
  sleep 2
done
docker logs omnirepo-e2e > /tmp/omnirepo-e2e/logs/01-boot.log 2>&1
```

Then walk the first-run setup **via the UI in a browser** (accept the
self-signed cert), not via curl:
- Set super-admin password.
- Create the test project (name it `e2e`).
- Create four repos under `e2e`: `docker` (type=docker), `ubuntu`
  (type=deb), `oracle` (type=rpm), and `raw-test` (type=raw) so we
  have something to compare ingest behaviour across types.

**Observations to capture:**
- Startup time from `docker run` to first healthy request.
- Resident memory at idle (`docker stats omnirepo-e2e --no-stream`).
- `/var/lib/omnirepo` tree after first boot (should have the canonical
  directories from `internal/app/dirs.go`).
- Whether the embedded Trivy DB is detected (`/admin/trivy` shows
  `baked-in` source).
- HTTPS cert fingerprint visible in browser — confirm it's the self-
  signed one the server generated.

**Exit criteria:** login works, all four repos appear in the UI, Trivy DB
status is `baked-in` or we've uploaded one.

---

### Phase 2 — Seed 20 Docker images from Docker Hub

**Goal:** fill the OCI registry with a representative set of real-world
images and verify protocol parity with `docker push`/`docker pull`.

**Image list** (20 official/library images, mix of sizes and architectures):

```
alpine:3.20
busybox:1.36
nginx:1.27-alpine
httpd:2.4-alpine
redis:7.4-alpine
postgres:16-alpine
mariadb:11
mysql:8.4
python:3.12-slim
node:22-alpine
golang:1.23-alpine
ruby:3.3-alpine
php:8.3-fpm-alpine
rabbitmq:3.13-alpine
memcached:1.6-alpine
registry:2.8
caddy:2-alpine
haproxy:3.1-alpine
traefik:v3.1
prom/prometheus:v2.54.1
```

> Docker Hub tag notes (F-T4):
> - `haproxy:3-alpine` does not exist — use `haproxy:3.1-alpine`.
> - Prometheus lives under `prom/`, not `library/` — use
>   `prom/prometheus:v2.54.1`.

Most are <100 MB; a few (mariadb, mysql, golang) are several hundred MB.
Total on-disk after dedup: expect ~3-5 GB.

**Commands** (from a shell with `docker login` cleared so anonymous
Docker Hub rate limits apply):

```bash
# The local registry. Use HTTP on 8080 for docker client — simpler than
# teaching docker about the self-signed cert on 8443. We still push
# with an API key; the client config lives in ~/.docker/config.json.
REG=localhost:8080
# F-T2: the real route is /api/v1/me/api-keys (not /profile/api-keys).
KEY=$(curl -sk -u admin:<PW> https://localhost:8443/api/v1/me/api-keys \
    -X POST -d '{"name":"e2e-docker-push"}' | jq -r .token)

docker login $REG -u admin -p "$KEY"

# F-T2: the OCI router matches /v2/{project}/{type}/{repo}/{image} so the
# tag target must be 4-segment (project/docker/repo/image). Pushing to the
# 3-segment `${REG}/e2e/docker/${IMG}` returns NAME_UNKNOWN.
for IMG in alpine:3.20 busybox:1.36 nginx:1.27-alpine httpd:2.4-alpine \
           redis:7.4-alpine postgres:16-alpine mariadb:11 mysql:8.4 \
           python:3.12-slim node:22-alpine golang:1.23-alpine ruby:3.3-alpine \
           php:8.3-fpm-alpine rabbitmq:3.13-alpine memcached:1.6-alpine \
           registry:2.8 caddy:2-alpine haproxy:3.1-alpine traefik:v3.1 \
           prom/prometheus:v2.54.1 ; do
  docker pull $IMG
  # Strip any `prom/` (or other) prefix from the image name so it lands
  # under .../docker/docker/<image>:<tag>; the router expects a single
  # image segment. IMG_LOCAL = basename of the reference.
  IMG_LOCAL="${IMG##*/}"
  docker tag  $IMG $REG/e2e/docker/docker/$IMG_LOCAL
  docker push $REG/e2e/docker/docker/$IMG_LOCAL
done
```

**Observations:**
- Cumulative push time (expect several minutes).
- Disk usage of `/tmp/omnirepo-e2e/data/blobs` after all pushes — compare
  to sum of `docker image ls` sizes to gauge CAS dedup.
- UI behaviour on `/projects/e2e/docker/docker`: tag list length,
  pagination, Scan Status badges if auto-scan is on.
- Scan queue depth (`/admin/trivy`? or internal logs — the `scan pool`
  log lines).

**Exit criteria:** all 20 images visible in the UI, `docker pull` from
OmniRepo of any one of them succeeds with matching digest to what was
pushed.

---

### Phase 3 — Seed Oracle Linux 9 BaseOS (RPM)

**Goal:** exercise the RPM protocol with a full real-world repo — includes
signed metadata (`repomd.xml`, `primary.xml.gz`).

**Approach:** use `dnf reposync` from inside a short-lived Oracle Linux
container to mirror the BaseOS x86_64 repo to
`/tmp/omnirepo-e2e/mirrors/oraclelinux-9-baseos`, then upload the `.rpm`
files to OmniRepo via the REST upload endpoint.

```bash
# Export ~815 RPMs of Oracle Linux 9 BaseOS (x86_64) with --newest-only.
# Expect ~750–800 MB on disk (F-T5: prior "~3,000 RPMs / 5–8 GB" forecast
# was stale — newest-only yields under 1k packages).
docker run --rm -v /tmp/omnirepo-e2e/mirrors:/mirrors \
  oraclelinux:9 bash -c '
    dnf install -y dnf-plugins-core createrepo_c
    dnf reposync --repoid=ol9_baseos_latest --arch=x86_64 --newest-only \
        --download-path=/mirrors
  '

ls /tmp/omnirepo-e2e/mirrors/ol9_baseos_latest/Packages/ | head
du -sh /tmp/omnirepo-e2e/mirrors/ol9_baseos_latest
```

Then upload the RPMs to the `oracle` repo in our `e2e` project:

```bash
KEY=<as above>
# F-T2: RPM PUTs live on the protocol surface, NOT under /api/v1.
#   PUT /<project>/rpm/<repo>/packages/<filename>
# See internal/protocol/rpm/handler.go:133.
REPO=https://localhost:8443/e2e/rpm/oracle/packages
find /tmp/omnirepo-e2e/mirrors/ol9_baseos_latest/Packages -name '*.rpm' |
  xargs -n1 -P4 -I{} curl -sk -u admin:$KEY -X PUT --data-binary @{} \
      "$REPO/$(basename {})"
```

**Observations:**
- Total upload wall-clock (hundreds of PUTs — good stress test for
  SQLite writer contention).
- `repomd.xml` generated at
  `https://localhost:8443/rpm/e2e/oracle/repodata/repomd.xml` (or
  whatever the routed path is in `internal/protocol/rpm/handler.go`).
- Primary metadata size + gzip.
- Scan queue backlog after upload — do we enqueue ~3000 scans? Does the
  pool chew through them? What happens to memory/CPU during that?
- UI `/projects/e2e/rpm/oracle` content tab with pagination working.

**Exit criteria:** a Oracle Linux 9 container pointed at our `oracle`
repo can `dnf makecache` successfully (Phase 6 will do the install
bootstrap).

---

### Phase 4 — Mirror Ubuntu Jammy main + security (APT)

**Goal:** the scale test. APT metadata (`Packages.gz`, `Release`,
`InRelease`) is the most format-sensitive and the most likely to expose
bugs. We also need this for Phase 6 (bootstrapping the host).

**Scope:** jammy main component, amd64, including `jammy`, `jammy-updates`,
`jammy-security`. Skip universe/multiverse/restricted for budget. Expect
~8,000-10,000 `.deb` files, ~15-20 GB.

**Approach:** use `apt-mirror` (in a docker sidecar to avoid polluting the
host's apt config):

```bash
cat > /tmp/omnirepo-e2e/mirrors/mirror.list <<'EOF'
set base_path    /mirrors/ubuntu
set nthreads     8
set _tilde 0

deb-amd64 http://archive.ubuntu.com/ubuntu         jammy          main
deb-amd64 http://archive.ubuntu.com/ubuntu         jammy-updates  main
deb-amd64 http://security.ubuntu.com/ubuntu        jammy-security main
clean http://archive.ubuntu.com/ubuntu
EOF

docker run --rm -v /tmp/omnirepo-e2e/mirrors:/mirrors ubuntu:22.04 bash -c '
  apt-get update && apt-get install -y apt-mirror
  cp /mirrors/mirror.list /etc/apt/mirror.list
  apt-mirror
'
du -sh /tmp/omnirepo-e2e/mirrors/ubuntu/mirror
```

Then upload the `.deb` files to the OmniRepo `ubuntu` repo. The important
part is that our `internal/protocol/deb` handler needs to see the
suite/component metadata — confirm the upload endpoint accepts the suite
+ component params it needs.

```bash
# F-T2: DEB PUTs go to the protocol surface with a full pool path. Real
# route is
#   PUT /<project>/deb/<repo>/pool/<c>/<pkg>/<file>.deb?suite=X&component=Y
# and suites must be declared FIRST via
#   PATCH /<project>/deb/<repo>/suites    (admin/api-key auth)
# See internal/protocol/deb/handler.go:137.

# 1) Declare the suites we're about to ingest (idempotent).
curl -sk -u admin:$KEY -X PATCH \
  -H 'Content-Type: application/json' \
  -d '{"suites":[
        {"suite":"jammy","component":"main","architecture":"amd64"},
        {"suite":"jammy","component":"main","architecture":"all"},
        {"suite":"jammy-updates","component":"main","architecture":"amd64"},
        {"suite":"jammy-updates","component":"main","architecture":"all"},
        {"suite":"jammy-security","component":"main","architecture":"amd64"},
        {"suite":"jammy-security","component":"main","architecture":"all"}
      ]}' \
  https://localhost:8443/e2e/deb/ubuntu/suites

# 2) Upload with the real pool path (F-T6 depends on this: the client-sent
#    path is what Packages.gz emits as the Filename field).
find /tmp/omnirepo-e2e/mirrors/ubuntu/mirror -name '*.deb' |
  while read DEB; do
    # Derive pool path relative to mirror root so apt's canonical layout
    # (pool/main/<prefix>/<src>/<file>.deb) survives upload.
    REL="${DEB#*/pool/}"
    curl -sk -u admin:$KEY -X PUT --data-binary @"$DEB" \
      "https://localhost:8443/e2e/deb/ubuntu/pool/$REL?suite=jammy&component=main"
  done
```

**Observations:**
- Disk use of `/var/lib/omnirepo/repos/e2e/deb/ubuntu/pool/` vs. mirror
  source dir — should match within a few percent.
- Generated `Release` and `InRelease` files at the expected URLs —
  inspect them with `gpg --verify InRelease Release` (the server should
  have generated a signing keypair at repo creation).
- SQLite size after the batch. Note how long
  `sqlite3 omnirepo.sqlite '.dump deb_packages' | wc -l` takes.
- Any lock-contention errors in the server log (`database is locked`)
  during the parallel upload.

**Exit criteria:** `InRelease` verifies, `Packages.gz` lists every
uploaded package, a client container can `apt-get update` from our
`ubuntu` repo without warnings.

---

### Phase 5 — Observe behaviour at scale

At this point the instance has ~13,000 artifacts. Run the Observability
pass **before** moving to Phase 6:

| What | How | What we're looking for |
|------|-----|------------------------|
| Dashboard responsiveness | Open `/` in the UI | Renders within ~1 s even with 13k artifacts |
| Search responsiveness | `/search` for common words | FTS index returns results; no timeout |
| Scan queue health | `/admin/trivy` or tail server log | No stuck scans; backlog drains over time |
| SQLite size | `du -h .../db/omnirepo.sqlite*` | Expect low hundreds of MB, not GB |
| Container memory | `docker stats` | Should plateau; no steady-state leak |
| UI Scan Results tab on a big repo | Open `/projects/e2e/deb/ubuntu` → Scan Results | Date grouping collapses cleanly, no hang |

If any of these fail, stop and file a finding in `test-findings.md`
before moving on. Don't paper over.

---

### Phase 6 — Bootstrap test: host uses ONLY OmniRepo

This is the payoff phase. We reconfigure the host's apt sources so
OmniRepo is the only possible source, then install a package and
verify it came from us.

**Safety protocol** — back up first, always:

```bash
cp /etc/apt/sources.list           /tmp/omnirepo-e2e/backup/sources.list.orig
cp -r /etc/apt/sources.list.d      /tmp/omnirepo-e2e/backup/sources.list.d.orig
cp /etc/hosts                      /tmp/omnirepo-e2e/backup/hosts.orig
```

**Point apt at OmniRepo:**

```bash
sudo tee /etc/apt/sources.list > /dev/null <<EOF
deb [trusted=yes] http://localhost:8080/deb/e2e/ubuntu jammy main
deb [trusted=yes] http://localhost:8080/deb/e2e/ubuntu jammy-updates main
deb [trusted=yes] http://localhost:8080/deb/e2e/ubuntu jammy-security main
EOF

# Disable the third-party source files too — they reach the internet.
sudo bash -c 'for f in /etc/apt/sources.list.d/*.sources /etc/apt/sources.list.d/*.list; do
  [ -f "$f" ] && mv "$f" "$f.disabled-by-omnirepo-test"
done'
```

(Use `[trusted=yes]` for the test to dodge the GPG check; the next
iteration should import the server's signing key and drop that flag.
Flag this in `test-findings.md` either way.)

**Run the test:**

```bash
sudo apt-get update               # must succeed
sudo apt-get install -y jq        # must install entirely from OmniRepo
which jq && jq --version          # sanity
```

**Observations:**
- Every request in `/tmp/omnirepo-e2e/logs/...` hits `localhost:8080`;
  no outbound traffic (verify with `tcpdump -i any 'port 80 and not host
  127.0.0.1'` briefly during the install if possible).
- `apt-cache show jq | grep '^Filename'` — should reference a path
  inside our `/deb/e2e/ubuntu/pool/...`.
- Metadata sigs: retry without `[trusted=yes]`, import our repo signing
  key, confirm `InRelease` validates.

**Second bootstrap: Docker client**

```bash
docker logout
# Configure docker for insecure HTTP registry on localhost:8080
sudo jq '.["insecure-registries"] = ["localhost:8080"]' \
    /etc/docker/daemon.json > /tmp/daemon.json.new || \
    echo '{"insecure-registries":["localhost:8080"]}' | sudo tee /etc/docker/daemon.json
sudo systemctl restart docker     # WSL2: sudo service docker restart

docker pull localhost:8080/e2e/docker/docker/alpine:3.20
docker run --rm localhost:8080/e2e/docker/docker/alpine:3.20 cat /etc/os-release
```

Confirm the pull works and the digest matches what we pushed in Phase 2.

**Exit criteria:** `jq` installed from OmniRepo + alpine container runs
from OmniRepo + zero outbound connections during either operation.

---

### Phase 7 — Oracle Linux client bootstrap (nice-to-have)

Run in a short-lived container since the host is Ubuntu:

```bash
docker run --rm -it --network host oraclelinux:9 bash -c '
  cat > /etc/yum.repos.d/omnirepo.repo <<EOF
[omnirepo-baseos]
name=OmniRepo Test
baseurl=http://localhost:8080/rpm/e2e/oracle
enabled=1
gpgcheck=0
EOF
  rm -f /etc/yum.repos.d/oracle-linux-ol9.repo  # so OmniRepo is the only source
  dnf makecache
  dnf install -y --nodocs nano
  rpm -q nano
'
```

**Exit criteria:** `nano` installs from OmniRepo end-to-end.

---

### Phase 8 — Replacement / reupload test

Test that uploading the *same file again* (e.g. because the mirror refresh
runs twice) does the right thing: update the timestamp, don't duplicate
metadata rows, don't accumulate blobs.

- Re-run a subset of the RPM upload from Phase 3.
- Verify no duplicate rows in `rpm_packages` for the same NEVRA.
- Verify CAS blobs don't multiply.

---

## 4. Teardown — **MUST RUN**

Restore the host so it's usable after the test, regardless of outcome:

```bash
# Stop + remove the container and image
docker stop omnirepo-e2e && docker rm omnirepo-e2e
docker rmi omnirepo:e2e

# Restore apt sources
sudo cp /tmp/omnirepo-e2e/backup/sources.list       /etc/apt/sources.list
sudo rm -rf /etc/apt/sources.list.d
sudo cp -r /tmp/omnirepo-e2e/backup/sources.list.d.orig /etc/apt/sources.list.d

# Restore any disabled .sources/.list files
sudo bash -c 'for f in /etc/apt/sources.list.d/*.disabled-by-omnirepo-test; do
  [ -f "$f" ] && mv "$f" "${f%.disabled-by-omnirepo-test}"
done'
sudo apt-get update

# Restore docker config
# (back up /etc/docker/daemon.json before editing in Phase 6; revert here)

# Delete the mirror data + OmniRepo volume (optional — keep if debugging)
# rm -rf /tmp/omnirepo-e2e
```

## 5. Success criteria (for marking 1.0 ready)

Ship only if **all** pass:

1. ✅ OmniRepo image builds cleanly, starts in < 10 s, has no errors in
   logs at idle.
2. ✅ 20 Docker images push + pull correctly, digests match.
3. ✅ ~800 RPMs ingested (newest-only OL9 BaseOS); `dnf makecache` works
   from the repo.
4. ✅ ~10,000 `.deb`s ingested; `apt-get update` works and `InRelease`
   verifies with the server-generated key.
5. ✅ Host bootstrap: `apt install jq` from OmniRepo as **only**
   source, with no outbound network traffic.
6. ✅ Oracle Linux container bootstrap: `dnf install nano` succeeds.
7. ✅ UI responsive (< 2 s for Dashboard, Search, any repo page) at
   ~13k artifacts.
8. ✅ No `database is locked` errors during any parallel upload burst.
9. ✅ Re-uploading the same artifact is idempotent (no blob/row dup).

Partial pass? Open items go in `test-findings.md`, triage, fix, repeat.

### Run 1 result — 2026-04-18

| # | Criterion | Verdict |
|---|-----------|---------|
| 1 | Image builds, starts <10 s, no idle log errors | **PASS** (container ready <1 s after `docker run`; idle RSS 86 MiB) |
| 2 | 20 images push+pull, digests match | **FAIL** (16/20). 4 blocked by OCI chunk-cap bug (F-T3); digest round-trip for stored images verified |
| 3 | ~3 k RPMs; `dnf makecache` works | **PASS (scoped)** — 815 RPMs ingested (`--newest-only` only gives ~800 in BaseOS); makecache green |
| 4 | ~10 k debs; `apt-get update` + InRelease verify | **PASS (scoped)** — 4850 debs (mirror truncated at disk budget); 74 s bulk upload @ P=6, zero "database is locked"; InRelease GPG-verified with server key fingerprint `075194C6 2A3F8E54 2564D015 13C76688 6551F492` |
| 5 | Host `apt install <pkg>` from OmniRepo only, no outbound | **FAIL → PASS with workaround**. As-is: apt 404s because Packages.gz Filename field doesn't match stored path (F-T6). With hardlink fix, `apt install nano` pulled only from `localhost:8080`, zero non-loopback traffic during install |
| 6 | OL9 `dnf install nano` works | **PASS** (nano-5.6.1-7.el9 installed) |
| 7 | UI responsive at scale | **PASS** — all API endpoints <60 ms at 5.7 k artifacts |
| 8 | No `database is locked` during parallel upload | **PASS** — 6-way deb upload + 4-way rpm upload, server log clean |
| 9 | Re-upload idempotent | **PASS** — 5 RPMs re-PUT returned 201, `rpm_packages` row count unchanged, no NEVRA duplicates |

**Overall verdict:** **NOT SHIP-READY**. Blocker F-T6 (pool-path
generator / filename mismatch) prevents a real apt client from
installing packages ingested via the natural Debian pool layout. See
`test-findings.md` for full findings and fix options. Homelab-ready
checklist after F-T3, F-T6, F-T7 are fixed.

## 6. Known risks / guardrails

- **Disk pressure:** budget is 60 GB. If any single phase blows past
  its forecast, stop and drop scope before the next phase.
- **apt sources half-restored:** *always* run the Teardown commands
  even if the test is aborted. A broken `/etc/apt/sources.list` leaves
  the host unable to install anything.
- **Docker Hub rate limits:** anonymous pulls are capped. If Phase 2
  hits a limit, retry with a free Docker Hub account's creds.
- **Trivy scan storm:** auto-scan of 13k artifacts could pin CPU for
  hours. Turn `auto_scan` off on the `ubuntu` repo before bulk upload;
  rescan selectively via the new /rescan endpoint afterwards to spot-
  check.
- **WSL2 + systemd quirks:** `sudo service docker restart` instead of
  `systemctl`. DNS may need `/etc/resolv.conf` tweaks.
- **Signing key for APT:** OmniRepo auto-generates a GPG keypair per
  APT repo. The public key lives somewhere in the data dir — confirm
  the exact path in `internal/protocol/deb/` before Phase 6 so we can
  drop `[trusted=yes]` and do a real-signed run.

## 7. What *not* to do in this test

- Don't mirror universe/multiverse or Ubuntu's debug archives — scope
  creep, blows disk budget.
- Don't enable auto-scan on every repo during bulk upload — it turns
  the 10-minute ingest into a multi-hour scan storm.
- Don't use `docker-compose up` or `make dev` for the OmniRepo
  instance. The point of this test is the real container.
- Don't leave the host's apt sources pointing at OmniRepo after the
  test. Always run Teardown.

## 8. Deliverables from this test

When the test completes, produce:
- `test-findings.md`: any failures, behaviours to fix, UX papercuts.
- Screenshots of any UI issue found (Playwright MCP, save under
  `test-screenshots/`).
- `/tmp/omnirepo-e2e/logs/` captures for each phase.
- A final pass/fail summary appended to this file at Section 5.
