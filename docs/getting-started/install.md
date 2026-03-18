---
title: Install the GitHub App
parent: Getting Started
nav_order: 1
---

# Install the GitHub App

[RISE RISC-V Runners](https://github.com/apps/rise-risc-v-runners) is delivered as a [GitHub App](https://github.com/apps/rise-risc-v-runners). Install it on your organization to start running CI jobs on RISC-V hardware.

We only supports installation of the [RISE RISC-V Runners GitHub App](https://github.com/apps/rise-risc-v-runners) on organizatons at the moment. Support for personal account is a work in progress. [Reach out](https://github.com/riseproject-dev/riscv-runner-app/issues) to let us know of your needs.
{: .warning }

## Steps

1. Go to [github.com/apps/rise-risc-v-runners](https://github.com/apps/rise-risc-v-runners)
2. Click **Install**
3. Choose the organization where you want to use RISC-V runners
4. Select repository access:
   - **All repositories** — enables RISC-V runners across every repo in the org
   - **Only select repositories** — pick specific repos
5. Click **Install**

## What the app does

The app listens for `workflow_job` webhooks. When a job requests a RISC-V runner label (e.g., `ubuntu-24.04-riscv`), the app provisions a runner pod on a RISC-V Kubernetes node. The runner registers with GitHub as a just-in-time self-hosted runner, executes the job, and is cleaned up afterward.

The app requires these organization permissions:
- **Self-hosted runners** (read/write) — to register and manage self-hosted runners
- **Metadata** (read) — required by GitHub for all apps

## Access model

Contact the RISE project team if the app installation does not trigger runners for your workflows.

## Next step

[Configure Your Workflow](configure) to start using RISC-V runners in your CI.
