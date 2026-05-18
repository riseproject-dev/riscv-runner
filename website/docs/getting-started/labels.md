---
title: Runner Labels Reference
parent: Getting Started
nav_order: 3
---

# Runner Labels Reference

Each label maps to a specific hardware configuration, OS version, and Kubernetes pool.

## Labels

| Label | OS | Board | SoC | Status |
|-------|-----|-------|-----|--------|
| `ubuntu-24.04-riscv` | Ubuntu 24.04 | Scaleway EM-RV1 | SpacemiT K1 | Generally available |
| `ubuntu-26.04-riscv` | Ubuntu 26.04 | TBD | TBD | Not yet enabled (awaiting RVA23 hardware) |

The webhook handler only recognises single-label arrays; jobs using multiple `runs-on` labels (other than via `[self-hosted, ...]` matrix combinations) will not be picked up. See the routing table in [Webhook Handler § Label matching](../architecture/ghfe#label-matching).

## Label-to-Kubernetes mapping

Each label maps to a Kubernetes node pool, selected by the `riseproject.dev/board` node label.

| Label | Default pool | GGML scope pool |
|-------|--------------|-----------------|
| `ubuntu-24.04-riscv` | `scw-em-rv1` | `cloudv10x-jupiter` |

"GGML scope" means jobs running in `ggml-org/*`, `riseproject-dev/llama.cpp`, or `riseproject-dev/llama.cpp-validation`. Those workloads are routed to `cloudv10x-jupiter` (CloudV 10xE Jupiter, SpacemiT K1) hardware.

## Runner exclusivity

Each RISC-V node runs at most one job at a time. This is enforced by the [device plugin](../architecture/kubernetes), which advertises a single `riseproject.com/runner` resource per node. Pods request the resource, and the Kubernetes scheduler prevents double-booking.

## Choosing a label

Today there is only one user-facing label: `ubuntu-24.04-riscv`. Use it for any RISC-V CI workload. The board you actually land on depends on the routing rules above; you do not pick a board directly.
