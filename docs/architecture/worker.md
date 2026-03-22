---
title: Worker & Scheduling
parent: Architecture
nav_order: 2
---

# Worker & Scheduling

The worker is a background thread that runs a reconciliation loop every 15 seconds. It matches pending job demand to available RISC-V node capacity and provisions runner pods.

**Source:** [`container/worker.py`](https://github.com/riseproject-dev/riscv-runner-app/blob/main/container/worker.py)

## Reconciliation loop

Each iteration runs four operations sequentially:

1. **GitHub reconciliation** (`gh_reconcile`): syncs Redis state with GitHub's actual job status
2. **Pod cleanup** (`cleanup_pods`): deletes completed/failed pods and removes stale worker records
3. **Job cleanup** (`cleanup_jobs`): removes completed job hashes older than 15 days
4. **Demand matching** (`demand_match`): provisions new runner pods for pending jobs

The loop uses a `threading.Condition` to wake immediately when the handler stores a new job, rather than waiting for the next 15-second tick.

## Demand matching

For each pending job (processed FIFO by `created_at`):

1. **Demand check**: count jobs vs. workers for the pool. Skip if supply meets demand.
2. **Max workers cap**: skip if the entity (organization or personal account) has reached its `max_workers` limit across all pools.
3. **Capacity check**: query Kubernetes for available `riseproject.com/runner` slots on nodes matching the pool's node selector. Skip if no slots are free.
4. **Provision**: if all checks pass:
   - Authenticate with the correct GitHub App (org app or personal app, based on entity type)
   - For organizations: ensure a runner group named "RISE RISC-V Runners" exists, then create an org-scoped JIT runner config
   - For personal accounts: create a repo-scoped JIT runner config
   - Create a Kubernetes pod with the JIT config
   - Record the worker in Redis

## Pod provisioning

Pods are created via the Kubernetes API with:

- **Node selector:** `riseproject.dev/board: {pool}` (targets the correct hardware)
- **Resource limit:** `riseproject.com/runner: 1` (enforces one pod per node via the device plugin)
- **Active deadline:** 525,600 seconds (~6 days) (prevents stuck pods)
- **Init container:** DinD sidecar for Docker support
- **Environment:** JIT runner config, Docker TLS certificates
- **Volumes:** Docker certs (emptyDir), Docker storage, workspace

The main container runs `./run.sh --jitconfig {config}`, which starts the GitHub Actions runner with the JIT configuration.

## GitHub reconciliation

The worker periodically checks GitHub's API for actual job status and syncs Redis:

- If GitHub reports `completed` but Redis shows `pending` or `running`, the worker updates Redis
- If GitHub reports `in_progress` but Redis shows `pending`, the worker updates Redis

This handles cases where webhooks are missed or delayed.

## Cleanup

**Pod cleanup:** Lists all runner pods. Deletes those in `Succeeded` or `Failed` phase. Removes corresponding worker records from Redis. Also removes Redis worker entries that have no matching pod (stale records).

**Job cleanup:** Removes completed job hashes older than 15 days. Active jobs are preserved indefinitely.

## Configuration

| Setting | Value | Source |
|---------|-------|--------|
| Poll interval | 15 seconds | `serve.py` |
| Max workers per entity | Configurable per org/account | `constants.py` (`ENTITY_CONFIG`) |
| Job retention | 15 days | `worker.py` |
| Pod active deadline | 525,600 seconds | `k8s.py` |

## Related files

- [`container/worker.py`](https://github.com/riseproject-dev/riscv-runner-app/blob/main/container/worker.py): reconciliation loop and demand matching
- [`container/k8s.py`](https://github.com/riseproject-dev/riscv-runner-app/blob/main/container/k8s.py): Kubernetes pod provisioning and capacity checks
- [`container/github.py`](https://github.com/riseproject-dev/riscv-runner-app/blob/main/container/github.py): GitHub API (JIT config, runner groups, job status)
- [`container/db.py`](https://github.com/riseproject-dev/riscv-runner-app/blob/main/container/db.py): Redis operations for jobs and workers
