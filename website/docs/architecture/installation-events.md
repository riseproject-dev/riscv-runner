---
title: Installation Event Log
parent: Architecture
nav_order: 6
---

# Installation Event Log

When a scheduler call to GitHub returns 404, the matching job is marked `failed` with `installation not found`, but the cause is invisible. The user may have uninstalled the app, suspended it, removed access to a specific repo, renamed their org/user account, or installed the wrong app variant on the wrong account type. Without captured history, users cannot be told why their jobs stopped getting picked up.

`installation_events` is the append-only log that lets the service answer the question after the fact. See [Database schema](database) for the table DDL.

## What gets logged

- **Every webhook delivery `ghfe` receives.** Includes `installation`, `installation_repositories`, `installation_target`, `workflow_job`, `ping`, plus a row for any unhandled `X-GitHub-Event` with `outcome=unhandled_event`.
- **Every scheduler GitHub-auth failure.** `auth_attempt.404` (installation gone) or `auth_attempt.other_error` (everything else). Successful auths are not logged: the underlying `AuthenticateApp` is TTL-cached for 59 minutes, so success is the hot path and would drown the log.

The `WebhookOutcome` type in [`container/internal/contract.go`](https://github.com/riseproject-dev/riscv-runner/blob/main/container/internal/contract.go) is the canonical list of `outcome` values. The column itself is `TEXT`, so new outcomes do not require schema migrations.

`entity_id` is the GitHub `account.id`, which is stable across renames and reinstalls. Uninstalling and reinstalling the app produces a new `installation_id` but keeps the same `entity_id`.

## State reconstruction

The log is the source of truth for an entity's installation history. To answer "what did installation X look like at time T?" the trace tool fetches every event for that entity and folds the payloads in `received_at` order:

| Event | State change |
|---|---|
| `installation.created` | initial repo set, `app_id`, `repository_selection`, `suspended=false` |
| `installation_repositories.added` | `repos := repos ∪ payload.repositories_added` |
| `installation_repositories.removed` | `repos := repos \ payload.repositories_removed` |
| `installation.suspend` / `installation.unsuspend` | flip `suspended` |
| `installation.deleted` | terminal: `installed=false`, `repos=∅` |
| `installation_target.renamed` | `entity_name := payload.account.login` |
| `auth_attempt.404` | the scheduler's most recent failure, with the `app_id` it tried |

Common diagnoses fall out of that fold:

| Cause | Signal |
|---|---|
| User uninstalled between job submission and reconcile | `installation.deleted` preceding the `auth_attempt.404` |
| Admin suspended the installation | `installation.suspend` with no later `unsuspend` |
| Admin removed access to a specific repo | `installation_repositories.removed` mentioning the failing repo |
| Account renamed; cached `entity_name` is stale | `installation_target.renamed` |
| JWT signed by the wrong app for this installation | `auth_attempt.404` row's `app_id` differs from `installation.created.app_id` |
| `repository_selection=selected` and the repo is not selected | `installation.created` shows `selected` and `installation_repositories.added` never adds the repo |

## Trace endpoints

[`ghfe`](ghfe) exposes the log via four read-only endpoints, all gated by `Authorization: Bearer $TRACE_API_SECRET`.

| Route | Returns |
|---|---|
| `GET /trace/entity/{entity_id}` | All events for one entity |
| `GET /trace/installation/{installation_id}` | Resolves to `entity_id`, then returns the same view |
| `GET /trace/job/{job_id}` | Resolves to `entity_id` via `jobs.entity_id`, then returns the same view |
| `GET /trace/payload/{event_id}` | Full JSONB payload for one row |

The list endpoints intentionally do not return the `payload` field: payloads can be tens of KB each and most rows are reviewed at a glance. For `workflow_job.*` rows the response includes `job_id` and `repo_full_name` extracted in SQL so the timeline stays readable. `/trace/payload/<id>` returns the full body for any individual row.

Authentication is a simple bearer-token check: it gates casual access but is not designed as a security boundary.

## CLI client

[`scripts/trace_installation.py`](https://github.com/riseproject-dev/riscv-runner/blob/main/scripts/trace_installation.py) is a thin client over the trace endpoints with a chronological table renderer and rule-based diagnosis hints. It takes one of `--installation-id`, `--entity-id`, `--entity-name`, or `--job-id`. The `--entity-name` resolution shells out to `gh api /users/<login>` (falling back to `/orgs/<login>`), so it requires `gh auth login`.

`PROD_URL` is hard-coded in the script. `TRACE_API_SECRET` comes from the environment.

```sh
TRACE_API_SECRET=... python3 scripts/trace_installation.py --job-id 123456789
TRACE_API_SECRET=... python3 scripts/trace_installation.py --entity-name riseproject-dev
```

## Operational notes

- The log table has no UNIQUE constraint on payload, so duplicate rows from redelivered webhooks are acceptable. Trace endpoints can dedupe by `delivery_id` from the JSONB payload when needed.
- The webhook handler writes the `jobs` side-effect and the `installation_events` row in **separate transactions**, so a log-write failure does not lose the job state. See [Database schema § Transactional model](database#transactional-model).
- The scheduler's `ghAuthenticate` wrapper ([`container/cmd/scheduler/gh_auth.go`](https://github.com/riseproject-dev/riscv-runner/blob/main/container/cmd/scheduler/gh_auth.go)) records failures only; the underlying `AuthenticateApp` is TTL-cached and would otherwise drown the log.
