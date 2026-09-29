# images

Container image for running GitHub Actions workflows on RISC-V (`linux/riscv64`). Built natively on RISC-V hardware and pushed to the Scaleway Container Registry.

For the full image inventory (every preinstalled tool with its version), the build-and-deploy pipeline, image tags, and the version-sync script, see [Architecture — Container Images](https://riscv-runners.riseproject.dev/docs/architecture/images). This README covers only what a contributor working in `images/` needs to know.

## Layout

```
runner/images/
├── Dockerfile.ubuntu              Runner image (multi-stage, parameterised by OS_VERSION)
├── Dockerfile.kube-proxy          Multi-architecture Kubernetes kube-proxy image
├── Dockerfile.pause               Multi-architecture Kubernetes pause image
└── riscv-runner-entrypoint.sh     PID-1 entrypoint, exec's run.sh --jitconfig "$RUNNER_JITCONFIG"
```

Companion files outside `runner/images/`:

- [`../../runner/images/versions-map.json`](versions-map.json) — mapping from Dockerfile ARGs to upstream version sources (maintained by `update-versions.py`).
- [`../../scripts/update-versions.py`](../../scripts/update-versions.py) — refreshes `versions-map.json` and the matching `ARG …_VERSION=` lines from the latest `actions/runner-images` release.
- [`../../.github/workflows/deploy-runner.yml`](../../.github/workflows/deploy-runner.yml) — build, staging deploy, prod deploy.
- [`../../.github/workflows/update-images-versions-map.yml`](../../.github/workflows/update-images-versions-map.yml) — weekly version sync.

## Build locally

```sh
docker buildx build \
  --platform linux/riscv64 \
  --file Dockerfile.ubuntu \
  --build-arg OS_VERSION=24.04 \
  --tag ghcr.io/riseproject-dev/riscv-runner/runner/ubuntu-24.04:local \
  .
```

Best run on a RISC-V host so no emulation is involved. On x86_64, `binfmt_misc` with QEMU will let the build complete, slowly.

Build the Debian trixie-based kube-proxy image for all supported architectures:

```sh
docker buildx build \
  --platform linux/amd64,linux/arm64,linux/riscv64 \
  --file Dockerfile.kube-proxy \
  --tag ghcr.io/riseproject-dev/riscv-runner/kube-proxy:v1.35.0 \
  --push \
  .
```

Build the pause image for the same architectures:

```sh
docker buildx build \
  --platform linux/amd64,linux/arm64,linux/riscv64 \
  --file Dockerfile.pause \
  --tag ghcr.io/riseproject-dev/riscv-runner/pause:3.10 \
  --push \
  .
```

## Updating pinned versions

```sh
python3 ../scripts/update-versions.py
```

Reads the latest `ubuntu24/*` release of `actions/runner-images`, walks `versions-map.json`, and rewrites the matching `ARG …_VERSION=` lines in `Dockerfile.ubuntu`. SHA256/SHA512 hashes are not updated automatically and must be edited by hand before merging.

The weekly workflow runs the same script and opens a draft PR if anything changes.

## Adding a new entry to `versions-map.json`

Each entry maps a Dockerfile ARG name to a field in the upstream runner-images manifest:

```json
{
  "arg": "PYTHON312_VERSION",
  "json_tool": "Cached Tools/Python",
  "match_prefix": "3.12",
  "dockerfile": "runner/images/Dockerfile.ubuntu"
}
```

`json_tool` is the path through the manifest tree; `match_prefix` filters list-valued entries (e.g. Python's multiple installed versions). `dockerfile` is the path from the repository root.
