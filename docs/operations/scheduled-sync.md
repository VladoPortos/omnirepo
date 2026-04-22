# Scheduled mirror sync

Drive OmniRepo mirror syncs on a fixed cadence from your platform's cron primitive (crontab, systemd timers, Kubernetes CronJob) — with a 409-idempotent, poll-to-terminal script that exits non-zero only on real failures.

## When to use this

OmniRepo does not include an in-process scheduler. To run mirror syncs on a fixed cadence, drive the REST API from your platform's cron primitive (crontab, systemd timers, Kubernetes CronJob). The worked example below fires a sync, polls until the job reaches a terminal state, and surfaces a distinct exit code per failure mode so the surrounding scheduler can alert correctly.

## API-key auth

Use a **project-scoped API key** (prefix `omr_p_`) with permission to trigger syncs. Mint one from the web UI under **Project → Settings → API Keys** — the plaintext is shown exactly once.

Pass the key via the **`Authorization: Bearer <omr_p_...>`** header. OmniRepo does not accept custom API-key headers — the Bearer scheme (or HTTP Basic with `__token__:<key>`) is the only supported path, enforced in `internal/auth/middleware/session_or_apikey.go`.

Do not embed the key literal in the script. Load it from a file (chmod 600) or a Kubernetes Secret and export it into the script's environment, as shown below.

## Worked example: APT mirror (archive.ubuntu.com/ubuntu)

The script below fires a sync for an APT mirror repo, polls its sync-job until it reaches a terminal state, and exits with a distinct code per failure mode. Treat `409 mirror.sync.in_flight` as an idempotent happy path — another caller (or a previous cron tick whose sync outlived its window) is already doing the work.

<!-- shellcheck-id: scheduled-sync -->
```bash
#!/usr/bin/env bash
set -euo pipefail

# Environment (set via cron or the K8s Secret below):
#   OMNIREPO_URL       - e.g. https://omnirepo.example.corp:8443
#   OMNIREPO_API_KEY   - omr_p_... project-scoped API key (scope=sync.trigger)
#   PROJECT, REPO_TYPE, REPO - mirror identifier
#
# Defaults: MAX_ATTEMPTS=60 x SLEEP_SECONDS=10 = 10 min ceiling.
# archive.ubuntu.com APT sync empirically takes 2-4 min; 10 min
# leaves headroom without tolerating runaway jobs.

: "${OMNIREPO_URL:?}" "${OMNIREPO_API_KEY:?}" "${PROJECT:?}" "${REPO_TYPE:?}" "${REPO:?}"

AUTH_HEADER="Authorization: Bearer ${OMNIREPO_API_KEY}"
BASE="${OMNIREPO_URL}/api/v1/projects/${PROJECT}/repos/${REPO_TYPE}/${REPO}"

MAX_ATTEMPTS=60
SLEEP_SECONDS=10

# 1. Enqueue the sync. 202 -> {"job_id": N, "kind": "..."}.
#    409 mirror.sync.in_flight = idempotent happy exit (0).
resp=$(curl -fsS --max-time 30 -H "${AUTH_HEADER}" -X POST "${BASE}/sync" || true)
case "$resp" in
  *"mirror.sync.in_flight"*)
    echo "Sync already in flight - trusting it. Exiting 0."
    exit 0
    ;;
  *"job_id"*)
    job_id=$(echo "$resp" | jq -r .job_id)
    ;;
  *)
    echo "Unexpected response: $resp" >&2
    exit 2
    ;;
esac

# 2. Poll to terminal state.
for ((i=0; i<MAX_ATTEMPTS; i++)); do
  status=$(curl -fsS --max-time 30 -H "${AUTH_HEADER}" \
    "${BASE}/sync-jobs/${job_id}" | jq -r .status)
  case "$status" in
    done)   echo "Sync complete (job ${job_id})."; exit 0 ;;
    failed) echo "Sync failed (job ${job_id}). See admin UI."; exit 1 ;;
    running|pending) sleep "${SLEEP_SECONDS}" ;;
    *)      echo "Unknown status: $status" >&2; exit 2 ;;
  esac
done

echo "Timed out waiting for job ${job_id} after $((MAX_ATTEMPTS*SLEEP_SECONDS))s" >&2
exit 3
```

Required runtime dependencies: `bash` (>= 4 for the C-style `for` loop), `curl`, `jq`. `curlimages/curl` in the Kubernetes section below bundles a busybox shell that supports the same syntax.

## Example crontab entry

```cron
# /etc/crontab fragment - nightly APT mirror sync at 02:00.
# Shell wrapper loads OMNIREPO_API_KEY from /etc/omnirepo/cron.env
# (chmod 600, owner root) so the key never appears in process args.
0 2 * * * root OMNIREPO_URL=https://omnirepo.corp:8443 \
  OMNIREPO_API_KEY=$(cat /etc/omnirepo/cron.env) \
  PROJECT=platform REPO_TYPE=deb REPO=ubuntu-mirror \
  /opt/omnirepo/sync.sh >> /var/log/omnirepo-sync.log 2>&1
```

## Kubernetes CronJob alternative

A middle-ground manifest — no custom image, no cluster-scoped RBAC, but tight enough to be safe in production. The five non-default knobs below each earn their keep:

