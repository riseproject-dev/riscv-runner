---
title: Database Schema
parent: Architecture
nav_order: 5
---

# Database Schema

Persistent state for the service lives in a single Scaleway Managed PostgreSQL database. Tables are isolated by schema (`prod` or `staging`), selected per-connection via `SET search_path`. There is no auto-migration: the DDL below is the source of truth and must be applied by hand when it changes.

The schema models three things:

- **`jobs`** records demand (each GitHub `workflow_job` becomes one row).
- **`workers`** records supply (each provisioned pod becomes one row; never deleted).
- **`installation_events`** is an append-only history of every webhook delivery and every scheduler GitHub-auth failure. See [Installation event log](installation-events).

## Enums

```sql
CREATE TYPE status_enum      AS ENUM ('pending', 'running', 'completed', 'failed');
CREATE TYPE provider_enum    AS ENUM ('github', 'gitlab', 'azdo');
CREATE TYPE entity_type_enum AS ENUM ('Organization', 'User');
```

## `jobs`

```sql
CREATE TABLE jobs (
    job_id          BIGINT PRIMARY KEY,
    status          status_enum NOT NULL DEFAULT 'pending',
    failure_info    JSONB,
    provider        provider_enum NOT NULL,
    entity_id       BIGINT NOT NULL,
    entity_name     TEXT NOT NULL,
    entity_type     TEXT NOT NULL,                -- 'Organization' or 'User'
    repo_full_name  TEXT NOT NULL,
    installation_id BIGINT NOT NULL,
    job_labels      JSONB NOT NULL DEFAULT '[]',  -- sorted at write time
    k8s_pool        TEXT NOT NULL,
    k8s_image       TEXT NOT NULL,
    k8s_pod         TEXT,
    html_url        TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_jobs_active    ON jobs (entity_id, job_labels, created_at) WHERE status != 'completed';
CREATE INDEX idx_jobs_reconcile ON jobs (installation_id)                   WHERE status != 'completed';
CREATE INDEX idx_jobs_created   ON jobs (created_at DESC);
```

Inserts come exclusively from [`ghfe`](ghfe), with `ON CONFLICT (job_id) DO NOTHING` so redelivered webhooks are no-ops. Every `INSERT` fires a trigger that emits `NOTIFY {schema}_queue_event`; the scheduler `LISTEN`s on that channel and wakes immediately rather than waiting for its 15-second tick.

Status transitions are forward-only: `pending → running → completed | failed`. All `UPDATE` queries enforce this with explicit `WHERE` clauses, so out-of-order webhook deliveries cannot regress state.

## `workers`

```sql
CREATE TABLE workers (
    pod_name        TEXT PRIMARY KEY,
    provider        provider_enum NOT NULL,
    entity_id       BIGINT NOT NULL,
    entity_name     TEXT NOT NULL,
    entity_type     TEXT NOT NULL,                -- 'Organization' or 'User'
    installation_id BIGINT NOT NULL,              -- GitHub App installation
    repo_full_name  TEXT,                         -- set only for User entities; NULL for Organization
    job_labels      JSONB NOT NULL DEFAULT '[]',
    k8s_pool        TEXT NOT NULL,
    k8s_image       TEXT NOT NULL,
    k8s_node        TEXT,
    status          status_enum NOT NULL DEFAULT 'pending',
    failure_info    JSONB,                        -- exhaustive diagnostics for Failed and stuck pods (version=2)
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    running_at      TIMESTAMPTZ,                  -- set when pod first reaches Running
    completed_at    TIMESTAMPTZ,                  -- set when status transitions to completed | failed
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_workers_active ON workers (entity_id, job_labels, k8s_pool) WHERE status != 'completed';
```

Inserts come exclusively from the [scheduler](scheduler) as part of `tryProvision` (which retries on `ErrDuplicatePodName` to handle name collisions). Worker rows are **never deleted**: terminal rows with `failure_info` populated are kept for post-mortem debugging. The `(entity_id, job_labels)` index supports demand matching.

A worker row's lifecycle is decoupled from its pod's. The row transitions to `completed` or `failed` on pod phase change, even though the pod itself stays around for the 6-hour delete grace so its logs and events remain inspectable.

## `installation_events`

```sql
CREATE TABLE installation_events (
    id                BIGSERIAL PRIMARY KEY,
    source            TEXT NOT NULL,              -- 'webhook' or 'scheduler'
    event             TEXT NOT NULL,              -- '{X-GitHub-Event}.{payload.action}' or 'auth_attempt.{status}'
    outcome           TEXT NOT NULL,              -- WebhookOutcome value (open-set TEXT, no migration on add)
    installation_id   BIGINT,
    app_id            BIGINT,                     -- GHAppOrgID or GHAppPersonalID
    entity_type       entity_type_enum,
    entity_id         BIGINT,                     -- installation.target_id = account.id (stable across renames)
    entity_name       TEXT,                       -- account login
    payload           JSONB NOT NULL,             -- full webhook body, or synthesised payload for scheduler rows
    received_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_install_events_installation ON installation_events (installation_id, entity_id);
CREATE INDEX idx_install_events_entity       ON installation_events (entity_id, received_at DESC);
```

`ghfe` writes one row per accepted webhook; the scheduler writes one row per GitHub-auth failure. See [Installation event log](installation-events) for the read side, available diagnoses, and the trace endpoints.

## Demand matching query

The scheduler's `demandMatch` reduces to two `COUNT`s keyed by `(entity_id, job_labels)`. `job_labels` is the JSONB array sorted at write time, so equality matches without normalisation at read time.

```sql
-- demand
SELECT COUNT(*) FROM jobs
WHERE entity_id = $1 AND job_labels = $2
  AND status IN ('pending', 'running');

-- supply
SELECT COUNT(*) FROM workers
WHERE entity_id = $1 AND job_labels = $2
  AND status IN ('pending', 'running');
```

`failed` workers do not count toward supply, so the next loop iteration automatically re-provisions a runner for the same pending job.

## Transactional model

- **`ghfe`** writes the `jobs` side-effect and the `installation_events` row in **separate transactions**. If the log write fails after the side-effect has committed, the handler returns 500 and GitHub redelivers. Re-deliveries converge: `add_job` is idempotent and the worker-status updates are no-ops on a second run.
- **Scheduler** runs `syncWorkersState` inside `WithWorkerLock`, which opens a transaction, issues `LOCK TABLE workers IN EXCLUSIVE MODE`, and pins the same connection for all `mark_worker_*` calls. Concurrent scheduler instances block on the lock, never racing.
- The scheduler `WaitForJob` opens a separate `pgx.Conn` (outside the pool) for `LISTEN {schema}_queue_event` so the long-lived notification connection does not exhaust the connection pool.

## Connection settings

The runtime opens a `pgxpool.Pool` with `PostgresMaxConn = 10` and a separate `pgx.Conn` for the `LISTEN` channel. The DSN is read from `POSTGRES_URL`; the schema (`prod` or `staging`) is selected per-connection via `SET search_path` in the post-connect hook. SSL is required by Scaleway (`sslmode=require`).
