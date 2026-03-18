---
title: Container Images
parent: Architecture
nav_order: 4
---

# Container Images

The runner environment consists of two container images: the **runner image** (main container) and the **DinD sidecar** (Docker-in-Docker). Both are built for `linux/riscv64` and stored in the Scaleway Container Registry.

**Source:** [riscv-runner-images](https://github.com/riseproject-dev/riscv-runner-images)

## Runner image

**Dockerfile:** [`runner/Dockerfile.ubuntu`](https://github.com/riseproject-dev/riscv-runner-images/blob/main/runner/Dockerfile.ubuntu)

The runner image is a multi-stage build based on Ubuntu (24.04 or 26.04). It includes:

### GitHub Actions Runner

The [GitHub Actions Runner](https://github.com/actions/runner) (v2.331.0) for RISC-V, built with .NET 8. This is the process that registers with GitHub, receives the job, and executes workflow steps.

The runner starts with:
```
./run.sh --jitconfig {config}
```

The JIT config is a base64-encoded token obtained from the GitHub API by the worker at pod creation time.

### Pre-installed software

| Category | Packages |
|----------|----------|
| **Python** | 3.10, 3.11, 3.12 (default), 3.13, 3.14 — built from source with shared libraries |
| **Compilers** | GCC, G++ |
| **Build tools** | Make, Autoconf, Automake, Libtool, Flex, Bison, Binutils |
| **Docker** | Docker CLI, Buildx, Compose |
| **VCS** | Git, Mercurial |
| **Networking** | curl, wget, openssh-client, netcat, dnsutils |
| **Compression** | bzip2, lz4, xz, zip, 7z, aria2 |
| **Packaging** | dpkg, rpm, fakeroot |
| **Utilities** | jq, shellcheck, tree, rsync, sudo, parallel |

### User configuration

The image creates a non-root `runner` user with passwordless sudo access. All jobs run as this user.

## DinD sidecar

**Dockerfile:** [`dind/Dockerfile`](https://github.com/riseproject-dev/riscv-runner-images/blob/main/dind/Dockerfile)

A minimal Debian-based image that runs the Docker daemon. It runs as an init container in the runner pod, providing Docker-in-Docker support.

### TLS setup

The DinD entrypoint script (`dockerd-entrypoint.sh`) automatically generates TLS certificates:

- Creates a CA and signs server + client certificates
- Certificates are written to a shared `emptyDir` volume
- The runner container connects to the Docker daemon over TLS on port 2376
- Certificates are valid for 825 days and regenerated on each pod startup

### Exposed ports

| Port | Protocol | Purpose |
|------|----------|---------|
| 2375 | TCP | Unencrypted Docker API (disabled when TLS is configured) |
| 2376 | TCP | TLS-encrypted Docker API |

## Build pipeline

**Workflow:** [`.github/workflows/release.yml`](https://github.com/riseproject-dev/riscv-runner-images/blob/main/.github/workflows/release.yml)

Images are built and pushed automatically:

- **Trigger:** Daily at 06:00 UTC, on push to `main`, or manual dispatch
- **Platform:** `linux/riscv64`
- **Cross-compilation:** Ubuntu 24.04 images build on native RISC-V runners. Ubuntu 26.04 images use QEMU emulation on x86 runners (requires RVA23 CPU).
- **Caching:** GitHub Actions Cache for Docker layer caching

## Registry

Images are stored in the Scaleway Container Registry:

```
rg.fr-par.scw.cloud/funcscwriseriscvrunnerappqdvknz9s/riscv-runner
```

### Image tags

| Tag | Image |
|-----|-------|
| `ubuntu-24.04-2.331.0` | Runner image, Ubuntu 24.04 |
| `ubuntu-26.04-2.331.0` | Runner image, Ubuntu 26.04 |
| `dind` | Docker-in-Docker sidecar |

## Source files

| File | Role |
|------|------|
| [`runner/Dockerfile.ubuntu`](https://github.com/riseproject-dev/riscv-runner-images/blob/main/runner/Dockerfile.ubuntu) | Runner image (multi-stage, Python builds, tools) |
| [`dind/Dockerfile`](https://github.com/riseproject-dev/riscv-runner-images/blob/main/dind/Dockerfile) | DinD sidecar image |
| [`dind/dockerd-entrypoint.sh`](https://github.com/riseproject-dev/riscv-runner-images/blob/main/dind/dockerd-entrypoint.sh) | Docker daemon entrypoint with TLS cert generation |
| [`.github/workflows/release.yml`](https://github.com/riseproject-dev/riscv-runner-images/blob/main/.github/workflows/release.yml) | CI/CD pipeline |
