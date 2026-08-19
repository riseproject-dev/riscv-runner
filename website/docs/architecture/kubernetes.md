---
title: Kubernetes Infrastructure
parent: Architecture
nav_order: 3
---

# Kubernetes Infrastructure

The device plugin runs as a DaemonSet on every RISC-V node. At startup it labels the node with the detected SoC board name, then registers with the kubelet to advertise a scheduling resource. Together, the label and the resource enable board-specific scheduling with exclusive node access.

**Source:** the [`runner/device-plugin/`](https://github.com/riseproject-dev/riscv-runner/tree/main/runner/device-plugin) directory

## How it fits together

```mermaid
flowchart TD
    subgraph Node["RISC-V Node"]
        HWP["riscv_hwprobe syscall"]
        DT["/sys/firmware/devicetree/base/compatible<br/>(Scaleway fallback)"]
        DP["Device Plugin Pod"]
        KL["Kubelet"]
        RP["Runner Pod"]
    end

    subgraph Cluster["Kubernetes Cluster"]
        SCHED["Scheduler"]
    end


    HWP <-->|"mvendorid, marchid, mimpid"| DP
    DT <-->|"probe failed: read compatible"| DP
    DP -->|"patch node label riseproject.dev/board=spacemit-k3"| SCHED

    DP -->|"advertise riseproject.com/runner=1"| KL
    KL -->|report allocatable resources| SCHED

    KL -->|schedule| RP
    SCHED -->|"nodeSelector: riseproject.dev/board\nlimits: riseproject.com/runner=1"| KL
```

## Device plugin

The device plugin implements the Kubernetes Device Plugin API via gRPC. It advertises exactly **one** `riseproject.com/runner` resource per node.

Runner pods request this resource:

```yaml
resources:
  limits:
    riseproject.com/runner: "1"
```

Since only one unit exists per node, the Kubernetes scheduler will never place two runner pods on the same node. This enforces exclusive access without taints or manual coordination.

### How it works

1. The plugin starts and registers with kubelet via a Unix socket at `/var/lib/kubelet/device-plugins/rise-riscv-runner.sock`
2. `ListAndWatch()` advertises a single healthy device (`runner-0`) to the kubelet
3. `Allocate()` returns an empty response. No actual device allocation is needed; only the scheduling constraint matters
4. A file watcher monitors the kubelet socket directory and re-registers if kubelet restarts

## Node labelling

The device plugin detects the SoC on each RISC-V node at startup and applies a `riseproject.dev/board` label. Runner pods use this label in their `nodeSelector` to land on the correct hardware.

### SoC detection

The primary key is the `riscv_hwprobe(2)` syscall, which returns the hardware identity triple (`mvendorid`, `marchid`, `mimpid`) read from the CPU CSRs.

1. Call `riscv_hwprobe` for the three ID keys and log the triple as hex.
2. Match the triple against a hand-maintained list of known SoCs:

| `mvendorid` | `marchid` | `mimpid` | Board label |
|-------------|-----------|----------|-------------|
| `0x710` | `0x8000000058000001` | `0x1000000049772200` | `spacemit-k1` |
| `0x710` | `0x8000000058000002` | `0x33d8a600` | `spacemit-k3` |
| `0x710` | `0x8000000058000002` | `0x4c4d900` | `spacemit-v100` |

3. If the triple matches no entry, `Detect` returns an error and the plugin exits. An unrecognized node fails loudly rather than mislabelling itself. To add the board, read the logged triple and append an entry to the list.

The Scaleway EM-RV1 is a special case: its kernel predates `riscv_hwprobe`, so the syscall fails. Only on that failure does detection fall back to reading `/sys/firmware/devicetree/base/compatible`; a `scaleway,em-rv1` prefix yields the `scaleway-em-rv1` label. Any other board on a kernel without the syscall is treated as the original probe failure.

### DaemonSet configuration

- **Namespace:** `kube-system`
- **Node selector:** `kubernetes.io/arch: riscv64`
- **Priority:** `system-node-critical`
- **RBAC:** ServiceAccount with ClusterRole granting `get` and `patch` on nodes
- **Environment:** `NODE_NAME` from downward API (`spec.nodeName`)
- **Volume mounts:** `/var/lib/kubelet/device-plugins` (host path), `/sys` (read-only host path)
- **Privileged:** Yes (device tree access for the Scaleway fallback)
- **Image:** `rg.fr-par.scw.cloud/funcscwriseriscvrunnerappqdvknz9s/riscv-runner:device-plugin-prod`

## Source files

| File | Role |
|------|------|
| [`cmd/k8s-device-plugin/main.go`](https://github.com/riseproject-dev/riscv-runner/blob/main/runner/device-plugin/cmd/k8s-device-plugin/main.go) | Entry point: label node, then start device plugin |
| [`pkg/plugin/plugin.go`](https://github.com/riseproject-dev/riscv-runner/blob/main/runner/device-plugin/pkg/plugin/plugin.go) | gRPC server, kubelet registration, watchdog |
| [`pkg/soc/detect.go`](https://github.com/riseproject-dev/riscv-runner/blob/main/runner/device-plugin/pkg/soc/detect.go) | `riscv_hwprobe` triple matching, Scaleway device tree fallback, SoC → board mapping |
| [`pkg/labeler/labeler.go`](https://github.com/riseproject-dev/riscv-runner/blob/main/runner/device-plugin/pkg/labeler/labeler.go) | Kubernetes API node label patching |
| [`k8s-ds-device-plugin.yaml`](https://github.com/riseproject-dev/riscv-runner/blob/main/runner/device-plugin/k8s-ds-device-plugin.yaml) | DaemonSet + RBAC manifest |
