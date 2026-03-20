---
title: Configure Your Workflow
parent: Getting Started
nav_order: 2
---

# Configure Your Workflow

Once the RISE RISC-V Runners GitHub App is installed ([organization](https://github.com/apps/rise-risc-v-runners) or [personal](https://github.com/apps/rise-risc-v-runners-personal)), use RISC-V runners by setting the `runs-on` label in your workflow file.

## Minimal workflow

```yaml
name: RISC-V CI
on: [push, pull_request]

jobs:
  build:
    runs-on: ubuntu-24.04-riscv
    steps:
      - uses: actions/checkout@v4
      - run: uname -m        # riscv64
      - run: gcc --version    # native riscv64-linux-gnu-gcc
      - run: make
```

Save this as `.github/workflows/riscv.yml` in your repository.

## Available labels

| Label | Description |
|-------|-------------|
| `ubuntu-24.04-riscv` | Standard RISC-V runner, Ubuntu 24.04 |
| `ubuntu-26.04-riscv` | Standard RISC-V runner, Ubuntu 26.04 |

See the [Runner Labels Reference](labels) for full hardware specs.

## Docker support

Every runner includes Docker-in-Docker. Build and run containers natively on RISC-V:

```yaml
jobs:
  docker:
    runs-on: ubuntu-24.04-riscv
    steps:
      - uses: actions/checkout@v4
      - run: docker build -t myapp .
      - run: docker run myapp
```

Docker Buildx and Docker Compose are also available.

## Pre-installed tools

The runner image includes:

- **Languages**: Python 3.10–3.14, GCC, G++
- **Build tools**: Make, Autoconf, Automake, CMake, Libtool
- **Docker**: Docker CLI, Buildx, Compose
- **Utilities**: Git, curl, wget, jq, shellcheck, rsync
- **Package managers**: dpkg, rpm, pip

See [riseproject-dev/riscv-runner-images:runner/Dockerfile.ubuntu](https://github.com/riseproject-dev/riscv-runner-images/blob/main/runner/Dockerfile.ubuntu) for the complete list

## Runner lifecycle

Runners are **ephemeral**. Each job gets a clean environment:

1. A fresh pod is provisioned when your job is queued
2. The pod registers as a just-in-time runner with GitHub
3. Your job executes
4. The pod is deleted after completion

There is no persistent state between jobs.

## Sample repository

See [riscv-runner-sample](https://github.com/riseproject-dev/riscv-runner-sample) for a working example.
