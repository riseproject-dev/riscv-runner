# container-go: external behavior contract

Frozen reference for the Go port. Source-of-truth citations are `container/*.py:LINE`.
DDL is in the root `README.md` ("Database schema").

## 1. HTTP surface

### ghfe (port 8080)

| Route | Method | Auth | Body / params | Success | Errors |
|---|---|---|---|---|---|
| `/health` | GET | — | — | 200 `ok` (text) | — |
| `/` | POST | HMAC-SHA256 + headers | webhook JSON | 200 text | 400 (bad headers/JSON), 401 (signature) |
| `/setup/org` | GET | — | `?installation_id=N` | 200 HTML | 400, 404, 502 |
| `/setup/personal` | GET | — | `?installation_id=N` | 200 HTML | 400, 404, 502 |
| `/trace/entity/<int>` | GET | `Authorization: Bearer $TRACE_API_SECRET` | path int | 200 JSON `{"events":[...]}` | 401, 404 |
| `/trace/installation/<int>` | GET | bearer | path int | 200 JSON `{"events":[...]}` | 401, 404 |
| `/trace/job/<int>` | GET | bearer | path int | 200 JSON `{"events":[...]}` | 401, 404 |
| `/trace/payload/<int>` | GET | bearer | path int (event id) | 200 JSON `{"payload":{...}}` | 401, 404 |

Access log is **opt-in per request**: `g.print_perf_log` defaults `False` (`ghfe.py:49`) and is set `True` only inside the staging-proxy branch (`ghfe.py:513`) and once a `workflow_job` has cleared signature, entity, and label checks (`ghfe.py:568`). The `after_request` hook (`ghfe.py:53-65`) emits `"%s %s -> %d in %.1fms"` only when both `g.print_perf_log` is true and the request isn't `GET /health`. Setup, trace, ping, ignored workflow_job events, and invalid-payload responses produce no access log line. The Go port must preserve this — health checks and discarded webhooks stay silent at INFO.

### scheduler (port 8080)

| Route | Method | Query | Response |
|---|---|---|---|
| `/health` | GET | — | 200 text `ok` |
| `/usage`, `/usage.json` | GET | — | HTML / JSON `{"jobs":[...],"workers":[...]}` |
| `/history`, `/history.json` | GET | `start`, `end`, `page=0`, `per_page=100` | HTML / JSON array |
| `/jobs`, `/jobs.json` | GET | same as `/history` | HTML / JSON array |
| `/workers`, `/workers.json` | GET | `start`, `end`, `page=0`, `per_page=100` | HTML / JSON array |

- `start`/`end` accept `YYYY-MM-DD` or `-Xd`. 400 on parse failure (`scheduler.py:774-787`, `821-834`).
- Paginated routes emit a GitHub-style `Link` header with `rel="first"|"prev"|"next"|"last"` (`scheduler.py:739-761`).
- Both binaries bind `0.0.0.0:8080` (`scheduler.py:888-889`).

## 2. Webhook contract

- Signature: `X-Hub-Signature-256: sha256=<hex(hmac_sha256(secret, raw_body))>`, timing-safe compare (`ghfe.py:70-103`).
- Required headers: `X-GitHub-Event`, `X-Hub-Signature-256`, `X-GitHub-Hook-Installation-Target-Id` (int, must match `GHAPP_ORG_ID` or `GHAPP_PERSONAL_ID`).
- Accepted events: `ping`, `installation`, `installation_repositories`, `installation_target`, `workflow_job` (`ghfe.py:427-626`).
- `workflow_job` accepted actions: `queued`, `in_progress`, `completed`. Everything else → `IGNORED_ACTION` (`ghfe.py:502-505`).
- Trimmed payload (`ghfe.py:106-177`, constant `_WORKFLOW_JOB_DROP_KEYS`):
  - `sender`, `repository.owner`: drop 11 `*_url` fields each.
  - `repository`: drop 31 `*_url` fields plus `license`.
  - `organization`: drop 8 `*_url` fields.
  - `workflow_job`: drop `url`, `run_url`, `check_run_url`, `steps[]`. **Preserve `workflow_job.html_url`.**

### Entity extraction by event type

| Event | entity_name | entity_id |
|---|---|---|
| `ping` | (none — only `app_id` logged) | — |
| `installation` | `installation.account.login` | `installation.target_id` |
| `installation_repositories` | `installation.account.login` | `installation.target_id` |
| `installation_target` | `account.login` | `account.id` |
| `workflow_job` | `repository.owner.login` | `repository.owner.id` (both orgs and users; see `ghfe.py:authorize_entity`) |