- `concurrencyPolicy: Forbid` — belt-and-suspenders with OmniRepo's own 409 handling. Prevents a queue of waiting jobs if a sync runs past its schedule.
- `backoffLimit: 0` — OmniRepo's polling script is the failure domain; Kubernetes retry on top would double-fire and confuse alerting.
- `activeDeadlineSeconds: 3600` — hard ceiling on runaway jobs. Matches the 10-minute script timeout with generous headroom.
- `secretKeyRef` — API key is mounted from a Secret, never baked into the manifest or an image.
- `image: curlimages/curl` — upstream-maintained minimal image with `curl` + `sh`; nothing to build or mirror.

```yaml
apiVersion: batch/v1
kind: CronJob
metadata:
  name: omnirepo-apt-sync
spec:
  schedule: "0 2 * * *"  # nightly at 02:00
  concurrencyPolicy: Forbid
  successfulJobsHistoryLimit: 3
  failedJobsHistoryLimit: 7
  jobTemplate:
    spec:
      backoffLimit: 0          # OmniRepo's 409 handles retry semantics
      activeDeadlineSeconds: 3600
      template:
        spec:
          restartPolicy: Never
          containers:
            - name: sync
              image: curlimages/curl:8.9.1
              command: ["/bin/sh", "-c"]
              args:
                - |
                  set -eu
                  AUTH_HEADER="Authorization: Bearer ${OMNIREPO_API_KEY}"
                  BASE="${OMNIREPO_URL}/api/v1/projects/${PROJECT}/repos/${REPO_TYPE}/${REPO}"
                  MAX_ATTEMPTS=60
                  SLEEP_SECONDS=10

                  resp=$(curl -fsS --max-time 30 -H "${AUTH_HEADER}" -X POST "${BASE}/sync" || true)
                  case "$resp" in
                    *"mirror.sync.in_flight"*) echo "Already in flight - exiting 0"; exit 0 ;;
                    *"job_id"*) job_id=$(echo "$resp" | sed -n 's/.*"job_id":[[:space:]]*\([0-9]*\).*/\1/p') ;;
                    *) echo "Unexpected response: $resp" >&2; exit 2 ;;
                  esac

                  i=0
                  while [ "$i" -lt "$MAX_ATTEMPTS" ]; do
                    status=$(curl -fsS --max-time 30 -H "${AUTH_HEADER}" \
                      "${BASE}/sync-jobs/${job_id}" \
                      | sed -n 's/.*"status":[[:space:]]*"\([^"]*\)".*/\1/p')
                    case "$status" in
                      done)   echo "Sync complete (job ${job_id})."; exit 0 ;;
                      failed) echo "Sync failed (job ${job_id})."; exit 1 ;;
                      running|pending) sleep "$SLEEP_SECONDS" ;;
                      *) echo "Unknown status: $status" >&2; exit 2 ;;
                    esac
                    i=$((i+1))
                  done
                  echo "Timed out waiting for job ${job_id}" >&2
                  exit 3
              env:
                - name: OMNIREPO_URL
                  value: "https://omnirepo.corp:8443"
                - name: OMNIREPO_API_KEY
                  valueFrom:
                    secretKeyRef:
                      name: omnirepo-sync-key
                      key: api-key
                - name: PROJECT
                  value: "platform"
                - name: REPO_TYPE
                  value: "deb"
                - name: REPO
                  value: "ubuntu-mirror"
```

Create the Secret separately so the key never lands in the manifest:

```sh
kubectl create secret generic omnirepo-sync-key \
  --from-literal=api-key="$(cat /etc/omnirepo/cron.env)"
```

Note on the inline script: `curlimages/curl` ships busybox `sh` rather than bash, so the inline variant above uses POSIX `while` and `sed`-based JSON extraction instead of the bash C-style `for` loop and `jq` used in the crontab version. If you prefer the richer bash/jq script, mount it from a ConfigMap and point `command` at `/bin/bash /scripts/sync.sh` in an image that ships both.

## Substituting protocols

The same flow works for RPM (`REPO_TYPE=rpm`), PyPI (`REPO_TYPE=pypi`), Helm (`REPO_TYPE=helm`), and Docker/OCI (`REPO_TYPE=docker`). The URL path is `/api/v1/projects/<name>/repos/<type>/<repo>/sync` for every mirror type — the script code is protocol-agnostic.

## What happens on failure

| Exit | Meaning |
|------|---------|
| `0`  | Sync completed successfully **or** `mirror.sync.in_flight` idempotent hit. |
| `1`  | Sync job reached terminal status `failed`. Investigate via admin UI → **Background Jobs**, or fetch `last_error` from `GET /sync-jobs/{id}`. |
| `2`  | Unexpected REST response or unknown status. Likely misconfiguration (wrong API key, wrong URL, or an OmniRepo upgrade changed response shape — file an issue). |
| `3`  | `MAX_ATTEMPTS × SLEEP_SECONDS` elapsed without reaching a terminal state. The job may still complete; exit 3 is the alert, not the truth. |

Cron daemons typically only notify on non-zero exits, so this taxonomy lets you wire each code to a different severity (e.g. page on 1/2, warn on 3, stay silent on 0).
