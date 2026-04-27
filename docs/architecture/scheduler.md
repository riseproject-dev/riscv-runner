---
title: Scheduler
parent: Architecture
nav_order: 2
---

# Scheduler

The scheduler is a background service that runs a reconciliation loop. It matches pending job demand to available RISC-V node capacity, provisions runner pods, syncs worker state with Kubernetes and GitHub, and cleans up terminated pods.

**Source:** [`container/scheduler.py`](https://github.com/riseproject-dev/riscv-runner-app/blob/main/container/scheduler.py)

## Reconciliation loop

The scheduler is woken by PostgreSQL `LISTEN/NOTIFY` (the `ghfe` webhook handler emits a `queue_event` notification when a new job is recorded) or by a 15-second timeout. Each iteration runs three operations:

1. **Job sync** (`sync_jobs_state`): reconciles job rows in PostgreSQL with GitHub's actual job status. Catches missed or out-of-order webhooks.
2. **Worker sync** (`sync_workers_state`): a single transaction that holds `LOCK TABLE workers IN EXCLUSIVE MODE` for its duration and runs five phases: orphan sweep, k8s pod phase → worker status sync, health checks (kill stuck-pending and never-registered runners), GitHub-side cleanup of terminal/orphaned runners, and deletion of pods past the 6-hour grace period.
3. **Demand match** (`demand_match`): provisions new runner pods where demand exceeds supply.

Only one scheduler at a time may run `sync_workers_state`. If a second instance is deployed it blocks on the table lock until the first commits.

## Demand matching

For each pending job (processed FIFO by `created_at`):

1. **Demand check.** Compute `demand = COUNT(jobs WHERE entity_id, job_labels match AND status IN (pending, running))` and `supply = COUNT(workers WHERE entity_id, job_labels match AND status IN (pending, running))`. Skip if `supply >= demand`.
2. **Max workers cap.** Skip if the entity (organization or personal account) has reached its `max_workers` limit across all pools.
3. **Capacity check.** Query Kubernetes for available `riseproject.com/runner` slots on nodes matching the pool's node selector. Skip if no slots are free.
4. **Provision.** If all checks pass:
   - Reserve a worker name in PostgreSQL.
   - Authenticate with the correct GitHub App (org app or personal app, based on entity type).
   - For organizations: ensure a runner group named "RISE RISC-V Runners" exists, then create an org-scoped JIT runner config.
   - For personal accounts: create a repo-scoped JIT runner config.
   - Create a Kubernetes pod with the JIT config injected as `RUNNER_JITCONFIG`.

Demand and supply are matched by `(entity_id, job_labels)` rather than by pool. This avoids stuck workers when different label sets map to the same pool but a workflow expects matching runner labels.

## Pod provisioning

Pods are created via the Kubernetes API with:

- **Node selector:** `riseproject.dev/board: {pool}` (targets the correct hardware).
- **Resource limit:** `riseproject.com/runner: 1` (enforces one pod per node via the device plugin).
- **Active deadline:** 525,600 seconds (~6 days) (prevents stuck pods).
- **Security context:** `privileged: true` (required by the in-pod Docker daemon).
- **Environment:** `RUNNER_JITCONFIG` (the base64 JIT token).
- **No init containers, no volumes.** Runner pods are a single container; the image's entrypoint launches the GitHub Actions runner directly. See [Container Images](images).

## Health checks

Two health checks run inside `sync_workers_state`. Rather than deleting the pod directly, the scheduler patches `spec.activeDeadlineSeconds = 1`. The kubelet then transitions the pod to `Failed` (reason `DeadlineExceeded`) so it enters the normal grace-and-delete flow and its logs and events remain inspectable.

- **`runner_never_registered`:** pod has been `Running` for more than `RUNNER_REGISTRATION_TIMEOUT_SECONDS` (120s) but the runner never appeared in the GitHub API. The worker is marked `failed` with full diagnostics in `failure_info`, then the pod is killed so its slot frees up for a retry.
- **`pod_stuck_pending`:** pod has been `Pending` for more than `POD_PENDING_TIMEOUT_SECONDS` (600s), typically due to missing capacity or an image-pull failure.

If GitHub refuses to delete a runner that is still busy, `sync_workers_state` aborts the cleanup for that worker and retries on the next loop iteration.

## Cleanup

Terminated pods (`Succeeded` or `Failed`) are kept for `POD_DELETE_GRACE_SECONDS` (6 hours) so logs and events remain accessible via `kubectl`. The worker row in PostgreSQL is updated to `completed`/`failed` immediately on phase transition, so pool supply accounting stays accurate throughout the grace period. After the grace period the pod is deleted; the worker row is never deleted.

## Configuration

| Setting | Value | Source |
|---------|-------|--------|
| Poll interval | 15 seconds | `scheduler.py` |
| Max workers per entity | Configurable per org/account | `constants.py` (`ENTITY_CONFIG`) |
| Pod active deadline | 525,600 seconds | `k8s.py` |
| Pod delete grace | 6 hours | `scheduler.py` |
| Runner registration timeout | 120 seconds | `constants.py` |
| Pod pending timeout | 600 seconds | `constants.py` |

## Related files

- [`container/scheduler.py`](https://github.com/riseproject-dev/riscv-runner-app/blob/main/container/scheduler.py): reconciliation loop, demand matching, worker state sync, cleanup.
- [`container/k8s.py`](https://github.com/riseproject-dev/riscv-runner-app/blob/main/container/k8s.py): Kubernetes pod provisioning, capacity checks, failure-info collection.
- [`container/github.py`](https://github.com/riseproject-dev/riscv-runner-app/blob/main/container/github.py): GitHub API (JIT config, runner groups, job status).
- [`container/db.py`](https://github.com/riseproject-dev/riscv-runner-app/blob/main/container/db.py): PostgreSQL operations for jobs and workers.