Every webhook delivery writes exactly one `installation_events` row carrying `source`, `event`, `outcome`, `payload` (full body), even on auth failure.

## 3. Label → pool/image resolution

From `match_labels_to_k8s` (`ghfe.py:226-255`). Returns `None` when no rule matches (caller emits `IGNORED_NO_LABEL`).

| Org match | Label predicate | Pool | Image |
|---|---|---|---|
| PyTorch, or `riseproject-dev` + repo in `{pytorch, executorch}` | `linux.riscv64.xlarge` or `linux.riscv64.2xlarge` in labels | `scw-em-rv1` | `RUNNER_IMAGE_UBUNTU_24_04` |
| same | `ubuntu-24.04-riscv` in labels | `scw-em-rv1` | `RUNNER_IMAGE_UBUNTU_24_04` |
| GGML, or `riseproject-dev` + repo in `{llama.cpp, llama.cpp-validation}` | labels == `["ubuntu-24.04-riscv"]` exactly | `cloudv10x-jupiter` | `RUNNER_IMAGE_UBUNTU_24_04` |
| any other | labels == `["ubuntu-24.04-riscv"]` exactly | `scw-em-rv1` | `RUNNER_IMAGE_UBUNTU_24_04` |
| any other | anything else (`ubuntu-26.04-riscv` etc.) | — | — (returns `None`) |

Constants: `PYTORCH_ORG_ID`, `GGML_ORG_ORG_ID`, `RISEPROJECT_DEV_ORG_ID` from `constants.py`.

## 4. Environment variables

Existing (unchanged):

| Var | Purpose |
|---|---|
| `PROD` | `"true"` → prod schema and routing branch active |
| `PROD_URL`, `STAGING_URL` | self-URLs; `STAGING_URL` is the proxy target |
| `POSTGRES_URL` | DSN |
| `K8S_KUBECONFIG` | YAML body (not a path); `yaml.safe_load` then `new_client_from_config_dict` (`k8s.py:24-26`) |
| `GHAPP_ORG_ID` | `2167633` (`constants.py:29`) |
| `GHAPP_ORG_PRIVATE_KEY` | RSA PEM |
| `GHAPP_PERSONAL_ID` | `3131217` (`constants.py:31`) |
| `GHAPP_PERSONAL_PRIVATE_KEY` | RSA PEM |
| `GHAPP_WEBHOOK_SECRET` | HMAC key |
| `TRACE_API_SECRET` | bearer for `/trace/*` |
| `LOGLEVEL` | default `INFO` |

New for the Go cutover:

| Var | Scope | Purpose |
|---|---|---|
| `GO_GHFE_URL` | Python ghfe | base URL of `ghfe-go`; required when `GO_GHFE_ROUTING` is non-empty |
| `GO_GHFE_ROUTING` | Python ghfe only | JSON `{"entities":[<name|id>, …]}`; empty / unset = route nothing |

## 5. Routing semantics

Routing applies to **`workflow_job` webhooks only** (same scope as the existing staging proxy at `ghfe.py:509-522`). Every other event type — `ping`, `installation`, `installation_repositories`, `installation_target`, plus anything unrecognised — is handled locally by Python ghfe. There is a single scheduler deployment at any time; it consumes every row in the DB regardless of which ghfe wrote it.

- **Staging proxy** (unchanged): inside the `workflow_job` branch, if `PROD` is true and the `(entity_id, repo_name)` pair is listed in `STAGING_ENTITIES`, the request is forwarded to `STAGING_URL`. Repoint `STAGING_URL` at the deployed Go ghfe staging URL to route staging traffic to Go.
- **`GO_GHFE_ROUTING`**: runs immediately after the staging proxy in the same `workflow_job` branch. If `GO_GHFE_URL` is set and `entity_id` (= `repository.owner.id`) is in the routing list, the raw body + headers (drop `Host`) are forwarded to `GO_GHFE_URL` with a 30s timeout; the response is returned verbatim. Otherwise Python handles the webhook as today.
- **Routing list parsing**: JSON `{"entities":[<int>, …]}`. Each entry is the GitHub owner id. Parsed once in `constants.py` into `frozenset[int]`. Empty / unset = nothing routed. No DB lookup.
- **Scheduler**: single deployment, no entity filter. Cutover Python → Go is one image swap on the existing function, performed independently of ghfe routing.

## 6. Database

DDL lives in `README.md` ("Database schema") — that section is the source of truth and the runtime no longer auto-applies it. Notes the Go code must honour:

- `search_path` is set to `prod` or `staging` on every borrowed connection.
- `LISTEN` channel: `{schema}_queue_event` (`db.py:847`). NOTIFY payload is `str(job_id)` (`db.py:314`).
- Status enum transitions are forward-only (`pending → running → completed|failed`); every `UPDATE` includes the status precondition.
- `jobs.k8s_pod` is `COALESCE`'d, never overwritten once set (`db.py:334, 366`).
- `failure_info` is JSONB with required key `version` (1 or 2). v2 is the only shape new code writes.

### Functions to reimplement (signatures from `db.py`)

Reads (`SELECT`): `get_pending_jobs`, `get_active_jobs`, `get_active_jobs_and_workers`, `get_workers_for_reconcile(terminal_lookback_seconds=3600)`, `get_pool_demand(entity_id, job_labels)`, `get_total_workers_for_entity(entity_id)`, `job_exists_for_pod(pod_name)`, `get_events_by_entity_id(entity_id)`, `get_entity_id_for_installation(installation_id)`, `get_entity_id_for_job(job_id)`.

Writes: `add_job`, `mark_job_running`, `mark_job_completed`, `mark_job_failed`, `add_worker` (raises `DuplicateRunnerNameException` on PK collision), `mark_worker_running`, `mark_worker_completed`, `mark_worker_failed`, `mark_worker_orphaned`, `add_installation_event` (returns `BIGSERIAL id`).

Listen: `wait_for_job(timeout)` — `select()` on the LISTEN conn, drains buffered NOTIFYs (`db.py:851-865`).

## 7. GitHub App auth (`github.py`)

- JWT (`github.py:36-43`): RS256, claims `iat=now()`, `exp=iat+600`, `iss=app_id`.
- Installation token: `POST https://api.github.com/app/installations/{installation_id}/access_tokens`, expect 201 (`github.py:46-74`).
- Cache: TTL = `60*59` (59 min), keyed by `(installation_id, app_id)`, `maxsize=1024`, LRU.
- JIT runner config:
  - Org: `POST /orgs/{name}/actions/runners/generate-jitconfig` (`github.py:141-163`).
  - Repo: `POST /repos/{full_name}/actions/runners/generate-jitconfig` (`github.py:166-188`).
  - Body: `{name, runner_group_id, labels, work_folder: "../../../work"}`. Returns `encoded_jit_config` (201).
- Runner group ensure (`github.py:103-138`): GET groups → if absent, POST `{name, visibility:"all", allows_public_repositories:true}`. Returns group id.
- List runners: `_paginated_get` on org-group or repo URLs (`github.py:212-221`). Walk `Link: rel="next"`.
- Delete runner: DELETE org or repo URL; treat 204 and 404 as success (`github.py:224-247`).
- Get job info: `GET /repos/{full_name}/actions/jobs/{job_id}`, return body on 200 (`github.py:250-268`).

## 8. Kubernetes pod manifest

`provision_runner` (`k8s.py:29-104`) produces a pod with:

| Field | Value |
|---|---|
| labels | `app=rise-riscv-runner`, `riseproject.dev/entity_id`, `riseproject.dev/entity_name`, `riseproject.dev/board` |
| nodeSelector | `riseproject.dev/board=<k8s_pool>` |
| activeDeadlineSeconds | `525600` |
| restartPolicy | `Never` |
| hostNetwork | `true` |
| containers | one (`name=runner`); no sidecar |
| securityContext.privileged | `true` |
| env | `RUNNER_WAIT_FOR_DOCKER_IN_SECONDS=60`, `RUNNER_JITCONFIG=<jit_config>` |
| resources.limits | `riseproject.com/runner=1`; **also** `ephemeral-storage=90Gi` iff `k8s_pool` starts with `scw-em-` (`k8s.py:46`) |
| volumes | two `emptyDir`: `docker-graph` → `/var/lib/docker`, `k0s` → `/var/lib/k0s` |
| namespace | `default` (also used by every other `k8s.py` op) |

Other k8s operations:

- `ListPods()` — `list_namespaced_pod(label_selector="app=rise-riscv-runner")`.
- `GetPodEvents(pod_name)` — `list_namespaced_event(field_selector=involvedObject.name=...)`, sorted by `last_timestamp || event_time || creation_timestamp`.
- `DeletePod(pod)` — `delete_namespaced_pod`; swallow 404.
- `KillPod(pod)` — patch `spec.activeDeadlineSeconds=1` (no delete; pod transitions to `Failed:DeadlineExceeded`).
- `CollectPodFailureInfo(pod, reason: FailureReason)` — returns `{version: 2, reason, pod_reason, pod_message, containers: {name: {exit_code, reason, message, logs}}, events: [{type, reason, message, count, first_seen, last_seen}], collect_error?}`.
- `AvailableSlots(pool)` — sum allocatable `riseproject.com/runner` over nodes matching `riseproject.dev/board=<pool>`, subtract count of `Pending|Running` runner pods on the same selector. Returns `Capacity{Total, Active, Available}`.

