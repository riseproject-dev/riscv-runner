# container-go

Go reimplementation of `container/`. Two binaries:

- `cmd/ghfe` — GitHub webhook frontend (port 8080).
- `cmd/scheduler` — reconciliation loop + read-only dashboards (port 8080).

`CONTRACT.md` is the frozen behavior reference. The root `README.md` is the
source of truth for the database schema.

## Routing during the cutover

`container/ghfe.py` (Python) is the entry point GitHub sends webhooks to.
When `GO_GHFE_ROUTING` lists an entity (by name or id) and `GO_GHFE_URL` is
set, Python forwards that entity's webhooks here. Go ghfe processes them
normally — it does not read either of those env vars.

The scheduler is a single deployment; cutover from Python → Go is one
image swap. Worker rows in the DB are scheduler-agnostic.

## Layout

```
container-go/
  cmd/
    ghfe/         webhook + setup + trace + health
    scheduler/    reconciler (5 phases), demand_match, /usage, /history, /workers
  internal/
    constants.go  Config, ENTITY_CONFIG, timeouts, image tags
    contract.go   shared types + DB/GitHub/Kube interfaces
    db.go         pgx-backed DB implementation
    github.go     GitHub App auth + REST client
    k8s.go        client-go pod ops + CollectPodFailureInfo
    log.go        slog init
    testutil/     in-memory fakes shared by cmd/ tests
```

## Tests

```
go test -race ./...
```

`internal/k8s.go` is tested against `k8s.io/client-go/kubernetes/fake`.
`cmd/ghfe` and `cmd/scheduler` use the fakes in `internal/testutil/`.
