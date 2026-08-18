# control-plane

GitHub App webhook handler (`ghfe`) and demand-matching scheduler. Two Go binaries deployed together as Scaleway Container Functions.

- `cmd/ghfe` — receives `workflow_job` webhooks, writes job state to PostgreSQL, serves `/setup/*` and `/trace/*`. No GitHub API or Kubernetes calls.
- `cmd/scheduler` — reads job state, provisions runner pods on Kubernetes, reconciles with GitHub, cleans up completed pods. Serves `/usage`, `/history`, `/jobs`, `/workers`.

For architecture, sequence diagrams, the database schema, the demand-matching algorithm, the installation event log, and ops runbooks, see the [website](https://riscv-runners.riseproject.dev/). This README covers only what a contributor working in `control-plane/` needs to know.

Go module: `github.com/riseproject-dev/riscv-runner/control-plane`.

## Layout

```
control-plane/
├── cmd/
│   ├── ghfe/                webhook handler, /setup/*, /trace/*, health
│   └── scheduler/           reconciler (5 phases), demand match, /usage, /history, /jobs, /workers
├── internal/
│   ├── constants.go         config, EntityConfigs, timeouts, image tags
│   ├── contract.go          shared types, WebhookOutcome enum, DB/GitHub/Kube interfaces
│   ├── db.go                pgx-backed PostgreSQL operations, WithWorkerLock
│   ├── github.go            GitHub App auth + REST client
│   ├── k8s.go               client-go pod operations + CollectPodFailureInfo
│   ├── log.go               slog initialisation
│   └── testutil/            in-memory fakes shared by cmd/ tests
├── Dockerfile               multi-stage build producing the ghfe and scheduler images
└── serverless.yml           Scaleway Serverless deployment manifest
```

External dependencies: `github.com/golang-jwt/jwt/v5`, `github.com/jackc/pgx/v5`, `k8s.io/client-go`.

Test infrastructure lives in `internal/testutil/`: in-memory fakes for the `DB`, `GitHubClient`, and `KubeClient` interfaces declared in `internal/contract.go`. Tests in `cmd/ghfe` and `cmd/scheduler` use those fakes; no live PostgreSQL, GitHub API, or Kubernetes cluster is required.

## Develop

From `control-plane/`:

```sh
go vet ./...
gofmt -l .              # exits 0 with no output if everything is formatted
go test -race ./...
```

CI mirrors this in [`.github/workflows/deploy-control-plane.yml`](../.github/workflows/deploy-control-plane.yml) before building images.

## Build a local image

```sh
docker buildx build \
  --target ghfe \
  --tag ghfe:local \
  -f Dockerfile .
docker buildx build \
  --target scheduler \
  --tag scheduler:local \
  -f Dockerfile .
```

The `Dockerfile` produces two binaries on top of `gcr.io/distroless/base-debian13`. CGO is disabled. The Scaleway deploy targets `linux/amd64`.

## Configuration

`ghfe` and `scheduler` share the same `Config` struct (loaded by `internal.LoadConfigFromEnv`). Environment variables consumed at runtime:

| Variable | Required | Purpose |
|---|:-:|---|
| `PROD` | yes | `true` selects the `prod` schema and `*-latest` image tags; otherwise `staging`/`*-staging` |
| `PROD_URL`, `STAGING_URL` | yes (ghfe) | URLs of the prod/staging ghfe; used by the staging proxy |
| `POSTGRES_URL` | yes | `postgresql://...?sslmode=require` |
| `K8S_KUBECONFIG` | yes (scheduler) | Kubeconfig as a string |
| `GHAPP_WEBHOOK_SECRET` | yes | Shared HMAC secret |
| `GHAPP_ORG_PRIVATE_KEY` | yes | RSA private key for the org App (PEM) |
| `GHAPP_PERSONAL_PRIVATE_KEY` | yes | RSA private key for the personal App (PEM) |
| `TRACE_API_SECRET` | yes (ghfe) | Bearer token gating `/trace/*` |
| `LOGLEVEL` | no | `DEBUG`/`INFO`/`WARN`/`ERROR` (default `INFO`) |

Hardcoded constants (App IDs, timeouts, `EntityConfigs` overrides) live in [`internal/constants.go`](internal/constants.go).

## Deploy

Deploy runs through [`.github/workflows/deploy-control-plane.yml`](../.github/workflows/deploy-control-plane.yml). Push to `main` pushes both images to the Scaleway registry, runs `npx serverless deploy --stage=staging`, triggers the staging sample workflow as a smoke test, then waits for an environment-gated approval before deploying to prod. Manual deploys are available via `workflow_dispatch`.

The Scaleway deployment spec is [`serverless.yml`](serverless.yml). The `scheduler` container is pinned to `minScale=1 maxScale=1` so the `LOCK TABLE workers` invariant is trivially preserved.

## Further reading

- [Architecture — Webhook Handler](https://riscv-runners.riseproject.dev/docs/architecture/ghfe)
- [Architecture — Scheduler](https://riscv-runners.riseproject.dev/docs/architecture/scheduler)
- [Architecture — Database Schema](https://riscv-runners.riseproject.dev/docs/architecture/database)
- [Architecture — Installation Event Log](https://riscv-runners.riseproject.dev/docs/architecture/installation-events)
- [Operations — Runbooks](https://riscv-runners.riseproject.dev/docs/operations/runbooks)
- [Operations — Cluster Provisioning](https://riscv-runners.riseproject.dev/docs/operations/cluster-provisioning)
