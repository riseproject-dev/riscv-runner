# RISC-V Runner Ansible Roles & Playbooks

This directory contains Ansible playbooks and roles for configuring Scaleway RISC-V baremetal runners.

## Directory Structure

```text
runner/ansible/
├── ansible.cfg            # Ansible configuration (pipelining, 300s ControlPersist)
├── site.yml               # Main entrypoint playbook
├── group_vars/
│   └── all.yml            # Central versions, checksums, kernel modules, & default variables
└── roles/
    ├── kernelspace/       # Kernel toolchain, compilation, sysctl, & module loading
    └── userspace/         # containerd, node_exporter, prometheus-agent, k8s binaries, & watchdog
```

## Extra Variables

The following variables can be passed at runtime via extra-vars (`-e`):

| Variable | Description | Default |
| :--- | :--- | :--- |
| `cockpit_metrics_push_url` | Scaleway Cockpit metrics remote write URL | `""` |
| `cockpit_metrics_token` | Scaleway Cockpit metrics push authorization secret | `""` |
| `github_probe_token` | Bearer token for `github-probe` health checks | `""` |

## Usage via `scw.py` (Recommended)

`scw.py` automatically discovers target baremetal IPs, retrieves telemetry tokens, and invokes Ansible securely:

```bash
# Full runner setup
.venv/bin/python3 scripts/scw.py runner setup riscv-runner-1

# Kernelspace-only setup
.venv/bin/python3 scripts/scw.py runner setup --kernelspace-only riscv-runner-1

# Userspace-only setup
.venv/bin/python3 scripts/scw.py runner setup --userspace-only riscv-runner-1
```

## Stand-Alone CLI Usage

To run playbooks manually against a baremetal runner without `scw.py`:

```bash
.venv/bin/ansible-playbook \
  -i "62.210.163.200," \
  -u ubuntu \
  --private-key ~/.ssh/id_scw \
  -e "cockpit_metrics_push_url=https://..." \
  -e "cockpit_metrics_token=..." \
  -e "github_probe_token=..." \
  runner/ansible/site.yml
```
