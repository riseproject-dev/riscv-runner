---
title: Container Images
parent: Architecture
nav_order: 4
---

# Container Images

Each runner pod uses a single unified container image: the GitHub Actions runner together with the full set of tools listed below. The image is built for `linux/riscv64` and stored in the Scaleway Container Registry.

**Source:** [riscv-runner-images](https://github.com/riseproject-dev/riscv-runner-images)

## Runner image

**Dockerfile:** [`runner/Dockerfile.ubuntu`](https://github.com/riseproject-dev/riscv-runner-images/blob/main/runner/Dockerfile.ubuntu)

The runner image is a multi-stage build based on Ubuntu (24.04 or 26.04). It contains:

### GitHub Actions Runner

The [GitHub Actions Runner for RISC-V](https://github.com/Cloud-V-10xE/github-runner-riscv), built with .NET 8. This is the process that registers with GitHub, receives the job, and executes workflow steps. The JIT runner config is passed in via the `RUNNER_JITCONFIG` environment variable; the worker obtains it from the GitHub API at pod creation time.

### Pre-installed software

| Category | Packages |
|----------|----------|
| **Languages** | Python 3.10–3.14 (including free-threaded variants), Node.js, Go, Rust, Java (Temurin 17/21/25), PHP, Ruby, Perl, Lua, R |
| **Compilers** | GCC, G++, Clang |
| **Build tools** | Make, CMake, Ninja, Autoconf, Automake, Libtool, Flex, Bison, Binutils, Gradle, Maven, Ant |
| **Container tooling** | Docker (CLI, Buildx, Compose, daemon), podman, buildah, skopeo, runc, kubectl |
| **VCS** | Git, Mercurial |
| **Networking** | curl, wget, openssh-client, netcat, dnsutils |
| **Compression** | bzip2, lz4, xz, zip, 7z, aria2 |
| **Packaging** | dpkg, rpm, fakeroot |
| **Utilities** | jq, shellcheck, tree, rsync, sudo, parallel |

The image aims to track the [official GitHub Actions Ubuntu runner images](https://github.com/actions/runner-images). Pinned versions live in [`versions-map.json`](https://github.com/riseproject-dev/riscv-runner-images/blob/main/versions-map.json). If your workflow needs a package that isn't in the image, [open an issue](https://github.com/riseproject-dev/riscv-runner-images/issues).

### User configuration

The image creates a non-root `runner` user with passwordless sudo. All workflow steps run inside this single container. The pod runs with `privileged: true` so the in-pod Docker daemon can program iptables and bridge devices.

## Build pipeline

**Workflow:** [`.github/workflows/release.yml`](https://github.com/riseproject-dev/riscv-runner-images/blob/main/.github/workflows/release.yml)

A single `build-runner` job builds the runner image, currently for Ubuntu 24.04 (26.04 is staged behind a matrix entry).

- **Trigger:** push to `main` or `staging`, daily schedule, or manual dispatch.
- **Platform:** `linux/riscv64`, built natively on `ubuntu-24.04-riscv` self-hosted RISC-V runners (no QEMU emulation in the build path).
- **Caching:** GitHub Actions Cache (`type=gha`) for Docker layer reuse. A concurrency group ensures only one build runs per branch.
- **Versioning:** [`scripts/update-versions.py`](https://github.com/riseproject-dev/riscv-runner-images/blob/main/scripts/update-versions.py) syncs pinned versions in `versions-map.json` from upstream releases, then a scheduled workflow opens a PR with the diff.

## Registry

Images are stored in the Scaleway Container Registry:

```
rg.fr-par.scw.cloud/funcscwriseriscvrunnerappqdvknz9s/riscv-runner
```

### Image tags

| Tag | Image | Source branch |
|-----|-------|---------------|
| `ubuntu-24.04-latest` | Runner image, Ubuntu 24.04 | `main` |
| `ubuntu-26.04-latest` | Runner image, Ubuntu 26.04 | `main` |

## Source files

| File | Role |
|------|------|
| [`runner/Dockerfile.ubuntu`](https://github.com/riseproject-dev/riscv-runner-images/blob/main/runner/Dockerfile.ubuntu) | Runner image (multi-stage; tools, language runtimes, container tooling) |
| [`runner/riscv-runner-entrypoint.sh`](https://github.com/riseproject-dev/riscv-runner-images/blob/main/runner/riscv-runner-entrypoint.sh) | PID-1 entrypoint, execs `run.sh --jitconfig "$RUNNER_JITCONFIG"` |
| [`versions-map.json`](https://github.com/riseproject-dev/riscv-runner-images/blob/main/versions-map.json) | Pinned versions for all bundled tools and runtimes |
| [`.github/workflows/release.yml`](https://github.com/riseproject-dev/riscv-runner-images/blob/main/.github/workflows/release.yml) | CI/CD pipeline |
