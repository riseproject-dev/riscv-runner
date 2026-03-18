---
title: Runner Labels Reference
parent: Getting Started
nav_order: 3
---

# Runner Labels Reference

Each label maps to a specific hardware configuration and OS version.

## Labels

| Label | OS | Board | SoC | Notes |
|-------|-----|-------|-----|-------|
| `ubuntu-24.04-riscv` | Ubuntu 24.04 | Scaleway EM-RV1 | TH1520 | Standard runner |
| `ubuntu-26.04-riscv` | Ubuntu 26.04 | TBD | TBD | Standard runner |

## Label-to-Kubernetes mapping

Each label maps to a Kubernetes node pool selected by node labels:

| Label | K8s Pool |
|-------|----------|
| `ubuntu-24.04-riscv` | `scw-em-rv1` |
| `ubuntu-26.04-riscv` | TBD |

## Runner exclusivity

Each RISC-V node runs at most one job at a time. This is enforced by the [device plugin](../architecture/kubernetes), which advertises a single `riseproject.com/runner` resource per node. Pods request this resource, and the Kubernetes scheduler prevents double-booking.

## Choosing a label

- Use `ubuntu-24.04-riscv` for general-purpose RISC-V CI
- Use `ubuntu-26.04-*` variants if your project targets Ubuntu 26.04
