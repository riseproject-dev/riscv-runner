# images

Container image for running GitHub Actions workflows on RISC-V (`linux/riscv64`). Built natively on RISC-V hardware and pushed to the Scaleway Container Registry.

For the full image inventory (every preinstalled tool with its version), the build-and-deploy pipeline, image tags, and the version-sync script, see [Architecture — Container Images](https://riscv-runners.riseproject.dev/docs/architecture/images). This README covers only what a contributor working in `images/` needs to know.

## Layout

```
images/
├── runner/
│   ├── Dockerfile.ubuntu              Runner image (multi-stage, parameterised by OS_VERSION)
│   └── riscv-runner-entrypoint.sh     PID-1 entrypoint, exec's run.sh --jitconfig "$RUNNER_JITCONFIG"
└── versions-map.json                  Mapping from Dockerfile ARGs to upstream version sources
```

Companion files outside `images/`:

- [`../scripts/update-versions.py`](../scripts/update-versions.py) — refreshes `versions-map.json` and the matching `ARG …_VERSION=` lines from the latest `actions/runner-images` release.
- [`../.github/workflows/deploy-images.yml`](../.github/workflows/deploy-images.yml) — build, staging deploy, prod deploy.
- [`../.github/workflows/update-images-versions-map.yml`](../.github/workflows/update-images-versions-map.yml) — weekly version sync.

## Build locally

```sh
docker buildx build \
  --platform linux/riscv64 \
  --file runner/Dockerfile.ubuntu \
  --build-arg OS_VERSION=24.04 \
  --tag riscv-runner:ubuntu-24.04-local \
  runner
```

Best run on a RISC-V host so no emulation is involved. On x86_64, `binfmt_misc` with QEMU will let the build complete, slowly.

## Updating pinned versions

```sh
python3 ../scripts/update-versions.py
```

Reads the latest `ubuntu24/*` release of `actions/runner-images`, walks `versions-map.json`, and rewrites the matching `ARG …_VERSION=` lines in `runner/Dockerfile.ubuntu`. SHA256/SHA512 hashes are not updated automatically and must be edited by hand before merging.

The weekly workflow runs the same script and opens a draft PR if anything changes.

## Adding a new entry to `versions-map.json`

Each entry maps a Dockerfile ARG name to a field in the upstream runner-images manifest:

```json
{
  "arg": "PYTHON312_VERSION",
  "json_tool": "Cached Tools/Python",
  "match_prefix": "3.12",
  "dockerfile": "images/runner/Dockerfile.ubuntu"
}
```

`json_tool` is the path through the manifest tree; `match_prefix` filters list-valued entries (e.g. Python's multiple installed versions). `dockerfile` is the path from the repository root.
