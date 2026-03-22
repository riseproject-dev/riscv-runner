---
title: FAQ
nav_order: 4
---

# FAQ

## Are runners persistent or ephemeral?

**Ephemeral.** Each job gets a fresh Kubernetes pod. No state persists between jobs. The pod is provisioned when your job is queued and deleted after completion.

## Is Docker available?

**Yes.** Every runner includes a Docker-in-Docker sidecar. You can use `docker build`, `docker run`, Docker Compose, and Buildx out of the box. Docker communicates over TLS within the pod.

## What architectures are supported?

**RISC-V 64-bit (riscv64) only.** All runners execute on physical RISC-V hardware. There is no x86 or ARM emulation. Binaries must be compiled for riscv64.

## What operating systems are available?

Ubuntu 24.04 and Ubuntu 26.04. The OS version is determined by the runner label you choose (e.g., `ubuntu-24.04-riscv` vs `ubuntu-26.04-riscv`).

## How do I get access?

Install the GitHub App on your [organization](https://github.com/apps/rise-risc-v-runners) or [personal account](https://github.com/apps/rise-risc-v-runners-personal). The service is open to all. No allowlist or approval required. Contact the [RISE project team](https://github.com/riseproject-dev/riscv-runner-app/issues) if the app installation does not trigger runners for your workflows.

## Can I use this for personal repositories?

**Yes.** Install the [personal account app](https://github.com/apps/rise-risc-v-runners-personal). It registers runners at the repository level and requires **Administration** permission (the minimum GitHub allows for repo-scoped runner registration). See [Install the GitHub App](getting-started/install) for details.

## How many jobs can run concurrently?

Concurrency is limited by:

1. **Hardware capacity**: each RISC-V node runs at most one job at a time (enforced by the [device plugin](architecture/kubernetes))
2. **Per-entity limits**: each organization or personal account has a configurable maximum number of concurrent workers across all pools

## How long can a job run?

Pods have an active deadline of approximately 6 days. Jobs exceeding this limit are terminated. For most CI workloads, this is not a constraint.

## What if my job is queued but no runner picks it up?

The worker checks for pending jobs every 15 seconds. If no RISC-V nodes with available capacity match your label, the job remains queued until a node becomes available. Check the [Runner Labels Reference](getting-started/labels) to make sure you are using a valid label.

## Can I SSH into the runner?

No. Runners are ephemeral and not accessible outside the job execution context. Debug using workflow step outputs, artifact uploads, or adding diagnostic commands to your workflow.

## What happens if a runner pod crashes?

The worker's cleanup loop (every 15 seconds) detects pods in `Failed` state and deletes them. The corresponding Redis worker record is also cleaned up. GitHub will mark the job as failed. You can re-run the job from the GitHub Actions UI.

## Where are the runner images hosted?

Images are stored in the Scaleway Container Registry (`rg.fr-par.scw.cloud`). They are rebuilt daily and on every push to the `main` branch of [riscv-runner-images](https://github.com/riseproject-dev/riscv-runner-images).
