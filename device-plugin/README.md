# RISC-V Runner Device Plugin

A Kubernetes device plugin and node labeller for RISC-V worker nodes. It provides two components:

- **k8s-device-plugin** registers a `riseproject.com/runner` resource (quantity 1) with the kubelet on each node. Pods that request this resource get exclusive scheduling, which gives the scheduler concurrency control over CI jobs per node.
- **k8s-node-labeller** detects the RISC-V SoC via the device tree (`/sys/firmware/devicetree/base/compatible`) and labels the node with `riseproject.dev/board=<board-name>`, enabling node selection by board type.

## Architecture

```
┌──────────────────────┐     ┌──────────────────────┐
│  k8s-device-plugin   │     │  k8s-node-labeller   │
│                      │     │                      │
│  Registers resource  │     │  Reads device tree   │
│  riseproject.com/    │     │  Labels node with    │
│  runner=1 via gRPC   │     │  riseproject.dev/    │
│                      │     │  board=<name>        │
│  Talks to: kubelet   │     │  Talks to: API server│
│  RBAC: none          │     │  RBAC: nodes get/patch│
└──────────────────────┘     └──────────────────────┘
        DaemonSet                   DaemonSet
```

Both run as DaemonSets on `riscv64` nodes. They share the `pkg/soc` package for SoC detection but are built and deployed as separate container images.

## Layout

```
device-plugin/
├── cmd/
│   ├── k8s-device-plugin/main.go       Device plugin binary
│   └── k8s-node-labeller/main.go       Node labeller binary
├── pkg/
│   ├── plugin/plugin.go                Device plugin gRPC server
│   ├── soc/detect.go                   SoC detection from device tree
│   └── labeler/labeler.go              Node labeling via k8s API
├── Dockerfile                          Image for k8s-device-plugin
├── labeller.Dockerfile                 Image for k8s-node-labeller
├── Makefile
├── k8s-ds-device-plugin.yaml           DaemonSet manifest
└── k8s-ds-node-labeller.yaml           DaemonSet + RBAC manifest
```

## Board mapping

The node labeller reads `/sys/firmware/devicetree/base/compatible` and matches entries against a built-in map:

| Compatible string | Board label |
|---|---|
| `scaleway,em-rv1-c4m16s128-a` | `scw-em-rv1` |
| `sophgo,mango` | `cloudv10x-pioneer` |

If no match is found, the first compatible entry is sanitized and used as the label value. To add new boards, update the `boardMap` in [`pkg/soc/detect.go`](pkg/soc/detect.go).

## Prerequisites

- Go 1.22+
- Podman (for cross-compilation to `riscv64`)
- A Kubernetes cluster with `riscv64` worker nodes

## Build

```bash
# Both binaries for linux/riscv64
make build

# One at a time
make build-device-plugin
make build-node-labeller
```

## Container images

```bash
# Both images
make container-build

# Build and push
make container-push

# Individually
make container-build-device-plugin
make container-build-node-labeller
```

Images are pushed to a single repository with two tags:

- `rg.fr-par.scw.cloud/funcscwriseriscvrunnerappqdvknz9s/riscv-runner:device-plugin-latest`
- `rg.fr-par.scw.cloud/funcscwriseriscvrunnerappqdvknz9s/riscv-runner:node-labeller-latest`

Override the repository:

```bash
make container-push IMAGE_REPO=myregistry.io/my-namespace/riscv-runner
```

> Note: the `make` targets here are slated for inlining into [`../.github/workflows/deploy-device-plugin.yml`](../.github/workflows/deploy-device-plugin.yml) so the Makefile can be removed.

## Deploy

```bash
kubectl apply -f k8s-ds-device-plugin.yaml
kubectl apply -f k8s-ds-node-labeller.yaml
```

CI deploys both DaemonSets automatically via [`../.github/workflows/deploy-device-plugin.yml`](../.github/workflows/deploy-device-plugin.yml) on pushes to `main` that touch `device-plugin/**`.

## Verify

After deployment, check that the device plugin registered the resource:

```bash
kubectl describe node <node-name> | grep riseproject
```

Output should contain:

```
  riseproject.com/runner:  <...>
  riseproject.dev/board=<...>
```

## Usage

Request the runner resource in your pod spec to limit concurrency to one job per node:

```yaml
resources:
  limits:
    riseproject.com/runner: "1"
```

Use the board label for node selection:

```yaml
nodeSelector:
  riseproject.dev/board: scw-em-rv1
```

## License

[MIT](../LICENSE).
