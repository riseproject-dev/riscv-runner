
> [2026-05-22 11:27 CET] We are currently experiencing an incident with [Scaleway seeing networking issues in their datacenter](https://status.scaleway.com/incidents/qkj49g6g3ykp). We are monitoring the situation and will update this website as soon as we have more information<br/>
> [2026-05-22 16:39 CET] It's partially back up but at reduced capacity. We are doing everything we can to get back to full capacity as soon as possible. We'll also reach out to anyone who was impacted to let them know.<br/>
> [2026-05-29 11:32 CET] We are still operating on lower capacity than usual. It leads to queuing on peak demand. We are working with Scaleway to understand and fix the issue.

# RISE RISC-V Runners

[RISE RISC-V Runners](https://riscv-runners.riseproject.dev/) is a managed GitHub Actions runner service that executes CI/CD workflows on real RISC-V hardware. Install the GitHub App on your [organization](https://github.com/apps/rise-risc-v-runners) or [personal account](https://github.com/apps/rise-risc-v-runners-personal), set `runs-on: ubuntu-24.04-riscv` in your workflow, and your jobs run on dedicated RISC-V nodes with full Docker support. No emulation, no cross-compilation.

[RISE](https://riseproject.dev/) provides the service free of charge for open source projects. The [documentation site](https://riscv-runners.riseproject.dev/) is the source of truth for users and operators.

## Quick start

1. Install the GitHub App: [organization](https://github.com/apps/rise-risc-v-runners) or [personal account](https://github.com/apps/rise-risc-v-runners-personal).
2. Use `runs-on: ubuntu-24.04-riscv` in your workflow:

```yaml
jobs:
  build:
    runs-on: ubuntu-24.04-riscv
    steps:
      - uses: actions/checkout@v6
      - run: uname -m   # prints riscv64
```

See the [Getting Started guide](https://riscv-runners.riseproject.dev/docs/getting-started/) on the website for the full setup.

## Repository layout

This is a monorepo containing every component of the service.

| Path | Component | Component README |
|------|-----------|------------------|
| [`website/`](website) | Jekyll documentation site, deployed to [riscv-runners.riseproject.dev](https://riscv-runners.riseproject.dev/) | [`website/CLAUDE.md`](website/CLAUDE.md) |
| [`container/`](container) | GitHub App webhook handler (`ghfe`) and demand-matching scheduler (Go) | [`container/README.md`](container/README.md) |
| [`images/`](images) | Runner container image (Ubuntu + the standard CI tool set), built on `linux/riscv64` | [`images/README.md`](images/README.md) |
| [`device-plugin/`](device-plugin) | Kubernetes device plugin and node labeller for RISC-V nodes (Go) | [`device-plugin/README.md`](device-plugin/README.md) |
| [`scripts/`](scripts) | Operator scripts (Scaleway provisioning, runner health probe, install-trace CLI, version sync) | — |
| [`.github/`](.github) | Workflows, dependabot config | — |
| [`LICENSE`](LICENSE) | MIT | — |

Architecture, the database schema, ops runbooks, and the full user guide live on the [website](https://riscv-runners.riseproject.dev/). Component READMEs are kept short and only cover what a contributor working in that directory needs (layout, dev/build/test commands, dependencies).

## CI/CD

Each component has its own workflow under `.github/workflows/`, scoped by `paths:` filter so an unrelated change does not trigger every pipeline.

| Workflow | Triggers | What it does |
|----------|----------|--------------|
| [`deploy-website.yml`](.github/workflows/deploy-website.yml) | `website/**`, manual | Build and deploy the Jekyll site to GitHub Pages |
| [`deploy-container.yml`](.github/workflows/deploy-container.yml) | `container/**`, manual | Test, build, push, deploy `ghfe`+`scheduler` to Scaleway (staging then prod with approval) |
| [`deploy-images.yml`](.github/workflows/deploy-images.yml) | `images/**`, daily at 06:00 UTC, manual | Build the runner image on RISC-V hardware, push, retag for staging then prod |
| [`update-images-versions-map.yml`](.github/workflows/update-images-versions-map.yml) | weekly cron, manual | Run `scripts/update-versions.py` and open a draft PR if pinned versions changed |
| [`deploy-device-plugin.yml`](.github/workflows/deploy-device-plugin.yml) | `device-plugin/**`, manual | Build both DaemonSet images, push, roll out across the cluster |

## Components in 30 seconds

### `container/`

Two Go binaries deployed together as Scaleway Container Functions: `ghfe` (webhook handler, no GitHub API or k8s calls, just signature validation, label routing, and PostgreSQL writes) and `scheduler` (5-phase reconciler under `LOCK TABLE workers`, demand-matching, pod provisioning, plus read-only HTML dashboards at `/usage`, `/history`, `/workers`). PostgreSQL is the state store, woken by `LISTEN/NOTIFY` to avoid polling.

Full reference: [Architecture — Webhook Handler](https://riscv-runners.riseproject.dev/docs/architecture/ghfe) and [Scheduler](https://riscv-runners.riseproject.dev/docs/architecture/scheduler).

### `images/`

Single unified runner image based on Ubuntu, built for `linux/riscv64`. Includes the [GitHub Actions Runner for RISC-V](https://github.com/Cloud-V-10xE/github-runner-riscv) (.NET 8), the standard set of language runtimes and CI tools, and a Docker-in-Docker daemon. Pinned tool versions live in [`images/versions-map.json`](images/versions-map.json) and are weekly-refreshed against [`actions/runner-images`](https://github.com/actions/runner-images).

Full reference: [Architecture — Container Images](https://riscv-runners.riseproject.dev/docs/architecture/images).

### `device-plugin/`

Two Go binaries that run as DaemonSets on every RISC-V worker node: `k8s-device-plugin` registers a single `riseproject.com/runner` resource per node so the Kubernetes scheduler enforces one runner pod per node, and `k8s-node-labeller` reads the SoC from `/sys/firmware/devicetree/base/compatible` and applies a `riseproject.dev/board=<board>` label for hardware-specific scheduling.

Full reference: [Architecture — Kubernetes Infrastructure](https://riscv-runners.riseproject.dev/docs/architecture/kubernetes).

### End-to-end flow

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

## Operations

The website has an [Operations](https://riscv-runners.riseproject.dev/docs/operations/) section covering cluster provisioning (`scripts/scw.py`) and day-to-day runbooks (inspecting database state, forcing pod cleanup, debugging installations via the trace endpoints, rotating secrets).

## Contributing

- Pick the right component README for the directory you are working in.
- Style guide for the documentation site is in [`website/CLAUDE.md`](website/CLAUDE.md). Engineer-to-engineer tone, no superlatives, no em dashes, no corporate filler.
- Issues: [riseproject-dev/riscv-runner](https://github.com/riseproject-dev/riscv-runner/issues).

## License

[MIT](LICENSE). All source files carry an `SPDX-License-Identifier: MIT` header.
