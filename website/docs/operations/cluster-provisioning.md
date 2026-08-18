---
title: Cluster Provisioning
parent: Operations
nav_order: 1
---

# Cluster Provisioning

Production and staging each have their own Kubernetes cluster on Scaleway, managed via [`scripts/scw.py`](https://github.com/riseproject-dev/riscv-runner/blob/main/scripts/scw.py).

## Provisioning subcommands

| Subcommand | Purpose |
|---|---|
| `scw.py control-plane create [--staging]` | Create a Kubernetes control plane (Scaleway POP2-2C-8G) with containerd, kubeadm, Flannel CNI, RBAC, and device plugins |
| `scw.py runner create --control-plane <name> <count>` | Create bare-metal RISC-V runner nodes (Scaleway EM-RV1) and join them to the cluster |
| `scw.py runner list --control-plane <name>` | List runners tagged to a control plane |
| `scw.py runner reinstall <runner-name>` | Reinstall the OS on a runner (wipes and re-joins the cluster). Accepts brace expansion: `riscv-runner-{6,25,27}` |
| `scw.py runner setup <runner-name>` | Re-run post-install configuration |
| `scw.py runner reboot <runner-name>` | Reboot the bare-metal server |
| `scw.py runner delete <runner-name>` | Delete a runner node |

Defaults: `ZONE=fr-par-2`, `PROJECT_ID=03a2e06e-…`, `PRIVATE_NETWORK_ID=58fa41d0-…`. Constants are hard-coded at the top of `scw.py`.

## Creating a new cluster from scratch

```sh
cd scripts
python3 -m venv .venv
source .venv/bin/activate
pip3 install -r requirements.txt

# 1. Create the control plane (--staging for the staging cluster).
python scw.py control-plane create

# 2. Add 3 bare-metal RISC-V runners.
python scw.py runner create --control-plane <control-plane-name> 3

# 3. Push kubeconfigs into GitHub Secrets. Replace `--env prod` with `--env staging`
#    when targeting the staging cluster.
SCW_QUERY='zone=fr-par-2 project-id=03a2e06e-e7c1-45a6-9f05-775d813c2e28'
SELECT_HOST='.[] | select(.name == "<control-plane-name>") | .public_ip.address'
HOST=$(scw instance server list $SCW_QUERY -o json | jq -r "$SELECT_HOST")

ssh root@$HOST cat /etc/kubernetes/kubeconfig-gh-app.conf \
  | gh secret set K8S_KUBECONFIG --repo riseproject-dev/riscv-runner --env prod

ssh root@$HOST cat /etc/kubernetes/kubeconfig-gh-deploy.conf \
  | gh secret set K8S_KUBECONFIG --repo riseproject-dev/riscv-runner --env prod
```

`gh-app` is the kubeconfig used at runtime by the `scheduler` container; it has `edit` access plus node list permission. `gh-deploy` is used by CI (the `K8S_KUBECONFIG` secret read by `deploy-runner.yml`); it has cluster-admin.

## After provisioning

The control plane bootstraps with [`runner/device-plugin/k8s-ds-device-plugin.yaml`](https://github.com/riseproject-dev/riscv-runner/blob/main/runner/device-plugin/k8s-ds-device-plugin.yaml) applied. Each newly-joined node is auto-labelled by the device plugin; it also advertises `riseproject.com/runner: 1` so the scheduler can target it.

To verify a node is ready to accept jobs:

```sh
kubectl describe node <node-name> | grep -E 'riseproject|kubernetes.io/arch'
```

Expected:

```
  kubernetes.io/arch=riscv64
  riseproject.dev/board=scw-em-rv1            # or cloudv10x-pioneer / cloudv10x-jupiter
  riseproject.com/runner:  1                  # under "Allocatable"
```

## Kubernetes RBAC

RBAC is configured automatically by `scw.py control-plane create`. Two user identities matter:

- **`gh-app`** — used by the scheduler container. `edit` access plus `nodes: list` for capacity checks.
- **`gh-deploy`** — used by CI. `cluster-admin`. Stored in GitHub Secrets as `K8S_KUBECONFIG`.

The device plugin has a ServiceAccount in `kube-system` with a ClusterRole granting `nodes: get, patch` (for labelling) and talks to the local kubelet via a Unix socket (no additional RBAC needed for device plugin registration).

## Adding a new board

When new RISC-V hardware enters the fleet:

1. SSH into a node of the new board and read `/sys/firmware/devicetree/base/compatible`. Note the first NUL-separated entry.
2. Add a row to `boardMap` in [`runner/device-plugin/pkg/soc/detect.go`](https://github.com/riseproject-dev/riscv-runner/blob/main/runner/device-plugin/pkg/soc/detect.go).
3. If the new board needs a dedicated label, extend `matchLabelsToK8s` in [`control-plane/cmd/ghfe/payload.go`](https://github.com/riseproject-dev/riscv-runner/blob/main/control-plane/cmd/ghfe/payload.go) and add the label to [Runner Labels](../getting-started/labels).
4. Push and let the device-plugin deploy workflow roll out the new labeller.
