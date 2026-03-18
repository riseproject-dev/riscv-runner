---
title: Home
layout: home
nav_order: 1
---

# RISE RISC-V Runners

Run GitHub Actions workflows on real RISC-V hardware. No emulation, no cross-compilation — native execution on physical boards.

[RISE RISC-V Runners](https://github.com/apps/rise-risc-v-runners) is a managed self-hosted runner service. Install the [GitHub App](https://github.com/apps/rise-risc-v-runners), set `runs-on: ubuntu-24.04-riscv` in your workflow, and your jobs run on dedicated RISC-V nodes with full Docker support.

[RISE](https://riseproject.dev/) provides the service free of charge for any OSS project. Our goal is to enable the Software Ecosystem at large on RISC-V. This is one of our small contribution to this large effort.

We only supports installation of the [RISE RISC-V Runners GitHub App](https://github.com/apps/rise-risc-v-runners) on organizatons at the moment. Support for personal account is a work in progress. [Reach out](https://github.com/riseproject-dev/riscv-runner-app/issues) to let us know of your needs.
{: .warning }


## How it works

1. Your workflow triggers a `workflow_job` webhook
2. The webhook handler validates the request and records demand in Redis
3. A background worker provisions a Kubernetes pod on a matching RISC-V node
4. The pod registers as a just-in-time GitHub Actions runner and executes your job
5. On completion, the pod is cleaned up automatically

Runners are **ephemeral** — each job gets a fresh environment. Docker-in-Docker is available in every run.

## Quick start

```yaml
jobs:
  build:
    runs-on: ubuntu-24.04-riscv
    steps:
      - uses: actions/checkout@v4
      - run: uname -m   # prints riscv64
```

See [Install the GitHub App](docs/getting-started/install) and [Configure Your Workflow](docs/getting-started/configure) to get started.

## Available runners

| Label | OS | Hardware | Notes |
|-------|-----|----------|-------|
| `ubuntu-24.04-riscv` | Ubuntu 24.04 | Scaleway EM-RV1 | Standard runner |

See the full [Runner Labels Reference](docs/getting-started/labels) for hardware specs.

## Architecture

The system spans four repositories:

- **[riscv-runner-app](https://github.com/riseproject-dev/riscv-runner-app)** — GitHub App webhook handler and background worker
- **[riscv-runner-device-plugin](https://github.com/riseproject-dev/riscv-runner-device-plugin)** — Kubernetes device plugin and node labeller
- **[riscv-runner-images](https://github.com/riseproject-dev/riscv-runner-images)** — Runner and DinD container images
- **[riscv-runner-sample](https://github.com/riseproject-dev/riscv-runner-sample)** — Sample repository demonstrating usage

See the [Architecture Overview](docs/architecture/) for details.