## 9. Reconciliation algorithm

`scheduler.py:429-462` — one tick, all five phases inside one `LOCK TABLE workers IN EXCLUSIVE MODE` critical section (`scheduler.py:865-867`).

1. **Orphan sweep** (`sync_workers_state` step 1, ref. line 440 → fn 239-244): workers in `pending|running` with no matching pod → `mark_worker_orphaned` (status becomes `completed`, no failure_info).
2. **Pod-phase sync** (line 443 → 247-266): map K8s pod `phase` to DB status — `Running` → `mark_worker_running`; `Succeeded` → `mark_worker_completed`; `Failed` → `mark_worker_failed` with collected `failure_info`.
3. **Health checks** (line 453 → 269-373): group active workers by GitHub runner scope, fetch runner list from GitHub, classify each pod against timeouts and kill via `KillPod` when exceeded.
4. **GitHub-side cleanup** (line 458 → 376-403): delete runners on GitHub for workers whose DB row is terminal or missing.
5. **Terminal-pod GC** (line 462 → 406-426): delete K8s pods in `Succeeded|Failed` once `finished_at` age exceeds `POD_DELETE_GRACE_SECONDS`.

Phases operate on a snapshot taken at the start of each phase — no cross-phase mutation reuse (invariant from `be1434c`).

### Timeouts (`constants.py:43-46`)

| Name | Value (s) | Applied in |
|---|---|---|
| `RUNNER_REGISTRATION_TIMEOUT_SECONDS` | 120 | `scheduler.py:328, 338, 364, 367` |
| `RUNNER_PENDING_TIMEOUT_SECONDS` | 600 | `scheduler.py:347` |
| `POD_PENDING_TIMEOUT_SECONDS` | 600 | `scheduler.py:315` |
| `POD_DELETE_GRACE_SECONDS` | 21600 | `scheduler.py:421` |

## 10. Demand match

`demand_match` (`scheduler.py:466-581`):

1. Pull all `pending` jobs (FIFO). For each distinct `k8s_pool`, fetch `AvailableSlots` **once**, then decrement locally per provision (`scheduler.py:485-489, 578`).
2. Skip pool when `available_slots <= 0` (note: `<= 0`, not `== 0`; concurrent runs can push below).
3. Per job: refetch row, skip if no longer `pending`.
4. Compute `(job_count, worker_count)` via `get_pool_demand(entity_id, job_labels)`. Skip if `job_count <= worker_count` ("demand met").
5. Check `entity_worker_count >= max_workers` (`ENTITY_CONFIG.get(entity_id, {"max_workers": 20})`). Skip if cap reached.
6. Generate runner name `{RUNNER_NAME_PREFIX}{rand9}` (`[a-z0-9]{9}`). Retry up to **5** times on `DuplicateRunnerNameException`.
7. On insert success, JIT runner config + `provision_runner`. On any failure, `add_worker` already created the row → `mark_worker_failed` with `failure_info.reason=pod_allocation_failure` (invariant from `9a9d611`).

## 11. Operational defaults

| Setting | Value | Source |
|---|---|---|
| Ports | `8080` (both binaries) | `scheduler.py:889` |
| Exit codes | `0` normal; `1` uncaught; `2` init failure (parity goal) | — |
| Scheduler poll interval | `15s` (`POLL_INTERVAL`), implemented via `db.wait_for_job(POLL_INTERVAL)` | `scheduler.py:27, 903` |
| Webhook proxy timeout | `30s` | `ghfe.py:519` |
| K8s namespace | `default` | `k8s.py` (all ops) |

## 12. Logging convention

`log/slog` text handler, level from `LOGLEVEL` (default `INFO`). Translation rule:

- Keep the English message text from Python verbatim.
- Move every positional arg to a slog attr; pick a stable attr name (`job_id`, `pod_name`, `installation_id`, `k8s_pool`, `worker_status`, `runner_status`, `reason`, `outcome`, `source`, `status_code`).
- Identity is always passed as a single `internal.Entity` attr under the key `entity`; the text handler expands it to `entity.type`, `entity.name`, `entity.id` (in that order) via `Entity.LogValue`. Don't pass the three fields separately.
- Errors trail with `"err", err`.

Examples:

