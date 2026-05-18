---
title: FAQ
nav_order: 4
---

# FAQ

## Are runners persistent or ephemeral?

**Ephemeral.** Each job gets a fresh Kubernetes pod. No state persists between jobs. The pod is provisioned when your job is queued and deleted after completion.

## Is Docker available?

**Yes.** `docker`, `docker compose`, and `docker buildx` are pre-installed and work out of the box.

## What architectures are supported?

**RISC-V 64-bit (riscv64) only.** All runners execute on physical RISC-V hardware. There is no RISC-V emulation. Binaries must be compiled for riscv64.

## What operating systems are available?

Ubuntu 24.04 is the only label currently routable. Ubuntu 26.04 is staged in the build pipeline and will be re-enabled once RVA23 hardware lands.

## How do I get access?

Install the GitHub App on your [organization](https://github.com/apps/rise-risc-v-runners) or [personal account](https://github.com/apps/rise-risc-v-runners-personal). The service is open to all. No allowlist or approval required. Contact the [RISE project team](https://github.com/riseproject-dev/riscv-runner/issues) if the app installation does not trigger runners for your workflows.

## Can I use this for personal repositories?

**Yes.** Install the [personal account app](https://github.com/apps/rise-risc-v-runners-personal). It registers runners at the repository level and requires **Administration** permission (the minimum GitHub allows for repo-scoped runner registration). See [Install the GitHub App](getting-started/install) for details.

## How many jobs can run concurrently?

Concurrency is limited by:

1. **Hardware capacity**: each RISC-V node runs at most one job at a time (enforced by the [device plugin](architecture/kubernetes))
2. **Per-entity limits**: each organization or personal account has a configurable maximum number of concurrent workers across all pools

## How long can a job run?

Pods have an `activeDeadlineSeconds` of 525,600 (about 6 days). Jobs exceeding this limit are terminated by the kubelet. For most CI workloads, this is not a constraint.

## What if my job is queued but no runner picks it up?

The scheduler is woken immediately when a webhook arrives, and otherwise polls every 15 seconds. If no RISC-V nodes with available capacity match your label, the job remains queued until a node becomes available. Check the [Runner Labels Reference](getting-started/labels) to make sure you are using a valid single-label `runs-on:` value.

## Can I SSH into the runner?

No. Runners are ephemeral and not accessible outside the job execution context. Debug using workflow step outputs, artifact uploads, or adding diagnostic commands to your workflow.

## What happens if a runner pod crashes?

The scheduler detects pods in the `Failed` state, marks the corresponding worker row in PostgreSQL as `failed` with diagnostics in `failure_info`, and (after a 6-hour grace period during which logs and events remain inspectable via `kubectl`) deletes the pod. GitHub marks the job as failed. You can re-run the job from the GitHub Actions UI.

## Where are the runner images hosted?

Images are stored in the Scaleway Container Registry (`rg.fr-par.scw.cloud`). They are rebuilt daily and on every push to the `main` branch of the [`images/`](https://github.com/riseproject-dev/riscv-runner/tree/main/images) directory.
