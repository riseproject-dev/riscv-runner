---
title: Runbooks
parent: Operations
nav_order: 2
---

# Runbooks

Day-to-day operational tasks for the service.

## Inspecting live state

The scheduler exposes read-only HTML dashboards over the public function URL:

- `/usage` — active jobs and workers grouped by `(entity_id, labels)`. Useful for "is anything currently running".
- `/jobs` (alias `/history`) — paginated job history with status filtering.
- `/workers` — paginated worker history including `failure_info` for failed pods.

Each page has a `.json` variant returning paginated JSON with a GitHub-style `Link` header. Query params: `start`, `end` (`YYYY-MM-DD` or `-Xd`), `page`, `per_page` (default 100).

## Cleaning up terminated runner pods

Runner pods stay alive for 6 hours after reaching `Succeeded` or `Failed` so their logs and events stay inspectable via `kubectl`. The worker row in PostgreSQL transitions to `completed`/`failed` immediately on phase change, so pool supply accounting stays correct throughout the grace period.

To force cleanup ahead of the grace period:

```sh
kubectl delete pods \
  -l app=rise-riscv-runner \
  --field-selector=status.phase!=Running,status.phase!=Pending,status.phase!=Unknown
```

The scheduler's `DeleteTerminalPods` phase runs the same logic on its own once pods are past `PodDeleteGrace`. The manual command above is for situations where you want the slot freed up sooner.

## Inspecting database state

Use the `POSTGRES_URL` secret's connection string:

```sh
psql "$POSTGRES_URL"
```

Common queries:

```sql
-- Current demand for a label set
SELECT COUNT(*) FROM staging.jobs
WHERE entity_id = :entity_id
  AND job_labels = '["ubuntu-24.04-riscv"]'
  AND status IN ('pending', 'running');

-- Current supply for a label set
SELECT COUNT(*) FROM staging.workers
WHERE entity_id = :entity_id
  AND job_labels = '["ubuntu-24.04-riscv"]'
  AND status IN ('pending', 'running');

-- Single job
SELECT * FROM staging.jobs WHERE job_id = :job_id;

-- Recent failed workers
SELECT pod_name, entity_id, k8s_pool, failure_info
FROM staging.workers
WHERE status = 'failed'
ORDER BY completed_at DESC
LIMIT 10;

-- Workers that never registered with GitHub
SELECT pod_name, entity_name, completed_at
FROM staging.workers
WHERE status = 'failed'
  AND failure_info->>'reason' = 'runner_never_registered'
ORDER BY completed_at DESC
LIMIT 20;

-- Failure-reason histogram for the last day
SELECT failure_info->>'reason' AS reason, COUNT(*)
FROM staging.workers
WHERE status = 'failed'
  AND completed_at > now() - interval '24 hours'
GROUP BY 1;
```

`failure_info->>'reason'` values: `pod_failed` (Kubernetes `Failed` phase), `pod_stuck_pending` (never reached Running), `runner_never_registered` (Running but never appeared in the GitHub API), `runner_idle` (registered with GitHub but stayed idle past the timeout), `node_unreachable` (pod was stranded on a node tainted with `node.kubernetes.io/unreachable`).

Substitute `prod.` for `staging.` when inspecting the production schema.

## Debugging an installation

When a user's jobs stop getting picked up, walk the [installation event log](../architecture/installation-events):

```sh
# By installation_id, if you have it from the user's settings page:
TRACE_API_SECRET=... python3 scripts/trace_installation.py --installation-id 12345

# By account login (resolves via `gh api /users` / `/orgs`):
TRACE_API_SECRET=... python3 scripts/trace_installation.py --entity-name luhenry

# Starting from a specific job ID (resolves entity via jobs.entity_id):
TRACE_API_SECRET=... python3 scripts/trace_installation.py --job-id 56781234
```

The CLI renders a chronological table with rule-based diagnosis hints. Common diagnoses are tabulated in [Installation Event Log § State reconstruction](../architecture/installation-events#state-reconstruction).

## Rotating GitHub App keys

Both apps share `GHAPP_WEBHOOK_SECRET`. Each app has its own RSA private key (`GHAPP_ORG_PRIVATE_KEY`, `GHAPP_PERSONAL_PRIVATE_KEY`).

1. Generate a new private key in the GitHub App settings page (`Generate a private key`). GitHub serves the old and new key in parallel during the rotation window.
2. Update the matching repository secret in `Settings → Secrets and variables → Actions → New repository secret`. Use the `prod` or `staging` environment as appropriate.
3. Redeploy `ghfe` and `scheduler`: trigger `Deploy Container` from the Actions tab, selecting the environment.
4. Confirm in `/usage` that new jobs continue to be picked up.
5. Delete the old key in the GitHub App settings once you have verified the new one works.

## Required secrets

| Secret | Used by | Purpose |
|---|---|---|
| `SCW_SECRET_KEY` | `deploy-container.yml`, `deploy-images.yml`, `deploy-device-plugin.yml` | Scaleway API key for registry login and `serverless deploy` |
| `GHAPP_WEBHOOK_SECRET` | `ghfe` runtime | HMAC secret shared by both GitHub Apps |
| `GHAPP_ORG_PRIVATE_KEY` | `ghfe`, `scheduler` runtime | RSA private key for the org App (PEM) |
| `GHAPP_PERSONAL_PRIVATE_KEY` | `ghfe`, `scheduler` runtime | RSA private key for the personal App (PEM) |
| `K8S_KUBECONFIG` | `scheduler` runtime, image and device-plugin deploys | Kubeconfig with `edit` (scheduler) or cluster-admin (deploys) access |
| `POSTGRES_URL` | `ghfe`, `scheduler` runtime | DSN, e.g. `postgresql://user:pass@host:5432/db?sslmode=require` |
| `TRACE_API_SECRET` | `ghfe`, `trace_installation.py` | Bearer token for `/trace/*` endpoints |
| `RISCV_RUNNER_SAMPLE_ACCESS_TOKEN` | `deploy-container.yml` | PAT for triggering the sample workflow on staging deploy |

## Build a runner image locally

For experimenting with the runner image without going through CI:

```sh
docker buildx build \
  --platform linux/riscv64 \
  --file images/runner/Dockerfile.ubuntu \
  --build-arg OS_VERSION=24.04 \
  --tag riscv-runner:ubuntu-24.04-local \
  images/runner
```

Best run on a RISC-V host so no emulation is involved. On x86_64, `binfmt_misc` with QEMU will let the build complete, slowly.

## Rolling out an image update

1. Merge a PR that changes `images/**`. CI builds `:ubuntu-24.04-sha-<sha>` and pushes it.
2. `deploy-staging` retags as `:ubuntu-24.04-staging`, then `kubectl rollout restart daemonset/rise-riscv-runner-device-plugin -n kube-system`. The init container in the daemonset pre-pulls the new image to every node.
3. The `deploy-prod` job waits for an environment-gated approval before retagging as `:ubuntu-24.04-latest`. New runner pods provisioned after that point pull the new image.

A runner pod that is currently running an old image will keep that image for the duration of its job. Image updates are not retroactive.
