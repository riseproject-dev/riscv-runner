# RISE RISC-V Runners

[RISE RISC-V Runners](https://riseproject-dev.github.io/riscv-runner) is a managed GitHub Actions runner service that executes CI/CD workflows on real RISC-V hardware. Install the GitHub App on your [organization](https://github.com/apps/rise-risc-v-runners) or [personal account](https://github.com/apps/rise-risc-v-runners-personal), set `runs-on: ubuntu-24.04-riscv` in your workflow, and your jobs run on dedicated RISC-V nodes with full Docker support. No emulation, no cross-compilation.

[RISE](https://riseproject.dev/) provides the service free of charge for open source projects. Visit the [documentation site](https://riseproject-dev.github.io/riscv-runner) for full details.

## Quick start

1. Install the GitHub App: [organization](https://github.com/apps/rise-risc-v-runners) or [personal account](https://github.com/apps/rise-risc-v-runners-personal)
2. Use `runs-on: ubuntu-24.04-riscv` in your workflow:

```yaml
jobs:
  build:
    runs-on: ubuntu-24.04-riscv
    steps:
      - uses: actions/checkout@v4
      - run: uname -m   # prints riscv64
```

## Repository layout

This is a monorepo containing every component of the service.

```
.
├── website/         Jekyll documentation site, deployed to riscv-runners.riseproject.dev
├── container/       GitHub App webhook handler (ghfe) and scheduler, deployed as a container
├── images/          Runner container image (Ubuntu + CI tools), built on linux/riscv64
├── device-plugin/   Kubernetes device plugin and node labeller for RISC-V nodes
├── scripts/         Operator scripts and runner health checks
├── .github/         Workflows, dependabot, issue templates
└── LICENSE          MIT
```

Each component has its own CI workflow under `.github/workflows/`, scoped by `paths:` filter so an unrelated change does not trigger every pipeline.

## Components

### `website/` — documentation

Jekyll site using the [just-the-docs](https://just-the-docs.github.io/just-the-docs/) theme, deployed to GitHub Pages at [riscv-runners.riseproject.dev](https://riscv-runners.riseproject.dev).

```sh
cd website
bundle install
bundle exec jekyll serve   # http://localhost:4000
```

Style guidance for contributing to the docs lives in [`website/CLAUDE.md`](website/CLAUDE.md).

CI: [`.github/workflows/deploy-website.yml`](.github/workflows/deploy-website.yml). Triggered on changes to `website/**`. GitHub Pages source is "GitHub Actions".

### `container/` — GitHub App and scheduler

Two Go binaries deployed together as a single Scaleway Container Function:

- **`cmd/ghfe`** — GitHub App webhook handler. Receives `workflow_job` events, validates signatures, and enqueues jobs for the scheduler.
- **`cmd/scheduler`** — Reconciles GitHub job state with Kubernetes pod state. Polls the GitHub API to detect lost or stuck jobs and provisions ephemeral runner pods.

Architecture diagrams: [`website/docs/architecture/ghfe.md`](website/docs/architecture/ghfe.md), [`website/docs/architecture/scheduler.md`](website/docs/architecture/scheduler.md). Full reference, including the demand-matching algorithm, lifecycle state machines, database schema, HTTP routes, cluster provisioning, and ops runbooks, lives in [`container/README.md`](container/README.md).

```sh
cd container
go test ./...
go vet ./...
```

CI: [`.github/workflows/deploy-container.yml`](.github/workflows/deploy-container.yml). Triggered on changes to `container/**`. Pushes to staging on every `main` push, with a manual approval gate before prod.

### `images/` — runner container image

Ubuntu-based container image with the GitHub Actions runner and the standard CI toolchain (Java, Python, Node.js, Go, Rust, Docker CLI + daemon, podman, buildah, kubectl, and the usual coreutils). Built natively on `linux/riscv64`.

Pinned tool versions live in [`images/versions-map.json`](images/versions-map.json) and are kept in sync with upstream by [`scripts/update-versions.py`](scripts/update-versions.py), run weekly by [`.github/workflows/update-images-versions-map.yml`](.github/workflows/update-images-versions-map.yml).

The image aims to match the package set in [`actions/runner-images`](https://github.com/actions/runner-images). Report missing packages as an issue.

Full image variant table, the complete tool inventory, build details, and registry layout: [`images/README.md`](images/README.md).

```sh
docker buildx build \
  --platform linux/riscv64 \
  --file images/runner/Dockerfile.ubuntu \
  --build-arg OS_VERSION=24.04 \
  --tag riscv-runner:ubuntu-24.04 \
  images/runner
```

CI: [`.github/workflows/deploy-images.yml`](.github/workflows/deploy-images.yml). Triggered on changes to `images/**`, nightly at 06:00 UTC, and on manual dispatch. Builds run on self-hosted `ubuntu-24.04-riscv` runners.

### `device-plugin/` — Kubernetes integration

Two Go binaries deployed as DaemonSets on RISC-V worker nodes:

- **`cmd/k8s-device-plugin`** — Registers a `riseproject.com/runner=1` extended resource with the kubelet on each node. Pods request this resource to get exclusive scheduling, which gives the scheduler concurrency control per node.
- **`cmd/k8s-node-labeller`** — Detects the RISC-V SoC by reading `/sys/firmware/devicetree/base/compatible` and labels the node with `riseproject.dev/board=<board>`. Used as a node selector to pin builds to specific hardware.

Both binaries share `pkg/soc` for SoC detection but build into separate images. Board map lives in [`device-plugin/pkg/soc/detect.go`](device-plugin/pkg/soc/detect.go). Architecture, board-mapping table, build commands, deploy steps, and verification: [`device-plugin/README.md`](device-plugin/README.md).

```sh
cd device-plugin
make build              # both binaries for linux/riscv64
make container-build    # both images
```

DaemonSet manifests: [`device-plugin/k8s-ds-device-plugin.yaml`](device-plugin/k8s-ds-device-plugin.yaml), [`device-plugin/k8s-ds-node-labeller.yaml`](device-plugin/k8s-ds-node-labeller.yaml).

CI: [`.github/workflows/deploy-device-plugin.yml`](.github/workflows/deploy-device-plugin.yml). Triggered on changes to `device-plugin/**`. Builds, pushes, and rolls out both DaemonSets.

### `scripts/`

Operator and developer scripts:

| Script | Purpose |
|---|---|
| `update-versions.py` | Refreshes `images/versions-map.json` from the latest [`actions/runner-images`](https://github.com/actions/runner-images) release. Runs weekly via CI. |
| `scw.py`, `trace_installation.py` | Scaleway operations and install tracing. |
| `check-health.py` | Runner health probe used inside the runner image. |
| `requirements.txt` | Python deps for the scripts above. |

## Cross-component flow

```
  ┌──────────────┐  workflow_job   ┌─────────────────┐
  │ User repo on │ ──────────────▶ │ container/      │
  │ github.com   │   (webhook)     │   ghfe          │
  └──────────────┘                 │   scheduler     │
                                   └────────┬────────┘
                                            │  Pod with riseproject.com/runner=1
                                            ▼
                            ┌───────────────────────────────────┐
                            │ Kubernetes cluster on RISC-V      │
                            │                                   │
                            │  device-plugin   (DaemonSet)      │
                            │  node-labeller   (DaemonSet)      │
                            │  runner pod      (images/runner)  │
                            └───────────────────────────────────┘
```

See [`website/docs/architecture/`](website/docs/architecture/) for the full picture.

## License

[MIT](LICENSE).
