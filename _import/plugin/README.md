# RISC-V Runner Device Plugin

A Kubernetes device plugin and node labeller for RISC-V worker nodes. It provides two components:

- **k8s-device-plugin** — Registers a `riseproject.com/runner` resource (quantity 1) with the kubelet on each node. Pods that request this resource get exclusive scheduling, giving you concurrency control over CI jobs per node.
- **k8s-node-labeller** — Detects the RISC-V SOC via the device tree (`/sys/firmware/devicetree/base/compatible`) and labels the node with `riseproject.dev/board=<board-name>`, enabling node selection by board type.

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

Both run as DaemonSets on `riscv64` nodes. They share the `pkg/soc` package for SOC detection but are built and deployed as separate container images.

## Project Structure

```
├── cmd/
│   ├── k8s-device-plugin/main.go       # Device plugin binary
│   └── k8s-node-labeller/main.go       # Node labeller binary
├── pkg/
│   ├── plugin/plugin.go                # Device plugin gRPC server
│   ├── soc/detect.go                   # SOC detection from device tree
│   └── labeler/labeler.go              # Node labeling via k8s API
├── Dockerfile                          # Image for k8s-device-plugin
├── labeller.Dockerfile                 # Image for k8s-node-labeller
├── Makefile
├── k8s-ds-device-plugin.yaml           # DaemonSet manifest
└── k8s-ds-node-labeller.yaml           # DaemonSet + RBAC manifest
```

## Board Mapping

The node labeller reads `/sys/firmware/devicetree/base/compatible` and matches entries against a built-in map:

| Compatible String | Board Label |
|---|---|
| `scaleway,em-rv1-c4m16s128-a` | `scw-em-rv1` |
| `sophgo,mango` | `cloudv10x-pioneer` |

If no match is found, the first compatible entry is sanitized and used as the label value. To add new boards, update the `boardMap` in `pkg/soc/detect.go`.

## Prerequisites

- Go 1.22+
- Podman (for cross-compilation to `riscv64`)
- A Kubernetes cluster with `riscv64` worker nodes

## Build

```bash
# Build both binaries for linux/riscv64
make build

# Build only one
make build-device-plugin
make build-node-labeller
```

## Container Images

```bash
# Build both images
make container-build

# Build and push both images
make container-push

# Build individually
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

## Deploy

```bash
kubectl apply -f k8s-ds-device-plugin.yaml
kubectl apply -f k8s-ds-node-labeller.yaml
```

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
