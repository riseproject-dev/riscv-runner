# device-plugin

Two Go binaries that run as DaemonSets on every RISC-V worker node:

- **`k8s-device-plugin`** — registers `riseproject.com/runner: 1` with the kubelet, giving the Kubernetes scheduler exclusive-access semantics for runner pods.
- **`k8s-node-labeller`** — detects the RISC-V SoC from the device tree and labels the node with `riseproject.dev/board=<board-name>`.

For architecture, the gRPC registration flow, the full SoC→board map, and the cluster integration, see [Architecture — Kubernetes Infrastructure](https://riscv-runners.riseproject.dev/docs/architecture/kubernetes). For ops (provisioning new nodes, adding new boards), see [Operations — Cluster Provisioning](https://riscv-runners.riseproject.dev/docs/operations/cluster-provisioning).

Go module: `github.com/riseproject-dev/riscv-runner/device-plugin`.

## Layout

```
device-plugin/
├── cmd/
│   ├── k8s-device-plugin/main.go       device plugin entry point
│   └── k8s-node-labeller/main.go       node labeller entry point
├── pkg/
│   ├── plugin/plugin.go                gRPC Device Plugin server, kubelet registration
│   ├── soc/detect.go                   /sys/firmware/devicetree/base/compatible → board name
│   └── labeler/labeler.go              MergePatch the node with riseproject.dev/board
├── Dockerfile                          image for k8s-device-plugin
├── labeller.Dockerfile                 image for k8s-node-labeller
├── Makefile
├── k8s-ds-device-plugin.yaml           DaemonSet (kube-system)
└── k8s-ds-node-labeller.yaml           DaemonSet + ServiceAccount + ClusterRole + binding (kube-system)
```

External dependencies: `google.golang.org/grpc`, `k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1`, `k8s.io/client-go`, `github.com/fsnotify/fsnotify`, `k8s.io/klog/v2`.

## Build

```sh
# Both binaries for linux/riscv64.
make build

# Individually.
make build-device-plugin
make build-node-labeller

# Container images (riscv64).
make container-build
make container-push
```

Defaults: `REGISTRY=rg.fr-par.scw.cloud/funcscwriseriscvrunnerappqdvknz9s`, `IMAGE=riscv-runner`, `TAG=staging`, `GOARCH=riscv64`. Override with `make container-push TAG=latest`.

> The `make` targets here are slated for inlining into [`../.github/workflows/deploy-device-plugin.yml`](../.github/workflows/deploy-device-plugin.yml) so the Makefile can be removed.

## Apply manifests

```sh
make kubectl-apply-node-labeller
make kubectl-apply-and-wait-device-plugin
```

Both targets envsubst `${TAG}` into the manifests before piping to `kubectl apply`. The `-and-wait` variant waits for the rollout to finish.

CI applies the same manifests automatically via [`../.github/workflows/deploy-device-plugin.yml`](../.github/workflows/deploy-device-plugin.yml).

## Adding a new board

1. SSH into a node of the new board and read `/sys/firmware/devicetree/base/compatible`. Note the first NUL-separated entry (e.g. `vendor,part-number`).
2. Add the entry to `boardMap` in [`pkg/soc/detect.go`](pkg/soc/detect.go).
3. Push the change; CI rebuilds the node-labeller image and rolls out the DaemonSet.
4. New nodes auto-label on next labeller start; existing nodes pick up the new label when their labeller pod restarts.

If the new board should be addressable by a new `runs-on:` label, also extend `matchLabelsToK8s` in [`../container/cmd/ghfe/payload.go`](../container/cmd/ghfe/payload.go) and update the [Runner Labels Reference](https://riscv-runners.riseproject.dev/docs/getting-started/labels).

## Resource and label semantics

Runner pods request the device plugin's resource and select the board label:

```yaml
resources:
  limits:
    riseproject.com/runner: "1"
nodeSelector:
  riseproject.dev/board: scw-em-rv1
```

The combination guarantees exclusive node access (one runner pod per node) and lands the pod on the right hardware. The scheduler in `container/cmd/scheduler/` sets both fields when provisioning.
