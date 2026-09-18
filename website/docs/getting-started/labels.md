---
title: Runner Labels Reference
parent: Getting Started
nav_order: 3
---

# Runner Labels Reference

Each label maps to a specific hardware configuration, OS version, and Kubernetes pool.

## Labels

| Label | OS | Status |
|-------|-----|--------|
| `ubuntu-24.04-riscv` | Ubuntu 24.04 | Generally available |
| `ubuntu-26.04-riscv` | Ubuntu 26.04 | Early Access |

Matching is on the exact set of `runs-on` labels. Order does not matter, but an unrecognised extra label means no rule matches and the job is ignored.

## Label-to-hardware mapping

Each label maps to a **node selector**: a board plus the provider that supplies the machine.

| Label | Board | Provider |
|-------|-------|----------|
| `ubuntu-24.04-riscv` | `scaleway-em-rv1` | `scaleway` |
| `ubuntu-26.04-riscv` | TBD | TBD |

Some organizations are routed to dedicated hardware instead.

Routing is decided by the organization and repository, not by the workflow. There is no label that selects a provider directly.

## Runner exclusivity

Each RISC-V node runs at most one job at a time. This is enforced by the [device plugin](../architecture/kubernetes), which advertises a single `riseproject.com/runner` resource per node. Pods request the resource, and the Kubernetes scheduler prevents double-booking.

## Choosing a label

Use `ubuntu-24.04-riscv` for any RISC-V CI workload. The board and provider you land on depend on the routing rules above; you do not pick hardware directly.
