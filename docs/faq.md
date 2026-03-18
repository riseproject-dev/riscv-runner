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

**RISC-V 64-bit (riscv64) only.** All runners execute on physical RISC-V hardware. There is no x86 or ARM emulation — binaries must be compiled for riscv64.

## What operating systems are available?

Ubuntu 24.04 and Ubuntu 26.04. The OS version is determined by the runner label you choose (e.g., `ubuntu-24.04-riscv` vs `ubuntu-26.04-riscv`).

## How do I get access?

Install the [RISE RISC-V Runners GitHub App](https://github.com/apps/rise-risc-v-runners) on your organization. Your organization must be on the allowlist. Contact the RISE project team if your workflows are not picking up runners after installation.

## Can I use this for personal repositories?

Personal accounts are not supported yet. The GitHub App can only be installed on organizations. If you need personal account support, open an issue at [riscv-runner-app](https://github.com/riseproject-dev/riscv-runner-app/issues).

## How many jobs can run concurrently?

Concurrency is limited by:

1. **Hardware capacity** — each RISC-V node runs at most one job at a time (enforced by the [device plugin](architecture/kubernetes))
2. **Per-org limits** — each organization has a configurable maximum number of concurrent workers across all pools

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