| Python (`scheduler.py:512`) | Go |
|---|---|
| `logger.info("Demand met for entity=%s entity_id=%s entity_type=%s labels=%s, jobs_count=%d workers_count=%d", entity_name, entity_id, entity_type, labels, job_count, worker_count)` | `slog.Info("Demand met for entity", "entity", j.Entity(), "labels", labels, "jobs_count", j, "workers_count", w)` |
| `logger.warning("Runner name %s collision, regenerating", candidate)` (`scheduler.py:540`) | `slog.Warn("Runner name collision, regenerating", "runner_name", candidate)` |
| `logger.error("kill_pod failed for %s: %s", pod.metadata.name, e)` (`scheduler.py:222`) | `slog.Error("kill_pod failed", "pod_name", name, "err", err)` |
| `logger.info("Stored job ...")` (any place storing) | `slog.Info("Stored job", "job_id", id, …)` |

Access log line stays one record per request: `slog.Info("request", "method", m, "path", p, "status", s, "elapsed_ms", e)`. Health-check requests are not logged (parity with `ghfe.py:62`).

Every webhook outcome (`OK`, `IGNORED_*`, `INVALID_SIGNATURE`, `INVALID_PAYLOAD`, `JOB_NOT_FOUND`, `UNAUTHENTICATED`, `INTERNAL_ERROR`) emits exactly one `installation_events` row (invariant from `b909123`); slog records mirror those outcomes by attribute, not by message wording.

## 13. Hard-won invariants → tests

Each row becomes a named Go test in Phase B; numbers are git SHAs documenting the bug that introduced the invariant.

| SHA | Invariant | Go test |
|---|---|---|
| f264661 | `workflow_job` payloads strip ~70 `*_url`, `license`, `steps[]`; keep `workflow_job.html_url` | `TestTrimWorkflowJobPayload_DropsURLsLicenseSteps` |
| aae3ab3 | `ignored_no_label` events log only `workflow_job.{labels,html_url}` + `repository.full_name` | `TestIgnoredNoLabel_PayloadMinimized` |
| b909123 | every webhook + auth attempt writes one `installation_events` row with `source`, `outcome` | `TestInstallationEvents_RowPerOutcome` |
| 9de4c35 | pod spec has `hostNetwork=true` on every pool | `TestProvisionRunner_UsesHostNetwork` |
| 0028278 / 653a5ba | `/var/lib/k0s` and `/var/lib/docker` are `emptyDir` volumes | `TestProvisionRunner_EmptyDirVolumes` |
| 3286cf6 | `ephemeral-storage` requests/limits only on `scw-em-*` pools | `TestProvisionRunner_DiskLimitsOnlyOnScwEM` |
| 40476b8 | `available_slots <= 0` skip (not `== 0`); two concurrent loops can't push it negative | `TestDemandMatch_SkipsWhenSlotsNonPositive` |
| 4232868 | capacity fetched once per pool per iteration, decremented locally | `TestDemandMatch_CapacityFetchedOncePerPool` |
| 9a9d611 | failed `provision_runner` writes `failure_info.reason=pod_allocation_failure` | `TestProvisionRunner_FailureMarksWorker` |
| b9c25e0 | registered-but-offline runners past `RUNNER_REGISTRATION_TIMEOUT_SECONDS` are killed | `TestPhase3_OfflineRunnerPastTimeoutFails` |
| 83469ab | online runner idle past `RUNNER_PENDING_TIMEOUT_SECONDS` → `runner_idle` failure | `TestPhase3_OnlineIdleRunnerPastTimeoutFails` |
| be1434c | phases 1–5 operate on a snapshot taken at phase start | `TestSyncWorkersState_PhasesIsolated` |
| b081af0 | `/workers` renders both v1 and v2 `failure_info` shapes | `TestRenderWorker_RendersV1AndV2FailureInfo` |
| caf0e8a | `/workers` paginates 50/page with GitHub-style `Link` header | `TestWorkers_PaginationAndLinkHeader` |
| 5c5004f | single-container pod; no dind sidecar, no docker-certs volume, no `DOCKER_*` TLS env | `TestProvisionRunner_NoSidecar` |
| 1055cc8 | `/workers` JSON & HTML field name spelling matches existing UI consumers | `TestWorkers_FieldNames` |
| new | Python ghfe forwards `workflow_job` webhooks to `GO_GHFE_URL` iff `entity_id` is in `GO_GHFE_ROUTING`; non-`workflow_job` events are never routed; staging proxy runs before the routing check. | `test_ghfe_routes_workflow_job_when_entity_in_list` (pytest) |
