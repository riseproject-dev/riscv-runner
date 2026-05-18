# RISC-V Runner Images

Container images for running GitHub Actions runners on RISC-V (`linux/riscv64`). Built natively on RISC-V hardware and pushed to the Scaleway Container Registry.

## Runner image

**Dockerfile:** [`runner/Dockerfile.ubuntu`](runner/Dockerfile.ubuntu)

GitHub Actions runner image based on Ubuntu. Available variants:

| Tag | Base |
|-----|------|
| `riscv-runner:ubuntu-24.04-<suffix>` | Ubuntu 24.04 |
| `riscv-runner:ubuntu-26.04-<suffix>` | Ubuntu 26.04 |

`<suffix>` is `latest` for builds from `main`, and the branch slug otherwise (e.g. `staging`).

The runner image includes:

- [GitHub Actions Runner for RISC-V](https://github.com/Cloud-V-10xE/github-runner-riscv) (built with .NET 8)
- Java (Adoptium Temurin)
- Python (including free-threaded variants)
- Node.js, Go, Rust
- Apache Ant, Gradle, Apache Maven
- Docker (CLI + daemon, Buildx, Compose), podman, buildah, skopeo, runc, kubectl
- git, curl, wget, jq, sudo, and many more CLI tools

Pinned versions for every tool above live in [`versions-map.json`](versions-map.json) and are kept in sync with upstream by [`../scripts/update-versions.py`](../scripts/update-versions.py).

The image aims to match the packages installed in the [official GitHub Actions runner images](https://github.com/actions/runner-images). If a package you depend on is missing, open an issue.

Build args:

- `OS_VERSION` — Ubuntu base image version (default: `latest`)

The image creates a non-root `runner` user with passwordless sudo. All workflow steps run inside this single container. The pod runs with `privileged: true` so the in-pod Docker daemon can program iptables and bridge devices.

## Layout

```
images/
├── runner/
│   ├── Dockerfile.ubuntu              Runner image (multi-stage)
│   └── riscv-runner-entrypoint.sh     PID-1 entrypoint, execs the runner
└── versions-map.json                  Pinned versions for all bundled tools
```

The runner build pipeline lives in [`../.github/workflows/deploy-images.yml`](../.github/workflows/deploy-images.yml). The companion update script that refreshes `versions-map.json` from upstream is [`../scripts/update-versions.py`](../scripts/update-versions.py), scheduled by [`../.github/workflows/update-images-versions-map.yml`](../.github/workflows/update-images-versions-map.yml).

## CI/CD

[`../.github/workflows/deploy-images.yml`](../.github/workflows/deploy-images.yml) triggers on:

- pushes to `main` that touch `images/**` or the workflow itself
- pull requests to `main` with the same path filter
- a daily schedule (06:00 UTC)
- manual dispatch

A single `build-runner` job builds the runner image natively on `ubuntu-24.04-riscv` self-hosted RISC-V runners. Images are pushed to the Scaleway Container Registry. GitHub Actions Cache (`type=gha`) speeds up subsequent builds. A concurrency group ensures only the latest run per branch executes.

Builds from `main` go through staging first, then prod after an environment-gated approval.

## Building locally

```bash
docker buildx build \
  --platform linux/riscv64 \
  --file runner/Dockerfile.ubuntu \
  --build-arg OS_VERSION=24.04 \
  --tag riscv-runner:ubuntu-24.04 \
  runner
```

Best run on a RISC-V host (`linux/riscv64`) so the build does not need any emulation.

## Registry

Images are stored in the Scaleway Container Registry:

```
rg.fr-par.scw.cloud/funcscwriseriscvrunnerappqdvknz9s/riscv-runner
```

## License

[MIT](../LICENSE).
