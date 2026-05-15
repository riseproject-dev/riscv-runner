# RISC-V Runner App

A GitHub App that listens for `workflow_job` webhooks and provisions ephemeral RISC-V GitHub Actions runners on Kubernetes using a demand-matching model.

## Usage

[**RISE RISC-V Runners**](https://github.com/apps/rise-risc-v-runners) is a GitHub App that provides ephemeral RISC-V runners for GitHub Actions workflows.

### Installation

See [Install the GitHub App](https://riseproject-dev.github.io/riscv-runner/docs/getting-started/install) on the public documentation site for the up-to-date instructions and the differences between the organization and personal-account variants.

### Running workflows on RISC-V

Use `runs-on: ubuntu-24.04-riscv` in your workflow:

```yaml
jobs:
  build:
    runs-on: ubuntu-24.04-riscv
    steps:
      - uses: actions/checkout@v4
      - run: uname -m  # riscv64
```

See [riseproject-dev/riscv-runner-sample](https://github.com/riseproject-dev/riscv-runner-sample) for more examples

Available platform labels:

| Labels | Board | Description |
|--------|-------|-------------|
| `ubuntu-24.04-riscv` | `scw-em-rv1` | Scaleway EM-RV1 RISC-V |

### Requirements

- Install the GitHub App on your organization or personal account.
- Runners are ephemeral -- each runner handles exactly one job and then terminates.

## Architecture

The app uses a **demand matching** model: on one side, workflow_jobs create demand for runners; on the other, k8s workers provide supply. The scheduler scales supply to match demand per (entity, job_labels) pool, with configurable limits.

Two GitHub Apps are used: one for organizations (org-scoped runners with runner groups) and one for personal accounts (repo-scoped runners). The `entity_id` abstracts over both: it is `org_id` for organizations or `repo_id` for personal accounts.

Jobs and workers are not directly linked -- the only relationship is through the entity. GitHub makes no direct job-to-runner link; a runner is attached to an org or repo, and the job runs inside that context.

The system is split into two containers:
- **ghfe** receives GitHub webhooks, validates them, and writes job state to PostgreSQL. It makes no GitHub API or k8s calls.
- **scheduler** reads job state from PostgreSQL, provisions runner pods on k8s, reconciles with GitHub, and cleans up completed pods.

```
GitHub (workflow_job webhook)
  |
  v
ghfe (ghfe.py)
  |  - Proxies webhooks to staging for staging entities (prod only)
  |  - Verifies webhook signature
  |  - Validates labels, determines entity type (org or personal)
  |  - Resolves (entity_id, job_labels) -> (k8s_pool, k8s_image)
  |  - Writes job to PostgreSQL
  |  - Serves /usage, /history
  |  - NO GitHub API calls, NO k8s calls
  |
  v
PostgreSQL (state store)
  |  - jobs table: all job metadata with status_enum, sorted JSONB labels
  |  - workers table: never deleted, status tracked (pending/running/completed/failed)
  |  - failure_info: exhaustive diagnostics for failed pods (including stuck ones)
  |  - LISTEN/NOTIFY: wakes scheduler on new jobs
  |
  v
Scheduler (scheduler.py)
  |  - sync_jobs_state:    sync job status with GitHub
  |  - sync_workers_state: runs under a per-scheduler LOCK TABLE workers advisory,
  |                        5 phases (atomic, single transaction):
  |                          1. orphan sweep (worker rows without a k8s pod)
  |                          2. k8s pod phase -> worker status sync
  |                          3. health checks: kill stuck-pending & never-registered
  |                             runners and free their slots
  |                          4. GH-side cleanup: delete registered runners whose
  |                             worker row is terminal or missing
  |                          5. delete k8s pods past the 6h grace period
  |  - demand_match:       provision runners where demand > supply
  |  - Woken by PostgreSQL LISTEN/NOTIFY or 15s timeout
  |
  v
Kubernetes (runner pods)
```

Each runner pod is a single container running the [riscv-runner-images](https://github.com/riseproject-dev/riscv-runner-images) image. The pod runs `privileged: true` (required by the in-pod Docker daemon) and receives its JIT runner config through the `RUNNER_JITCONFIG` environment variable.

Only one scheduler at a time may run `sync_workers_state`: each invocation holds `LOCK TABLE workers IN EXCLUSIVE MODE` for its duration, using a thread-local connection pin (`db.hold_connection`) so all `mark_worker_*` calls share the locked transaction. If a second scheduler container is deployed, it will block on the lock until the first commits.

### Sequence: Queued webhook

```
GitHub -> ghfe: workflow_job (action=queued)
ghfe: validate signature, labels, entity type
ghfe: match_labels_to_k8s(labels) -> (k8s_pool, k8s_image)
ghfe -> PostgreSQL: add_job() -> INSERT + NOTIFY queue_event
ghfe -> GitHub: 200 OK
```

### Sequence: Scheduler provisioning

```
Scheduler: woken by LISTEN/NOTIFY (or 15s timeout)
Scheduler: get_pending_jobs() -> SELECT job_id FROM jobs WHERE status='pending' ORDER BY created_at
Scheduler: for each pending job:
  - get_pool_demand(entity_id, job_labels) -> (jobs, workers)
  - if jobs <= workers: skip (demand met)
  - if entity total workers >= max_workers: skip
  - get_available_slots(node_selector): skip if no capacity
  - add_worker(entity_id, k8s_pool, name, labels, image) -> reserve name in DB
  - authenticate_app(installation_id, entity_type) -> token
  - [org] ensure_runner_group(entity_name, token) -> group_id
  - [org] create_jit_runner_config_org(token, group_id, labels, entity_name, name) -> jit_config
  - [personal] create_jit_runner_config_repo(token, labels, repo_full_name, name) -> jit_config
  - provision_runner(jit_config, name, image, pool, entity_id, entity_name) -> pod
```

### Sequence: In-progress webhook

```
GitHub -> ghfe: workflow_job (action=in_progress)
ghfe -> PostgreSQL: mark_job_running(job_id)
  - UPDATE jobs SET status='running' WHERE status='pending'
ghfe -> GitHub: 200 OK
```

### Sequence: Completed webhook

```
GitHub -> ghfe: workflow_job (action=completed)
ghfe -> PostgreSQL: mark_job_completed(job_id)
  - UPDATE jobs SET status='completed' WHERE status IN ('pending', 'running')
ghfe -> GitHub: 200 OK
```

### Sequence: Cancellation

Cancellation is passive. When a job is cancelled on GitHub:
1. The `completed` webhook fires and marks the job completed in PostgreSQL
2. If a worker was already provisioned, it picks up another job or times out
3. GH reconciliation detects stale jobs within ~15s and cleans them up

### Job lifecycle state machine

```
queued webhook      in_progress webhook     completed webhook
    |                       |                       |
    v                       v                       v
 PENDING  ----------->  RUNNING  ----------->  COMPLETED
    |                                               ^
    +-----------------------------------------------+
              completed webhook (before provision)
```

### Worker lifecycle

```
add_worker() reserves name in DB (status=pending, running_at=NULL, completed_at=NULL)
  -> k8s pod created
  -> K8s pod Running   -> status=running, running_at set from container start time
  -> K8s pod Succeeded -> status=completed, completed_at set from container finish time
  -> K8s pod Failed    -> status=failed,    completed_at set, failure_info populated
       |
       sync_workers_state keeps Succeeded/Failed pods around for 6 hours
       (POD_DELETE_GRACE_SECONDS) so logs/events remain accessible via kubectl,
       then deletes them. The worker row is updated immediately on phase
       transition (not after delete).
```

Health checks for stuck runners run inside `sync_workers_state` and, rather than
deleting the pod directly, *kill* it by patching `spec.activeDeadlineSeconds = 1`.
The kubelet then transitions the pod to `Failed` (reason `DeadlineExceeded`) so it
enters the normal 6-hour grace-and-delete flow — logs/events remain inspectable:

- **RUNNER_NEVER_REGISTERED**: pod has been Running for more than
  `RUNNER_REGISTRATION_TIMEOUT_SECONDS` (120s) but the runner never appeared in the
  GitHub API. Worker is marked `failed` with full diagnostics in `failure_info`, then
  the pod is killed so its slot frees up for a retry.
- **POD_STUCK_PENDING**: pod has been Pending for more than
  `POD_PENDING_TIMEOUT_SECONDS` (600s), likely due to missing capacity or image pull
  failures. Same remediation.

Both health checks first attempt to delete the runner from GitHub if one is
registered under that name. If GitHub refuses (e.g. `422 "Runner is busy"`),
`sync_workers_state` aborts the cleanup for that worker — GitHub believes a job
is actually running, so we leave the worker alone and retry next cycle.

Workers are never deleted from PostgreSQL. The `status` field tracks the lifecycle: `pending -> running -> completed|failed`. Historical workers with `failure_info` are available for post-mortem debugging.

### GitHub / Kubernetes / DB reconciliation

Phase 4 of `sync_workers_state` cleans up GitHub-registered runners once per cycle:
- Runners registered in GitHub whose worker row is `completed` or `failed` are deleted from GitHub.
- Runners registered in GitHub with no matching worker row (orphans from a previous scheduler, crashed provisioning, etc.) are deleted.
- For org-scoped runners the listing is scoped to the `RISE RISC-V Runners` runner group; for repo-scoped (personal accounts) runners are filtered by the `rise-riscv-runner{-staging}-` name prefix.

### Installation event log

When `gh.authenticate_app()` returns 404 in the scheduler, the matching job
is marked `failed` with `installation not found` — but the *cause* of the
404 is invisible. The user may have uninstalled the app, suspended it,
removed our access to a specific repo, renamed their org/user account, or
installed the wrong app variant on the wrong account type. Without
captured history, we can't tell users why their jobs stopped getting
picked up.

`installation_events` is an append-only table that records, in
chronological order:

- Every webhook delivery the app receives (`installation`,
  `installation_repositories`, `installation_target`, `workflow_job`,
  `ping`, plus a row for any unhandled `X-GitHub-Event` we don't model).
- Every scheduler `gh.authenticate_app()` failure (`auth_attempt.404`,
  `auth_attempt.other_error`).

Each row carries the full payload as `JSONB`, plus filter/index keys
(`installation_id`, `app_id`, `entity_type`, `entity_id`, `entity_name`)
and a free-form `outcome` string. The `WebhookOutcome` enum in
`constants.py` is the canonical list of outcome values; the column itself
is `TEXT` so new outcomes don't require schema migrations. `entity_id`
is the GitHub `account.id`, which is stable across renames and reinstalls
— uninstalling and reinstalling the app produces a new `installation_id`
but keeps the same `entity_id`.

The webhook handler writes the `jobs` side-effect (`add_job`,
`mark_job_running`, `mark_job_completed`) and the `installation_events`
row in **separate transactions**. If the log write fails the side effect
has already committed; the handler returns 500 and GitHub redelivers.
Re-deliveries converge: `add_job` uses `ON CONFLICT (job_id) DO NOTHING`,
the worker-status updates are no-ops on a second run, and the log table
has no UNIQUE constraint on payload so a duplicate log row is acceptable
(the trace endpoints can dedupe by `delivery_id` from the JSONB payload
when needed).

The scheduler's `_gh_authenticate_app` wrapper logs only failures
(`gh.authenticate_app` is `@ttl_cache`-decorated, so success is the hot
path). `cachetools.func.ttl_cache` does not cache exceptions, so transient
errors don't poison subsequent calls.

#### State reconstruction

The log is the source of truth for an entity's installation history. To
answer *"what did installation X look like at time T?"* the trace tool
fetches every event for that entity and folds the payloads in
`received_at` order:

| Event | State change |
|---|---|
| `installation.created` | initial repo set, `app_id`, `repository_selection`, `suspended=false` |
| `installation_repositories.added` | `repos := repos ∪ payload.repositories_added` |
| `installation_repositories.removed` | `repos := repos \ payload.repositories_removed` |
| `installation.suspend` / `installation.unsuspend` | flip `suspended` |
| `installation.deleted` | terminal — `installed=false`, `repos=∅` |
| `installation_target.renamed` | `entity_name := payload.account.login` (the new name) |
| `auth_attempt.404` | the scheduler's most recent failure, with the `app_id` it tried |

Common diagnoses fall straight out of that fold:

| Cause | Signal |
|---|---|
| User uninstalled between job submission and reconcile | `installation.deleted` row preceding the `auth_attempt.404` |
| Admin suspended the installation | `installation.suspend` with no later `unsuspend` |
| Admin removed access to a specific repo | `installation_repositories.removed` mentioning the failing repo |
| Account renamed; cached `entity_name` is stale | `installation_target.renamed` |
| JWT signed by the wrong app for this installation | `auth_attempt.404` row's `app_id` differs from `installation.created.app_id` |
| `repository_selection=selected` and the repo isn't selected | `installation.created` shows `selected` and `installation_repositories.added` never adds the repo |

#### Querying

The `/trace/*` endpoints on `ghfe` return events as JSON. Authentication is
a simple `Authorization: Bearer $TRACE_API_SECRET` check (gates casual
access only — not designed as a security boundary).

```
GET /trace/entity/<int:entity_id>             # all events for one entity
GET /trace/installation/<int:installation_id> # resolves to entity_id, then same
GET /trace/job/<int:job_id>                   # resolves job_id → entity_id via jobs.entity_id
GET /trace/payload/<int:event_id>             # full JSONB payload for one row
```

The list endpoints intentionally **do not** return the `payload` field —
payloads can be tens of KB each and most rows are reviewed at a glance.
For `workflow_job.*` rows the response includes `job_id` and
`repo_full_name` extracted in SQL so the timeline stays readable;
`/trace/payload/<id>` is the way to get the full body for any individual
row.

`scripts/trace_installation.py` is a thin client over the trace endpoints
with a chronological table renderer and rule-based diagnosis hints. It
takes one of `--installation-id`, `--entity-id`, `--entity-name`, or
`--job-id`. The `--entity-name` resolution shells out to `gh api
/users/<login>` (falling back to `/orgs/<login>`) so it requires `gh auth
login`. `PROD_URL` is hard-coded in the script; `TRACE_API_SECRET` comes
from the environment.

### Database schema

Tables live in a `prod` or `staging` schema (same database, isolated by `SET search_path`).

```sql
CREATE TYPE status_enum    AS ENUM ('pending', 'running', 'completed', 'failed');
CREATE TYPE provider_enum  AS ENUM ('github', 'gitlab', 'azdo');
CREATE TYPE entity_type_enum AS ENUM ('Organization', 'User');

CREATE TABLE jobs (
    job_id          BIGINT PRIMARY KEY,
    status          status_enum NOT NULL DEFAULT 'pending',
    failure_info    JSONB,
    provider        provider_enum NOT NULL,
    entity_id       BIGINT NOT NULL,
    entity_name     TEXT NOT NULL,
    entity_type     TEXT NOT NULL,        -- 'Organization' or 'User'
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

CREATE TABLE workers (
    pod_name        TEXT PRIMARY KEY,
    provider        provider_enum NOT NULL,
    entity_id       BIGINT NOT NULL,
    entity_name     TEXT NOT NULL,
    entity_type     TEXT NOT NULL,        -- 'Organization' or 'User'
    installation_id BIGINT NOT NULL,      -- GitHub App installation, needed for reconcile calls
    repo_full_name  TEXT,                 -- only set for User entities (repo-scoped runners); NULL for Organization
    job_labels      JSONB NOT NULL DEFAULT '[]',
    k8s_pool        TEXT NOT NULL,
    k8s_image       TEXT NOT NULL,
    k8s_node        TEXT,
    status          status_enum NOT NULL DEFAULT 'pending',
    failure_info    JSONB,                -- exhaustive diagnostics for Failed and stuck pods (version=2)
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    running_at      TIMESTAMPTZ,          -- set when k8s pod first reaches running
    completed_at    TIMESTAMPTZ,          -- set when status transitions to completed|failed
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_workers_active ON workers (entity_id, job_labels, k8s_pool) WHERE status != 'completed';

CREATE TABLE installation_events (
    id                BIGSERIAL PRIMARY KEY,
    source            TEXT NOT NULL,             -- 'webhook' or 'scheduler'
    event             TEXT NOT NULL,             -- '{X-GitHub-Event}.{payload.action}' for webhooks, 'auth_attempt.{status}' for scheduler
    outcome           TEXT NOT NULL,             -- WebhookOutcome enum value (open-set TEXT, no schema migration on add)
    installation_id   BIGINT,
    app_id            BIGINT,                    -- GHAPP_ORG_ID or GHAPP_PERSONAL_ID, populated from X-GitHub-Hook-Installation-Target-Id
    entity_type       entity_type_enum,
    entity_id         BIGINT,                    -- = installation.target_id = account.id (stable across renames)
    entity_name       TEXT,                      -- account login (mirrors jobs.entity_name semantics)
    payload           JSONB NOT NULL,            -- full webhook body, or synthesised dict for scheduler rows
    received_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_install_events_installation ON installation_events (installation_id, entity_id);
CREATE INDEX idx_install_events_entity       ON installation_events (entity_id, received_at DESC);
```

This DDL is the source of truth for the prod and staging schemas; the runtime no longer auto-applies it. The scheduler publishes a `NOTIFY {schema}_queue_event` (channel name varies by `prod` / `staging` schema) on every `jobs` insert; the scheduler `LISTEN`s on that channel as its wake signal.

Status transitions are forward-only: `pending -> running -> (completed | failed)`. All UPDATE queries enforce this with explicit WHERE clauses. A `failed` worker does not count toward supply in `get_pool_demand`, so `demand_match` automatically re-provisions a runner for the same pending job on the next loop iteration.

### Demand matching algorithm

```
demand  = COUNT(jobs WHERE entity_id = ? AND job_labels = ? AND status IN (pending, running))
supply  = COUNT(workers WHERE entity_id = ? AND job_labels = ? AND status IN (pending, running))
deficit = demand - supply
```

Demand and supply are matched by `(entity_id, job_labels)`. This prevents the bug where different label sets mapping to the same pool cause stuck workers (e.g., PyTorch `linux.riscv64.xlarge` vs `ubuntu-24.04-riscv` both map to `scw-em-rv1` but need separate runners with matching labels).

The scheduler iterates pending jobs in FIFO order. For each job:
1. If `demand <= supply` for its `(entity_id, job_labels)`: skip (demand already met)
2. If entity's total workers across all pools >= `max_workers`: skip
3. If no k8s node capacity for the pool's node selector: skip
4. Otherwise: provision a new runner

### Configuration

Per-entity configuration is defined in `ENTITY_CONFIG` in `constants.py`, keyed by entity ID (org ID or user ID):

| Field | Type | Description |
|-------|------|-------------|
| `max_workers` | int or None | Maximum concurrent workers across all pools. None = unlimited |
| `staging` | bool | If true, webhooks are proxied from prod to staging |

### HTTP routes

**ghfe:**

| Route | Method | Description |
|-------|--------|-------------|
| `/` | POST | Webhook endpoint for `workflow_job` events |
| `/health` | GET | Health check (returns `ok`) |
| `/usage` | GET | Human-readable view of per-pool jobs and workers |
| `/history` | GET | Job history sorted by status (pending, running, completed) then creation time |
| `/trace/entity/<entity_id>` | GET | Installation event log for an entity (requires bearer token) |
| `/trace/installation/<installation_id>` | GET | Resolves to `entity_id` then returns its event log |
| `/trace/job/<job_id>` | GET | Resolves to `entity_id` via `jobs.entity_id` then returns its event log |
| `/trace/payload/<event_id>` | GET | Full JSONB payload for one log row |

**scheduler:**

| Route | Method | Description |
|-------|--------|-------------|
| `/health` | GET | Health check (returns `ok`) |

### Key files

| File | Purpose |
|------|---------|
| `container/constants.py` | Environment configuration, entity config, image tags |
| `container/ghfe.py` | Flask webhook handler -- validates requests, writes to PostgreSQL |
| `container/scheduler.py` | Scheduler -- GH reconciliation, demand matching, cleanup, worker status sync |
| `container/k8s.py` | Kubernetes pod provisioning, deletion, capacity checks, failure info collection |
| `container/db.py` | PostgreSQL database operations |
| `container/github.py` | GitHub API functions (auth, runner groups, JIT config, job status) |
| `container/Dockerfile` | Docker image for the ghfe and scheduler containers |
| `container-go/` | Go reimplementation of ghfe and scheduler (see `container-go/CONTRACT.md`) |
| `scripts/trace_installation.py` | CLI client for the `/trace/*` endpoints — chronological table + diagnosis hints |

### Go cutover

`container-go/` ships a Go reimplementation deployed alongside the Python tree
as the `ghfe-go` and `scheduler-go` Scaleway functions. Cutover is gradual:

- **ghfe**: set `GO_GHFE_URL` on the Python ghfe function to the Go ghfe URL,
  then populate `GO_GHFE_ROUTING={"entities":[<entity_id>, ...]}` with the
  GitHub owner ids to forward. Only `workflow_job` webhooks are routed; the
  staging proxy at `container/ghfe.py:509` still runs first. Rollback is
  removing entries from `GO_GHFE_ROUTING`.
- **scheduler**: single deployment. Once staging has soaked on the Go
  scheduler, swap the prod `scheduler` function's image to
  `scheduler-prod-go`. Rollback is the inverse image swap.

See `container-go/CONTRACT.md` for the frozen behavioral surface the Go port
must preserve.

### Infrastructure

| Service | Product | Purpose |
|---------|---------|---------|
| ghfe | Scaleway Container | Receives webhooks, writes job state to PostgreSQL |
| scheduler | Scaleway Container | Demand matching, pod provisioning, cleanup, worker status sync |
| State store | Scaleway Managed Database | PostgreSQL: jobs + workers tables |
| Runner pods | Self-hosted k8s clusters | Ephemeral RISC-V runner pods |

Production and staging each have their own k8s cluster, provisioned via the `scripts/` tooling. Four containers are deployed total:
- `ghfe` + `scheduler` (production, `main` branch)
- `ghfe` + `scheduler` (staging, `staging` branch)

## Development

Create a python venv and install dev dependencies:
```bash
python3.12 -m venv .venv
source .venv/bin/activate
pip install --upgrade pip
pip install -r requirements-dev.txt
```

Run tests:
```bash
source .venv/bin/activate && PYTHONPATH=container python3 -m pytest
```

Tests mock PostgreSQL and Kubernetes -- no live services are required.

## Deployment

Deployment is handled automatically by GitHub Actions (`.github/workflows/release.yml`).

### How it works

1. **Push to `main`** automatically deploys to **production**: runs tests, builds the `ghfe` and `scheduler` Docker images, pushes them to Scaleway Container Registry, and deploys via `serverless deploy`.
2. **Push to `staging`** automatically deploys to **staging**: same pipeline but builds `:staging` tags. After deploy, it triggers a sample workflow to verify end-to-end.
3. **Manual deploy** via the Actions tab: click "Run workflow", select "staging" or "production".

### What to expect

- The CI pipeline runs tests first. If tests fail, deploy is skipped.
- Docker image build and push takes ~1 minute.
- `serverless deploy` takes ~1 minute to update the containers on Scaleway.
- Total pipeline time is ~2-3 minutes.

### GitHub Secrets

The following secrets must be configured in the repository settings (Settings > Secrets and variables > Actions):

| Secret | Description |
|---|---|
| `SCW_SECRET_KEY` | Scaleway API secret key (used for container registry login and serverless deploy) |
| `GHAPP_WEBHOOK_SECRET` | GitHub webhook HMAC secret (shared by both apps) |
| `GHAPP_ORG_PRIVATE_KEY` | GitHub App RSA private key for organizations (PEM format) |
| `GHAPP_PERSONAL_PRIVATE_KEY` | GitHub App RSA private key for personal accounts (PEM format) |
| `K8S_KUBECONFIG` | Kubeconfig for the Kubernetes cluster |
| `POSTGRES_URL` | PostgreSQL connection string (e.g. `postgresql://user:pass@<host>:5432/db?sslmode=require`) |
| `TRACE_API_SECRET` | Bearer token gating `/trace/*` endpoints (and the `trace_installation.py` script) |
| `RISCV_RUNNER_SAMPLE_ACCESS_TOKEN` | PAT for triggering sample workflow on staging deploy |

## Kubernetes cluster provisioning

Production and staging each have their own k8s cluster on Scaleway, managed via scripts in `scripts/`.

### Provisioning scripts

| Script | Purpose |
|--------|---------|
| `scripts/scw.py control-plane create` | Create a k8s control plane instance (Scaleway POP2-2C-8G) with containerd, kubeadm, Flannel CNI, RBAC, and device plugins |
| `scripts/scw.py runner {create,list,reinstall,setup,delete}` | Create, reinstall, list, or delete bare metal RISC-V runner nodes (Scaleway EM-RV1) |

### Creating a new cluster from scratch

```bash
cd scripts
python3 -m venv .venv-scripts
source .venv-scripts/bin/activate
pip3 install -r requirements.txt

# 1. Create the control plane
## Pass --staging for a staging control-plane
python scw.py control-plane create [--staging]

# 2. Add runner nodes (creates 3 bare metal RISC-V servers)
python scw.py runner create --control-plane <control-plane-name> 3

# 3. Update Github Secrets:
## Note the `--env main` for the prod environment, use `--env staging` for staging environment
ssh root@$(scw instance server list zone=fr-par-2 project-id=03a2e06e-e7c1-45a6-9f05-775d813c2e28 -o json | jq -r '.[] | select(.name == "riscv-runner-control-plane-0") | .public_ip.address') cat /etc/kubernetes/kubeconfig-gh-app.conf | gh secret set K8S_KUBECONFIG --repo riseproject-dev/riscv-runner-app --env prod
ssh root@$(scw instance server list zone=fr-par-2 project-id=03a2e06e-e7c1-45a6-9f05-775d813c2e28 -o json | jq -r '.[] | select(.name == "riscv-runner-control-plane-0") | .public_ip.address') cat /etc/kubernetes/kubeconfig-gh-deploy.conf | gh secret set K8S_KUBECONFIG --repo riseproject-dev/riscv-runner-images --env prod
ssh root@$(scw instance server list zone=fr-par-2 project-id=03a2e06e-e7c1-45a6-9f05-775d813c2e28 -o json | jq -r '.[] | select(.name == "<control-plane-name>") | .public_ip.address') cat /etc/kubernetes/kubeconfig-gh-deploy.conf | gh secret set K8S_KUBECONFIG --repo riseproject-dev/riscv-runner-device-plugin --env main
```

### Managing runners

```bash
# List runners tagged to a control plane
python scw.py runner list --control-plane <control-plane-name>

# Reinstall OS on a runner (wipes and re-joins the cluster)
python scw.py runner reinstall <runner-name>

# Reinstall OS on many runners (4 in parallel by default)
python scw.py runner reinstall riscv-runner-{6,25,27,30,33,34}

# Delete runners
python scw.py runner delete <runner-name>
```

### Kubernetes RBAC

RBAC is configured automatically by the control plane provisioning script. The key users:

- `gh-app` -- used by the scheduler container. Has edit access and node list permission for capacity checks.
- `gh-deploy` -- used by CI for kubeconfig stored in GitHub Secrets. Has cluster-admin access.

## Operations

### Cleanup terminated runner pods

Runner pods stay alive for 6 hours after reaching Succeeded/Failed so their logs and events can still be inspected via `kubectl`. The worker row in PostgreSQL is updated to `completed`/`failed` immediately on phase transition (not after the pod is deleted), so pool supply accounting is accurate throughout the grace period.

To manually clean up finished pods ahead of the grace period:

```bash
kubectl delete pods -l app=rise-riscv-runner --field-selector=status.phase!=Running,status.phase!=Pending,status.phase!=Unknown
```

### Inspect database state

```bash
# Connect to PostgreSQL (use connection string from POSTGRES_URL secret)
psql $POSTGRES_URL

# Check demand for a label set
SELECT COUNT(*) FROM staging.jobs WHERE entity_id = {entity_id} AND job_labels = '["ubuntu-24.04-riscv"]' AND (status = 'pending' OR status = 'running');

# Check supply for a label set
SELECT COUNT(*) FROM staging.workers WHERE entity_id = {entity_id} AND job_labels = '["ubuntu-24.04-riscv"]' AND (status = 'pending' OR status = 'running');

# View a job
SELECT * FROM staging.jobs WHERE job_id = {job_id};

# View recent failed workers with diagnostics
SELECT pod_name, entity_id, k8s_pool, failure_info FROM staging.workers WHERE status = 'failed' ORDER BY completed_at DESC LIMIT 10;

# Filter by failure reason (e.g. runners that never registered with GitHub)
SELECT pod_name, entity_name, completed_at FROM staging.workers WHERE status = 'failed' AND failure_info->>'reason' = 'runner_never_registered' ORDER BY completed_at DESC LIMIT 20;

# Other reasons: 'pod_failed' (k8s Failed phase), 'pod_stuck_pending' (never reached Running), 'runner_never_registered'
SELECT failure_info->>'reason' AS reason, COUNT(*) FROM staging.workers WHERE status = 'failed' AND completed_at > now() - interval '24 hours' GROUP BY 1;
```
