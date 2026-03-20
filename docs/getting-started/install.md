---
title: Install the GitHub App
parent: Getting Started
nav_order: 1
---

# Install the GitHub App

RISE RISC-V Runners is delivered as a GitHub App. There are two variants — one for organizations and one for personal accounts.

## For organizations

1. Go to [github.com/apps/rise-risc-v-runners](https://github.com/apps/rise-risc-v-runners)
2. Click **Install**
3. Choose the organization where you want to use RISC-V runners
4. Select repository access:
   - **All repositories** — enables RISC-V runners across every repo in the org
   - **Only select repositories** — pick specific repos
5. Click **Install**

The organization app requires these permissions:
- **Self-hosted runners** (read/write) — to register and manage organization-level self-hosted runners
- **Metadata** (read) — required by GitHub for all apps

Runners are registered at the **organization level** within a dedicated runner group named "RISE RISC-V Runners".

## For personal accounts

1. Go to [github.com/apps/rise-risc-v-runners-personal](https://github.com/apps/rise-risc-v-runners-personal)
2. Click **Install**
3. Select repository access:
   - **All repositories** — enables RISC-V runners across every repo in your account
   - **Only select repositories** — pick specific repos
4. Click **Install**

The personal app requires these permissions:
- **Administration** (read/write) — required to register repository-level self-hosted runners
- **Metadata** (read) — required by GitHub for all apps

> The **Administration** permission is broader than ideal. GitHub does not offer a repo-scoped "self-hosted runners" permission for personal accounts, so **Administration** is the minimum required to register runners at the repository level.
{: .warning }

Runners are registered at the **repository level** (not in a runner group).

## How the app works

The app listens for `workflow_job` webhooks. When a job requests a RISC-V runner label (e.g., `ubuntu-24.04-riscv`), the app provisions a runner pod on a RISC-V Kubernetes node. The runner registers with GitHub as a just-in-time self-hosted runner, executes the job, and is cleaned up afterward.

## Access model

The service is open to any organization or personal account that installs the app. Contact the [RISE project team](https://github.com/riseproject-dev/riscv-runner-app/issues) if the app installation does not trigger runners for your workflows.

## Next step

[Configure Your Workflow](configure) to start using RISC-V runners in your CI.
