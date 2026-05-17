# container

Go implementation of the GitHub webhook frontend and scheduler. Two binaries:

- `cmd/ghfe` — GitHub webhook frontend (port 8080).
- `cmd/scheduler` — reconciliation loop + read-only dashboards (port 8080).

The root `README.md` is the source of truth for architecture, the database
schema, and the deployed HTTP routes.

## Layout

```
container/
  cmd/
    ghfe/         webhook + setup + trace + health
    scheduler/    reconciler (5 phases), demand_match, /usage, /history, /jobs, /workers
  internal/
    constants.go  Config, EntityConfigs, timeouts, image tags
    contract.go   shared types, WebhookOutcome enum, DB/GitHub/Kube interfaces
    db.go         pgx-backed DB implementation
    github.go     GitHub App auth + REST client
    k8s.go        client-go pod ops + CollectPodFailureInfo
    log.go        slog init
    testutil/     in-memory fakes shared by cmd/ tests
```

The Go module path is `github.com/riseproject-dev/riscv-runner-app/container`.

## Tests

```
go test -race ./...
```

`internal/k8s.go` is tested against `k8s.io/client-go/kubernetes/fake`.
`cmd/ghfe` and `cmd/scheduler` use the fakes in `internal/testutil/`.
