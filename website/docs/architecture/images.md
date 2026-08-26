---
title: Container Images
parent: Architecture
nav_order: 4
---

# Container Images

Each runner pod uses a single unified container image: the GitHub Actions runner together with the full set of tools listed below. The image is built for `linux/riscv64` and stored in the Scaleway Container Registry.

**Source:** the [`runner/images/`](https://github.com/riseproject-dev/riscv-runner/tree/main/runner/images) directory.

## Runner image

**Dockerfile:** [`Dockerfile.ubuntu`](https://github.com/riseproject-dev/riscv-runner/blob/main/runner/images/Dockerfile.ubuntu).

The runner image is a multi-stage build based on Ubuntu. `Dockerfile.ubuntu` is parameterised by `ARG OS_VERSION`. The build pipeline currently produces Ubuntu 24.04 images; the matrix entry for 26.04 is commented out and will be re-enabled when RVA23 hardware lands.

### GitHub Actions Runner

The [GitHub Actions Runner for RISC-V](https://github.com/Cloud-V-10xE/github-runner-riscv), built with .NET 8. This is the process that registers with GitHub, receives the job, and executes workflow steps. The JIT runner config is passed in via the `RUNNER_JITCONFIG` environment variable; the scheduler obtains it from the GitHub API at pod creation time.

### Pre-installed software

| Category | Packages |
|----------|----------|
| **Languages** | Python 3.10–3.14 (including free-threaded variants), Node.js 20/22/24, Go 1.22–1.26, Rust, Java (Temurin 17/21/25), PHP, Ruby, Perl, Lua, R |
| **Compilers** | GCC 12/13/14, G++, Clang |
| **Build tools** | Make, CMake, Ninja, Autoconf, Automake, Libtool, Flex, Bison, Binutils, Gradle, Maven, Ant |
| **Container tooling** | Docker (CLI, Buildx, Compose, daemon), podman, buildah, skopeo, runc, kubectl, tini, crictl |
| **VCS** | Git, Mercurial, `gh` CLI |
| **Networking** | curl, wget, openssh-client, netcat, dnsutils |
| **Compression** | bzip2, lz4, xz, zip, 7z, aria2 |
| **Packaging** | dpkg, rpm, fakeroot |
| **Utilities** | jq, shellcheck, tree, rsync, sudo, parallel, sccache |

The image aims to track the [official GitHub Actions Ubuntu runner images](https://github.com/actions/runner-images). Pinned versions live in [`runner/images/versions-map.json`](https://github.com/riseproject-dev/riscv-runner/blob/main/runner/images/versions-map.json). If your workflow needs a package that is not in the image, [open an issue](https://github.com/riseproject-dev/riscv-runner/issues).

### Entrypoint and runtime

[`riscv-runner-entrypoint.sh`](https://github.com/riseproject-dev/riscv-runner/blob/main/runner/images/riscv-runner-entrypoint.sh) is the PID-1 entrypoint, wrapped by `tini` (`docker-init`). It:

1. Verifies the container runs as the `runner` user in `/home/runner`.
2. Detects iptables legacy vs nf_tables and adjusts PATH.
3. Starts `containerd` then `dockerd --mtu=1450` in the background.
4. Requires `RUNNER_VERSION` and `RUNNER_JITCONFIG` env vars.
5. Launches `run.sh --jitconfig "$RUNNER_JITCONFIG"`.
6. Forwards SIGTERM to the runner; a cleanup trap kills `dockerd` and `containerd` on exit.

The image creates a non-root `runner` user with passwordless sudo. All workflow steps run inside this single container. The pod runs with `privileged: true` and host network so the in-pod Docker daemon can program iptables and bridge devices.

## Build pipeline

**Workflow:** [`.github/workflows/deploy-runner.yml`](https://github.com/riseproject-dev/riscv-runner/blob/main/.github/workflows/deploy-runner.yml).

A single `build-runner` matrix job builds the runner image. Currently only `ubuntu 24.04` is enabled.

- **Trigger:** push or PR to `main` filtered to `runner/**`, daily schedule at 06:00 UTC, or manual dispatch.
- **Platform:** `linux/riscv64`, built natively on self-hosted `ubuntu-24.04-riscv` runners. No QEMU emulation in the build path.
- **Caching:** GitHub Actions Cache (`type=gha`) for Docker layer reuse. A concurrency group ensures only one build runs per scope.
- **Registry choice:** Scaleway for `riseproject-dev/main`, `ghcr.io` for other internal branches, a tar artifact for external PRs.
- **Deploy:** after `main` builds, `deploy-staging` retags `:ubuntu-24.04-staging`, then `deploy-prod` retags `:ubuntu-24.04-prod` after an environment-gated approval. Both deploys `kubectl rollout restart daemonset/rise-riscv-runner-device-plugin` so each node pre-pulls the new image via the init container in the daemonset.

### Version sync

[`scripts/update-versions.py`](https://github.com/riseproject-dev/riscv-runner/blob/main/scripts/update-versions.py) fetches the latest `actions/runner-images` release tagged `ubuntu24/*`, downloads its `internal.ubuntu24.json` manifest, walks `runner/images/versions-map.json`, and updates the matching `ARG …_VERSION=` lines in the referenced Dockerfiles. It does not update SHA256/SHA512 hashes; those must be edited manually before merging.

The weekly workflow [`update-images-versions-map.yml`](https://github.com/riseproject-dev/riscv-runner/blob/main/.github/workflows/update-images-versions-map.yml) runs the script every Monday at 00:00 UTC and opens a draft PR if anything changes.

## Registry

Images are stored in the Scaleway Container Registry:

```
rg.fr-par.scw.cloud/funcscwriseriscvrunnerappqdvknz9s/riscv-runner
```

### Image tags

| Tag | Image | Source branch |
|-----|-------|---------------|
| `ubuntu-24.04-prod` | Runner image, Ubuntu 24.04 | `main` (deploy-prod) |
| `ubuntu-26.04-prod` | Runner image, Ubuntu 26.04 | `main` (deploy-prod) |
| `ubuntu-24.04-staging` | Runner image, Ubuntu 24.04 | `main` (deploy-staging) |
| `ubuntu-26.04-staging` | Runner image, Ubuntu 26.04 | `main` (deploy-staging) |
| `ubuntu-24.04-sha-<sha>` | Per-commit build | every build |
| `ubuntu-26.04-sha-<sha>` | Per-commit build | every build |

## Source files

| File | Role |
|------|------|
| [`Dockerfile.ubuntu`](https://github.com/riseproject-dev/riscv-runner/blob/main/runner/images/Dockerfile.ubuntu) | Runner image (multi-stage: tools, language runtimes, container tooling) |
| [`riscv-runner-entrypoint.sh`](https://github.com/riseproject-dev/riscv-runner/blob/main/runner/images/riscv-runner-entrypoint.sh) | PID-1 entrypoint, exec's `run.sh --jitconfig "$RUNNER_JITCONFIG"` |
| [`versions-map.json`](https://github.com/riseproject-dev/riscv-runner/blob/main/runner/images/versions-map.json) | Pinned versions for all bundled tools and runtimes |
| [`.github/workflows/deploy-runner.yml`](https://github.com/riseproject-dev/riscv-runner/blob/main/.github/workflows/deploy-runner.yml) | Build, staging deploy, prod deploy |
| [`.github/workflows/update-images-versions-map.yml`](https://github.com/riseproject-dev/riscv-runner/blob/main/.github/workflows/update-images-versions-map.yml) | Weekly version sync |
| [`scripts/update-versions.py`](https://github.com/riseproject-dev/riscv-runner/blob/main/scripts/update-versions.py) | Version sync script |
