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
      - uses: actions/checkout@v6
      - run: uname -m        # riscv64
      - run: gcc --version    # native riscv64-linux-gnu-gcc
      - run: make
```

Save this as `.github/workflows/riscv.yml` in your repository.

## Available labels

| Label | Description |
|-------|-------------|
| `ubuntu-24.04-riscv` | Standard RISC-V runner, Ubuntu 24.04 |

`ubuntu-26.04-riscv` is not yet routable: the webhook handler only matches `ubuntu-24.04-riscv` today, and the image build matrix only produces the 24.04 image. The label will be re-enabled when RVA23 hardware lands. See the [Runner Labels Reference](labels) for full hardware specs.

## Pre-installed tools

The runner image aims to mirror the standard GitHub Actions Ubuntu runner. Highlights:

- **Languages and runtimes:** Python 3.10–3.14 (including free-threaded variants), Node.js, Go, Rust, Java (Temurin 17/21/25), PHP, Ruby, Perl, Lua, R.
- **Compilers:** GCC, G++, Clang.
- **Build tools:** Make, CMake, Ninja, Autoconf, Automake, Libtool, Gradle, Maven, Ant.
- **Container tooling:** Docker (CLI + daemon, Buildx, Compose), podman, buildah, skopeo, runc, kubectl.
- **Utilities:** Git, curl, wget, jq, shellcheck, rsync.

```yaml
jobs:
  docker:
    runs-on: ubuntu-24.04-riscv
    steps:
      - uses: actions/checkout@v6
      - run: docker build -t myapp .
      - run: docker run myapp
```

See [the runner Dockerfile](https://github.com/riseproject-dev/riscv-runner/blob/main/images/runner/Dockerfile.ubuntu) for the complete list and [`versions-map.json`](https://github.com/riseproject-dev/riscv-runner/blob/main/images/versions-map.json) for pinned versions.

## Runner lifecycle

Runners are **ephemeral**. Each job gets a clean environment:

1. A fresh pod is provisioned when your job is queued
2. The pod registers as a just-in-time runner with GitHub
3. Your job executes
4. The pod is deleted after completion

There is no persistent state between jobs.

## Sample repository

See [riscv-runner-sample](https://github.com/riseproject-dev/riscv-runner-sample) for a working example.
