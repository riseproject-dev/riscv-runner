---
title: Operations
nav_order: 4
has_children: true
---

# Operations

Material for operators of the service. Read this if you maintain a deployment, provision new RISC-V hardware, debug a stuck job, or inspect the running state.

For an architectural overview of what runs where, see the [Architecture](../architecture/) section. For component-level build and test commands, see each component's `README.md` in the [monorepo](https://github.com/riseproject-dev/riscv-runner).

## Pages in this section

- [Cluster Provisioning](cluster-provisioning): create and maintain Scaleway control planes and bare-metal RISC-V nodes with [`scripts/scw.py`](https://github.com/riseproject-dev/riscv-runner/blob/main/scripts/scw.py).
- [Runbooks](runbooks): inspect database state, force pod cleanup, debug failed workers via the trace endpoints, and rotate secrets.

## Deployments at a glance

Production and staging each have their own Kubernetes cluster on Scaleway. Four Scaleway Container Functions are deployed in total:

- `ghfe` + `scheduler` (production, deployed from `main`).
- `ghfe` + `scheduler` (staging, deployed from `staging`).

All four are defined by [`control-plane/serverless.yml`](https://github.com/riseproject-dev/riscv-runner/blob/main/control-plane/serverless.yml). The scheduler's `serverless.yml` pins `minScale=1 maxScale=1` so the `LOCK TABLE workers` invariant is trivially preserved.

| Service | Product | Purpose |
|---|---|---|
| `ghfe` | Scaleway Container Function | Receives GitHub webhooks, writes job state to PostgreSQL |
| `scheduler` | Scaleway Container Function | Demand matching, pod provisioning, cleanup, worker sync |
| State store | Scaleway Managed Database | PostgreSQL: `jobs`, `workers`, `installation_events` |
| Runner pods | Self-hosted Kubernetes clusters on Scaleway EM-RV1 (and CloudV 10xE Pioneer / Jupiter) | Ephemeral RISC-V runner pods |
