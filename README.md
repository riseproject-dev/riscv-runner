# RISE RISC-V Runners

[RISE RISC-V Runners](https://riseproject-dev.github.io/riscv-runner) is a managed GitHub Actions runner service that executes CI/CD workflows on real RISC-V hardware. Install the GitHub App on your [organization](https://github.com/apps/rise-risc-v-runners) or [personal account](https://github.com/apps/rise-risc-v-runners-personal), set `runs-on: ubuntu-24.04-riscv` in your workflow, and your jobs run on dedicated RISC-V nodes with full Docker support. No emulation, no cross-compilation.

[RISE](https://riseproject.dev/) provides the service free of charge for open source projects. Visit the [documentation site](https://riseproject-dev.github.io/riscv-runner) for full details.

## Quick start

1. Install the GitHub App: [organization](https://github.com/apps/rise-risc-v-runners) or [personal account](https://github.com/apps/rise-risc-v-runners-personal)
2. Use `runs-on: ubuntu-24.04-riscv` in your workflow:

```yaml
jobs:
  build:
    runs-on: ubuntu-24.04-riscv
    steps:
      - uses: actions/checkout@v4
      - run: uname -m   # prints riscv64
```

## Related repositories

- **[riscv-runner-app](https://github.com/riseproject-dev/riscv-runner-app)**: GitHub App webhook handler and background worker. Receives `workflow_job` events, validates requests, and provisions ephemeral runner pods on Kubernetes.
- **[riscv-runner-images](https://github.com/riseproject-dev/riscv-runner-images)**: Container images for the runner and Docker-in-Docker sidecar. Built for RISC-V and published to the project's container registry.
- **[riscv-runner-device-plugin](https://github.com/riseproject-dev/riscv-runner-device-plugin)**: Kubernetes device plugin and node labeller. Exposes RISC-V board capabilities so the scheduler can place runner pods on matching hardware.
- **[riscv-runner-sample](https://github.com/riseproject-dev/riscv-runner-sample)**: Sample repository demonstrating how to configure a GitHub Actions workflow to use RISC-V runners.
