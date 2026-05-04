#!/usr/bin/env python3
"""Provision RISE RISC-V runner control planes and runners on Scaleway."""

import argparse
import itertools
import os
import re
import sys
import time

import logging
# logging.basicConfig(level=logging.INFO)

from enum import StrEnum

from fabric import Connection
from paramiko.ssh_exception import NoValidConnectionsError, SSHException

from invoke.exceptions import UnexpectedExit

from scaleway import Client
from scaleway.instance.v1.custom_api import InstanceUtilsV1API
from scaleway.instance.v1.types import VolumeServerTemplate, VolumeVolumeType, ServerAction
from scaleway.baremetal.v1 import BaremetalV1API
from scaleway.baremetal.v1.content import SERVER_TRANSIENT_STATUSES, SERVER_INSTALL_TRANSIENT_STATUSES
from scaleway.baremetal.v1.types import CreateServerRequestInstall, OfferSubscriptionPeriod
from scaleway.baremetal.v3 import BaremetalV3PrivateNetworkAPI
from scaleway.ipam.v1 import IpamV1API
from scaleway.ipam.v1.types import ResourceType
from scaleway_core.utils import WaitForOptions
from scaleway_core.api import ScalewayException


# --- Constants ---

ZONE = "fr-par-2"
PROJECT_ID = "03a2e06e-e7c1-45a6-9f05-775d813c2e28"
PRIVATE_NETWORK_ID = "58fa41d0-f6a4-4b6f-8f65-b788563842c1" # rpvn-rise-riscv-runner-app

SSH_OPTS = [
    "-o", "StrictHostKeyChecking=no",
    "-o", "UserKnownHostsFile=/dev/null",
]

SSH_KEY_IDS = [
    "ba303e6a-25a5-477b-a823-55dc2d1961a4",  # Ludovic Henry
]


# --- Scaleway SDK clients ---

scw_client = Client.from_config_file_and_env()
scw_client.default_zone = ZONE
scw_client.default_project_id = PROJECT_ID
instance_api = InstanceUtilsV1API(scw_client)
baremetal_api = BaremetalV1API(scw_client)
baremetal_pn_api = BaremetalV3PrivateNetworkAPI(scw_client)
ipam_api = IpamV1API(scw_client)


class ProvisioningException(Exception):
    pass


# --- SSH helpers via fabric ---

def ssh_connect(host, user, retries=30, delay=30):
    """Wait for SSH to be available and return a fabric Connection."""
    assert host, "host must be defined"
    assert user, "user must be defined"
    for attempt in range(retries):
        try:
            conn = Connection(
                host,
                user=user,
                connect_kwargs={
                    "key_filename": "/Users/luhenry/.ssh/id_rivos",
                },
            )
            conn.run("true")
            return conn
        except (NoValidConnectionsError, SSHException, OSError, TimeoutError) as e:
            print(f"SSH not ready (attempt {attempt + 1}/{retries}), error: \"{e}\". Retrying in {delay}s...")
            time.sleep(delay)
    raise RuntimeError(f"SSH to {user}@{host} not available after {retries} attempts")


# --- IPAM helpers ---

def get_private_ip_for_nic(nic_id):
    """Get the IPv4 address assigned to an instance private NIC via IPAM."""
    resp = ipam_api.list_i_ps(
        resource_id=nic_id,
        resource_type=ResourceType.INSTANCE_PRIVATE_NIC,
        is_ipv6=False,
    )
    for ip in resp.ips:
        if not ip.is_ipv6:
            return ip.address.split("/")[0]

    raise ProvisioningException(f"No IPv4 address assigned via IPAM for NIC {nic_id}")


# --- Private network result types ---

class PrivateNetwork:
    def __init__(self, ip):
        self.ip = ip

class InstancePrivateNetwork(PrivateNetwork):
    def __init__(self, ip):
        super().__init__(ip)

class BareMetalPrivateNetwork(PrivateNetwork):
    def __init__(self, ip, vlan_id):
        super().__init__(ip)
        self.vlan_id = vlan_id


# --- Server wrappers ---

class Instance:
    def __init__(self, id):
        self.id = id

    @staticmethod
    def create(hostname, server_type: str, storage_size: int, cloud_init_script: str):
        resp = instance_api.create_server(
            commercial_type=server_type,
            name=hostname,
            image="ubuntu_noble",
            volumes={"0": VolumeServerTemplate(
                volume_type=VolumeVolumeType.SBS_VOLUME,
                size=storage_size,
            )},
        )
        server_id = resp.server.id

        # Set cloud-init user data
        instance_api.set_server_user_data(
            server_id=server_id,
            key="cloud-init",
            content=cloud_init_script.encode(),
        )

        # Power on the server and wait for it to be running
        instance_api.server_action(server_id=server_id, action=ServerAction.POWERON)
        instance_api.wait_instance_server(server_id=server_id, zone=ZONE) # it doesn't take zone from default

        return Instance(server_id)

    def get_public_ip(self):
        resp = instance_api.get_server(server_id=self.id)
        server = resp.server
        if server.public_ip and server.public_ip.address:
            return server.public_ip.address
        for ip in (server.public_ips or []):
            if ip.address:
                return ip.address
        raise RuntimeError(f"No public IP found for instance {self.id}")

    def attach_private_network(self):
        resp = instance_api.create_private_nic(
            server_id=self.id,
            private_network_id=PRIVATE_NETWORK_ID,
        )
        nic_id = resp.private_nic.id
        ip = get_private_ip_for_nic(nic_id)
        return InstancePrivateNetwork(ip)

    def delete(self):
        instance_api.server_action(server_id=self.id, action=ServerAction.TERMINATE)


class BareMetal:
    def __init__(self, id):
        self.id = id

    @staticmethod
    def create(hostname, server_type, os_id, tags=None):
        # Look up the offer ID by name
        offer_id = None
        for offer in baremetal_api.list_offers(zone=ZONE, subscription_period=OfferSubscriptionPeriod.MONTHLY).offers:
            if offer.name == server_type:
                offer_id = offer.id
                break
        if not offer_id:
            raise RuntimeError(f"Offer '{server_type}' not found")

        server = baremetal_api.create_server(
            name=hostname,
            description="",
            protected=False,
            offer_id=offer_id,
            tags=tags or [],
            install=CreateServerRequestInstall(
                os_id=os_id,
                hostname=hostname,
                ssh_key_ids=SSH_KEY_IDS,
            ),
        )

        return BareMetal(server.id)

    def start(self):
        baremetal_api.start_server(server_id=self.id)

    def get_public_ip(self):
        server = baremetal_api.get_server(server_id=self.id)
        for ip in (server.ips or []):
            if ip.version == "IPv4":
                return ip.address
        raise ProvisioningException(f"No IPv4 address found for baremetal server {self.id}")

    def attach_private_network(self):
        # Enable private network option
        options_resp = baremetal_api.list_options(zone=ZONE)
        option_id = None
        for option in options_resp.options:
            if option.name == "Private Network":
                option_id = option.id
                break
        if not option_id:
            raise ProvisioningException("Private Network option not found")

        try:
            baremetal_api.add_option_server(server_id=self.id, option_id=option_id)
        except ScalewayException:
            # Ignore if the option is already on the server
            pass

        time.sleep(1) # there are timing issues sometimes leading to 500

        # Attach to the private network
        pn = baremetal_pn_api.add_server_private_network(
            server_id=self.id,
            private_network_id=PRIVATE_NETWORK_ID,
        )

        for ipam_ip_id in (pn.ipam_ip_ids or []):
            ip_info = ipam_api.get_ip(ip_id=ipam_ip_id)
            if not ip_info.is_ipv6:
                return BareMetalPrivateNetwork(ip_info.address, pn.vlan)

        raise ProvisioningException(f"No private IPv4 address assigned for baremetal server {self.id}")

    def get_private_network(self):
        pn_resp = baremetal_pn_api.list_server_private_networks(
            server_id=self.id,
        )
        for pn in pn_resp.server_private_networks:
            for ipam_ip_id in (pn.ipam_ip_ids or []):
                ip_info = ipam_api.get_ip(ip_id=ipam_ip_id)
                if not ip_info.is_ipv6:
                    return BareMetalPrivateNetwork(ip_info.address, pn.vlan)
        raise ProvisioningException(f"No private IPv4 address found for baremetal server {self.id}")

    def update_tags(self, tags):
        baremetal_api.update_server(server_id=self.id, tags=tags)

    def reinstall(self, os_id, hostname):
        baremetal_api.install_server(
            server_id=self.id,
            os_id=os_id,
            hostname=hostname,
            ssh_key_ids=SSH_KEY_IDS,
        )

    def delete(self):
        baremetal_api.delete_server(server_id=self.id)

    def wait_for_server(self):
        def is_ready(res):
            ready = res.status not in SERVER_TRANSIENT_STATUSES and res.install.status not in SERVER_INSTALL_TRANSIENT_STATUSES
            print(f"  server status = {res.status}, server install status = {res.install.status}, {"ready!" if ready else "not ready yet!"}")
            return ready

        time.sleep(5) # there can be a race condition between the previous operation
                      # and waiting for the server, add an artificial sleep to allow
                      # scaleway's backend to sync up
        baremetal_api.wait_for_server(
            server_id=self.id,
            options=WaitForOptions(
                timeout=15*60, # 15 minutes
                stop=is_ready,
            ),
        )


# =============================================================================
# Runner provisioning
# =============================================================================

RUNNER_SERVER_TYPE = "EM-RV1-C4M16S128-A"

RETRY_DELAY = 60

SETUP_SCRIPT = r"""
# Redirect stdout and stderr to /var/log/riscv-runner-setup.log
exec > >(sudo tee -a /var/log/riscv-runner-setup.log) 2>&1

echo "Setup @ $(date)"

set -euxo pipefail

# Fresh packages
sudo apt update -qq
sudo apt upgrade -qq -y

# Load required kernel modules
cat <<EOF | sudo tee /etc/modules-load.d/k8s.conf
overlay
br_netfilter
nf_conntrack
tun
EOF

sudo modprobe overlay
sudo modprobe br_netfilter
sudo modprobe nf_conntrack
sudo modprobe tun

# Configure sysctl params for Kubernetes networking
cat <<EOF | sudo tee /etc/sysctl.d/k8s.conf
net.bridge.bridge-nf-call-iptables  = 1
net.bridge.bridge-nf-call-ip6tables = 1
net.ipv4.ip_forward                 = 1
EOF

# Apply the changes
sudo sysctl --system

# # Configure private network VLAN interface
# cat <<'EOF' | sudo tee -a /etc/systemd/network/05-end0.network
# [Match]
# Name=end0
# [Network]
# DHCP=yes
# VLAN=end0.@@PN_VLAN_ID@@
# EOF

# cat <<'EOF' | sudo tee /etc/systemd/network/10-end0.@@PN_VLAN_ID@@.netdev
# [NetDev]
# Name=end0.@@PN_VLAN_ID@@
# Kind=vlan
# [VLAN]
# Id=@@PN_VLAN_ID@@
# EOF

# cat <<'EOF' | sudo tee /etc/systemd/network/11-end0.@@PN_VLAN_ID@@.network
# [Match]
# Name=end0.@@PN_VLAN_ID@@
# [Network]
# Address=@@PN_IP@@
# EOF

# sudo networkctl reload

# Check that it succeeded
# sudo apt install -qq -y --no-install-recommends retry
# retry --delay=2 --times=5 -- ip addr show end0.@@PN_VLAN_ID@@

# # Configure private network VLAN interface
# sudo ip link add link end0 name end0.@@PN_VLAN_ID@@ type vlan id @@PN_VLAN_ID@@
# sudo ip link set end0.@@PN_VLAN_ID@@ up
# sudo ip addr add @@PN_IP@@ dev end0.@@PN_VLAN_ID@@

# Install containerd
sudo apt install -qq -y --no-install-recommends containerd
sudo mkdir -p /etc/containerd
containerd config default | sudo tee /etc/containerd/config.toml > /dev/null

## Enable SystemdCgroup driver
sudo sed -i 's/SystemdCgroup = false/SystemdCgroup = true/g' /etc/containerd/config.toml

## Set the multi-arch (amd64/riscv64) compatible pause image
## This ensures that both architectures can pull a valid sandbox image
sudo sed -i 's|sandbox_image = ".*"|sandbox_image = "cloudv10x/pause:3.10"|' /etc/containerd/config.toml

## Restart the service
sudo systemctl restart containerd

# Install crictl
CRICTL_VERSION="v1.35.0" # https://github.com/kubernetes-sigs/cri-tools/releases/tag/v1.35.0
curl -fsSL \
  --retry 5 \
  --retry-delay 5 \
  --retry-all-errors \
  https://github.com/kubernetes-sigs/cri-tools/releases/download/${CRICTL_VERSION}/crictl-${CRICTL_VERSION}-linux-$(uname -m).tar.gz | \
    sudo tar -C /usr/local/bin -xvzf -

# Install kubernetes cli tools: kubeadm, kubelet, kubectl
sudo apt install -qq -y --no-install-recommends curl unzip
curl -fsSL \
  --retry 5 \
  --retry-delay 5 \
  --retry-all-errors \
  -H "User-Agent: Mozilla/5.0 (X11; Linux x86_64; rv:128.0) Gecko/20100101 Firefox/128.0" \
  -H "Accept: */*" \
  -H "Referer: https://gitlab.com/" \
  -o artifacts.zip \
  https://gitlab.com/riseproject/risc-v-runner/kubernetes/-/jobs/13257210986/artifacts/download
unzip artifacts.zip '_output/*' -d artifacts
sudo mv artifacts/_output/local/go/bin/kube* /usr/local/bin/
rm -rf artifacts artifacts.zip
sudo chown root:root /usr/local/bin/kube*
sudo chmod +x /usr/local/bin/kube*

# Install CNI plugins
sudo mkdir -p /opt/cni/bin
curl -fsSL \
  --retry 5 \
  --retry-delay 5 \
  --retry-all-errors \
  https://github.com/containernetworking/plugins/releases/download/v1.4.0/cni-plugins-linux-riscv64-v1.4.0.tgz | \
    sudo tar -C /opt/cni/bin -xvzf -

# Setup kubelet systemd service
cat <<'EOF' | sudo tee /etc/systemd/system/kubelet.service
[Unit]
Description=kubelet: The Kubernetes Node Agent
Documentation=https://kubernetes.io/docs/
Wants=network-online.target
After=network-online.target

[Service]
ExecStart=/usr/local/bin/kubelet
Restart=always
StartLimitInterval=0
RestartSec=10

[Install]
WantedBy=multi-user.target
EOF

sudo mkdir -p /etc/systemd/system/kubelet.service.d

cat <<'EOF' | sudo tee /etc/systemd/system/kubelet.service.d/10-kubeadm.conf
[Service]
Environment="KUBELET_KUBECONFIG_ARGS=--bootstrap-kubeconfig=/etc/kubernetes/bootstrap-kubelet.conf --kubeconfig=/etc/kubernetes/kubelet.conf"
Environment="KUBELET_CONFIG_ARGS=--config=/var/lib/kubelet/config.yaml"
EnvironmentFile=-/var/lib/kubelet/kubeadm-flags.env
EnvironmentFile=-/etc/default/kubelet
ExecStart=
ExecStart=/usr/local/bin/kubelet $KUBELET_KUBECONFIG_ARGS $KUBELET_CONFIG_ARGS $KUBELET_KUBEADM_ARGS $KUBELET_EXTRA_ARGS
EOF

sudo systemctl daemon-reload
sudo systemctl enable --now kubelet

# Join the cluster (uses the control plane's private network IP)
sudo kubeadm reset -f || true
sudo @@KUBEADM_JOIN_CMD@@

# Mandatory reboot for fresh nodes to finalize networking and cgroups
sudo reboot
"""

class ServerNotFoundException(Exception):
    pass


def get_control_plane_host(control_plane_name):
    """Returns (public_ip, private_ip)."""
    resp = instance_api.list_servers(name=control_plane_name)
    for server in resp.servers:
        if server.name == control_plane_name:
            # Get public IP
            public_ip = None
            if server.public_ip and server.public_ip.address:
                public_ip = server.public_ip.address
            if not public_ip:
                for ip in (server.public_ips or []):
                    if ip.address:
                        public_ip = ip.address
                        break
            if not public_ip:
                raise RuntimeError(f"Control plane '{control_plane_name}' has no public IP")

            # Get private IP from the private NIC via IPAM
            private_ip = None
            for nic in (server.private_nics or []):
                ip_resp = ipam_api.list_i_ps(
                    resource_id=nic.id,
                    resource_type=ResourceType.INSTANCE_PRIVATE_NIC,
                    project_id=PROJECT_ID,
                    is_ipv6=False,
                )
                for ip_info in ip_resp.ips:
                    if not ip_info.is_ipv6:
                        private_ip = ip_info.address.split("/")[0]
                        break
                if private_ip:
                    break
            if not private_ip:
                raise RuntimeError(f"Control plane '{control_plane_name}' has no private IP")

            return public_ip, private_ip
    raise ServerNotFoundException(f"Control plane '{control_plane_name}' not found in project {PROJECT_ID}")


def get_os_id():
    resp = baremetal_api.list_os()
    for os_entry in resp.os:
        if os_entry.name == "Ubuntu" and os_entry.version == "24.04 LTS (Noble Numbat)":
            return os_entry.id
    raise RuntimeError("Ubuntu 24.04 LTS OS not found")


def get_kubeadm_join_cmd(ssh_cp, cp_ip):
    # Create a short-lived token
    result = ssh_cp.run("kubeadm token create --ttl 15m", hide=True)
    token = result.stdout.strip()

    # Get the CA cert hash
    result = ssh_cp.run(
        "openssl x509 -pubkey -in /etc/kubernetes/pki/ca.crt"
        " | openssl rsa -pubin -outform der 2>/dev/null"
        " | openssl dgst -sha256 -hex"
        " | sed 's/^.* //'",
        hide=True,
    )
    ca_cert_hash = result.stdout.strip()

    return f"kubeadm join {cp_ip}:6443 --token {token} --discovery-token-ca-cert-hash sha256:{ca_cert_hash}"


def run_setup(ssh, pn, ssh_cp, cp_public_ip):
    join_cmd = get_kubeadm_join_cmd(ssh_cp, cp_public_ip)
    script = SETUP_SCRIPT.replace("@@KUBEADM_JOIN_CMD@@", join_cmd) \
                         #FIXME(pn): enable private address again
                         # .replace("@@PN_IP@@", pn.ip)
                         # .replace("@@PN_VLAN_ID@@", pn.vlan_id)
    ssh.run(script)


def find_server_by_name(hostname):
    resp = baremetal_api.list_servers(name=hostname)
    for server in resp.servers:
        if server.name == hostname:
            return server
    raise ServerNotFoundException(f"Server '{hostname}' not found in project {PROJECT_ID}")


def drain_and_delete_k8s_node(hostname, ssh_cp):
    ssh_cp.run(
        f"kubectl --kubeconfig=/etc/kubernetes/admin.conf drain {hostname} --ignore-daemonsets --delete-emptydir-data --force --timeout=0",
        warn=True,
    )
    ssh_cp.run(
        f"kubectl --kubeconfig=/etc/kubernetes/admin.conf delete node {hostname} --ignore-not-found",
    )


def wait_for_k8s_node(hostname, ssh_cp):
    while True:
        try:
            result = ssh_cp.run(f"kubectl --kubeconfig=/etc/kubernetes/admin.conf get node {hostname} --no-headers -o name", hide='both')
            assert result.exited == 0
            print(f"  node {hostname} available but not ready yet!")
            break
        except UnexpectedExit:
            print(f"  node {hostname} not available yet!")
            time.sleep(15)

    ssh_cp.run(
        f"kubectl --kubeconfig=/etc/kubernetes/admin.conf wait --for=condition=Ready node/{hostname} --timeout=600s", hide='out'
    )
    print(f"  node {hostname} available and ready!")


def create_server(hostname, os_id, tags=None):
    while True:
        try:
            return BareMetal.create(hostname, RUNNER_SERVER_TYPE, os_id, tags=tags)
        except Exception:
            print(f"Server creation failed, retrying in {RETRY_DELAY}s...")
            time.sleep(RETRY_DELAY)


def get_next_runner_index():
    prefix = "riscv-runner-"
    pattern = re.compile(rf"^{re.escape(prefix)}(\d+)$")
    used = set()
    # baremetal_api.list_servers uses pagination
    for page in itertools.count(start=0):
        resp = baremetal_api.list_servers(page=page)
        if len(resp.servers) == 0:
            break
        for server in resp.servers:
            m = pattern.match(server.name or "")
            if m:
                used.add(int(m.group(1)))
    i = 0
    while i in used:
        i += 1
    return i


def cmd_runner_create(args):
    os_id = get_os_id()
    print(f"Using OS ID: {os_id}")

    for _ in range(args.count):
        index = get_next_runner_index()
        runner = f"riscv-runner-{index}"
        print(f"\n{'='*60}")
        print(f"Creating runner {runner}")
        print(f"{'='*60}")

        control_plane = args.control_plane

        try:
            cp_public_ip, cp_private_ip = get_control_plane_host(control_plane)
            print(f"Using control plane: {control_plane} (public: {cp_public_ip}, private: {cp_private_ip})")
        except ServerNotFoundException:
            print(f"Failed to find control plane {control_plane}")
            sys.exit(1)

        ssh_cp = ssh_connect(host=cp_public_ip, user="root")

        tags = [f"control-plane:{control_plane}"]
        print(f"Provisioning {runner}")
        server = create_server(runner, os_id, tags=tags)
        server.wait_for_server()
        print(f"Server created: {server.id}")

        #FIXME(pn): Disable private network for now, it doesn't work reliably enough
        # pn = server.attach_private_network()
        # print(f"Private network enabled (VLAN {pn.vlan_id}, IP {pn.ip})")
        pn = None

        print(f"Starting {runner}...")
        server.start()
        server.wait_for_server()
        ip = server.get_public_ip()
        print(f"Server IP: {ip}")

        ssh = ssh_connect(host=ip, user="ubuntu")
        run_setup(ssh, pn, ssh_cp, cp_public_ip)

        print(f"Waiting for node {runner} to be ready in k8s")
        wait_for_k8s_node(runner, ssh_cp)

        print(f"Server {runner} provisioned")


def cmd_runner_reinstall(args):
    os_id = get_os_id()
    print(f"Using OS ID: {os_id}")

    for runner in args.runners:
        print(f"\n{'='*60}")
        print(f"Reinstalling runner {runner}")
        print(f"{'='*60}")

        server = find_server_by_name(runner)
        print(f"Found existing server: {server.id}")

        control_plane = next(tag[14:] for tag in server.tags if tag.startswith("control-plane:"))
        if not control_plane:
            print(f"Failing to process {runner}: missing 'control-plane:*' tag, tags = [{",".join(server.tags)}]")
            sys.exit(1)

        cp_public_ip = None
        cp_private_ip = None
        try:
            cp_public_ip, cp_private_ip = get_control_plane_host(control_plane)
            print(f"Using control plane: {control_plane} (public: {cp_public_ip}, private: {cp_private_ip})")
        except ServerNotFoundException:
            if args.to_control_plane and args.to_control_plane != control_plane:
                # Maybe the next TO control plane is working
                pass
            else:
                print(f"Failed to find control plane {control_plane}")
                sys.exit(1)

        if cp_public_ip:
            ssh_cp = ssh_connect(host=cp_public_ip, user="root")

            print(f"Draining and removing {runner} from k8s")
            drain_and_delete_k8s_node(runner, ssh_cp)
            print(f"Drained and removed {runner} from k8s")

        server = BareMetal(server.id)

        # We are switching the runner to a different control plane
        if args.to_control_plane:
            if args.to_control_plane == control_plane:
                print(f"WARNING! Using the same source and destination control plane, is that expected?")
            else:
                control_plane = args.to_control_plane
                cp_public_ip, cp_private_ip = get_control_plane_host(control_plane)
                print(f"Switching control plane: {control_plane} (public: {cp_public_ip}, private: {cp_private_ip})")
                ssh_cp = ssh_connect(host=cp_public_ip, user="root")

                # Update the control-plane tag
                server.update_tags([f"control-plane:{control_plane}"])

        print(f"Reinstalling OS on {runner}...")
        server.reinstall(os_id, runner)
        server.wait_for_server()
        print(f"OS reinstalled on {runner}")

        #FIXME(pn): Disable private network for now, it doesn't work reliably enough
        # try:
        #     pn = server.get_private_network()
        # except ProvisioningException:
        #     pn = server.attach_private_network()
        # print(f"Private IP: {pn.ip}, vlan={pn.vlan_id}")
        pn = None

        ip = server.get_public_ip()
        print(f"Public IP: {ip}")

        ssh = ssh_connect(host=ip, user="ubuntu")
        run_setup(ssh, pn, ssh_cp, cp_public_ip)

        print(f"Waiting for node {runner} to be ready on k8s")
        wait_for_k8s_node(runner, ssh_cp)

        print(f"Server {runner} provisioned")


def cmd_runner_setup(args):
    for runner in args.runners:
        print(f"\n{'='*60}")
        print(f"Setting up runner {runner}")
        print(f"{'='*60}")

        server = find_server_by_name(runner)
        print(f"Found existing server: {server.id}")

        control_plane = next(tag[14:] for tag in server.tags if tag.startswith("control-plane:"))
        if not control_plane:
            print(f"Failing to process {runner}: missing 'control-plane:*' tag, tags = [{",".join(server.tags)}]")
            sys.exit(1)

        cp_public_ip = None
        cp_private_ip = None
        try:
            cp_public_ip, cp_private_ip = get_control_plane_host(control_plane)
            print(f"Using control plane: {control_plane} (public: {cp_public_ip}, private: {cp_private_ip})")
        except ServerNotFoundException:
            if args.to_control_plane and args.to_control_plane != control_plane:
                # Maybe the next TO control plane is working
                pass
            else:
                print(f"Failed to find control plane {control_plane}")
                sys.exit(1)

        if cp_public_ip:
            ssh_cp = ssh_connect(host=cp_public_ip, user="root")

            print(f"Draining and removing {runner} from k8s")
            drain_and_delete_k8s_node(runner, ssh_cp)
            print(f"Drained and removed {runner} from k8s")

        server = BareMetal(server.id)

        # We are switching the runner to a different control plane
        if args.to_control_plane:
            if args.to_control_plane == control_plane:
                print(f"WARNING! Using the same source and destination control plane, is that expected?")
            else:
                control_plane = args.to_control_plane
                cp_public_ip, cp_private_ip = get_control_plane_host(control_plane)
                print(f"Switching control plane: {control_plane} (public: {cp_public_ip}, private: {cp_private_ip})")
                ssh_cp = ssh_connect(host=cp_public_ip, user="root")

                # Update the control-plane tag
                server.update_tags([f"control-plane:{control_plane}"])

        #FIXME(pn): Disable private network for now, it doesn't work reliably enough
        # try:
        #     pn = server.get_private_network()
        # except ProvisioningException:
        #     pn = server.attach_private_network()
        # print(f"Private IP: {pn.ip}, vlan={pn.vlan_id}")
        pn = None

        ip = server.get_public_ip()
        print(f"Public IP: {ip}")

        ssh = ssh_connect(host=ip, user="ubuntu")
        run_setup(ssh, pn, ssh_cp, cp_public_ip)

        print(f"Waiting for node {runner} to be ready on k8s")
        wait_for_k8s_node(runner, ssh_cp)

        print(f"Server {runner} provisioned")


def cmd_runner_list(args):
    tag = f"control-plane:{args.control_plane}"
    servers = baremetal_api.list_servers_all(tags=[tag])

    rows = []
    for s in servers:
        install_status = s.install.status if s.install else "unknown"
        tags = ",".join(s.tags)
        rows.append((s.id, s.name, s.status, install_status, tags, s.ping_status))

    # Compute column widths
    headers = ("ID", "NAME", "STATUS", "INSTALL", "TAGS", "PING")
    widths = [len(h) for h in headers]
    for row in rows:
        for i, val in enumerate(row):
            widths[i] = max(widths[i], len(str(val)))

    fmt = "  ".join(f"{{:<{w}}}" for w in widths)
    print(fmt.format(*headers))
    for row in rows:
        print(fmt.format(*[str(v) for v in row]))


def cmd_runner_delete(args):
    for runner in args.runners:
        print(f"\n{'='*60}")
        print(f"Deleting runner {runner}")
        print(f"{'='*60}")

        server = find_server_by_name(runner)
        print(f"Found server: {server.id}")

        control_plane = next(tag[14:] for tag in server.tags if tag.startswith("control-plane:"))
        if not control_plane:
            print(f"Failing to process {runner}: missing 'control-plane:*' tag, tags = [{",".join(server.tags)}]")
            sys.exit(1)

        cp_public_ip, cp_private_ip = get_control_plane_host(control_plane)
        print(f"Using control plane: {control_plane} (public: {cp_public_ip}, private: {cp_private_ip})")
        ssh_cp = ssh_connect(host=cp_public_ip, user="root")

        print(f"Draining and removing {runner} from k8s")
        drain_and_delete_k8s_node(runner, ssh_cp)
        print(f"Drained and removed {runner} from k8s")

        server = BareMetal(server.id)
        server.delete()
        print(f"Server {runner} deleted")


# =============================================================================
# Control plane provisioning
# =============================================================================

CONTROL_PLANE_SERVER_TYPE = "POP2-2C-8G"
BLOCK_STORAGE_SIZE = 50 * 1_000_000_000

CLOUD_INIT = r"""#cloud-config
write_files:
  - path: /etc/modules-load.d/k8s.conf
    owner: root:root
    permissions: "0644"
    content: |
      overlay
      br_netfilter

  - path: /etc/sysctl.d/k8s.conf
    owner: root:root
    permissions: "0644"
    content: |
      net.bridge.bridge-nf-call-iptables  = 1
      net.bridge.bridge-nf-call-ip6tables = 1
      net.ipv4.ip_forward                 = 1

  - path: /etc/kubernetes/clusterroles.yml
    owner: root:root
    permissions: "0644"
    content: |
      apiVersion: v1
      # luhenry
      items:
      - apiVersion: rbac.authorization.k8s.io/v1
        kind: ClusterRoleBinding
        metadata:
          name: luhenry-cluster-admin-binding
        roleRef:
          apiGroup: rbac.authorization.k8s.io
          kind: ClusterRole
          name: cluster-admin
        subjects:
        - apiGroup: rbac.authorization.k8s.io
          kind: User
          name: luhenry
      # gh-app
      - apiVersion: rbac.authorization.k8s.io/v1
        kind: ClusterRoleBinding
        metadata:
          name: gh-app-edit-binding
        roleRef:
          apiGroup: rbac.authorization.k8s.io
          kind: ClusterRole
          name: edit
        subjects:
        - apiGroup: rbac.authorization.k8s.io
          kind: User
          name: gh-app
      - apiVersion: rbac.authorization.k8s.io/v1
        kind: ClusterRole
        metadata:
          name: gh-app-node-reader
        rules:
        - apiGroups:
          - ""
          resources:
          - nodes
          verbs:
          - list
      - apiVersion: rbac.authorization.k8s.io/v1
        kind: ClusterRoleBinding
        metadata:
          name: gh-app-node-reader-binding
        roleRef:
          apiGroup: rbac.authorization.k8s.io
          kind: ClusterRole
          name: gh-app-node-reader
        subjects:
        - apiGroup: rbac.authorization.k8s.io
          kind: User
          name: gh-app
      # gh-deploy
      - apiVersion: rbac.authorization.k8s.io/v1
        kind: ClusterRoleBinding
        metadata:
          name: gh-deploy-cluster-admin-binding
        roleRef:
          apiGroup: rbac.authorization.k8s.io
          kind: ClusterRole
          name: cluster-admin
        subjects:
        - apiGroup: rbac.authorization.k8s.io
          kind: User
          name: gh-deploy
      kind: List

package_update: true
package_upgrade: true

packages:
  - containerd
  - apt-transport-https
  - ca-certificates
  - curl
  - gpg
  - jq
  - socat
  - conntrack
  - ebtables
  - ipset

bootcmd:
  - modprobe overlay
  - modprobe br_netfilter

runcmd:
  - sysctl --system

  # Configure containerd
  - |
    set -eux
    mkdir -p /etc/containerd
    containerd config default > /etc/containerd/config.toml
    sed -i 's/SystemdCgroup = false/SystemdCgroup = true/g' /etc/containerd/config.toml
    sed -i 's|sandbox_image = ".*"|sandbox_image = "cloudv10x/pause:3.10"|' /etc/containerd/config.toml
    systemctl restart containerd

  # Install kubelet, kubeadm, kubectl from official apt repo
  - |
    set -eux
    curl -fsSL https://pkgs.k8s.io/core:/stable:/v1.35/deb/Release.key | gpg --dearmor -o /etc/apt/keyrings/kubernetes-apt-keyring.gpg
    echo 'deb [signed-by=/etc/apt/keyrings/kubernetes-apt-keyring.gpg] https://pkgs.k8s.io/core:/stable:/v1.35/deb/ /' > /etc/apt/sources.list.d/kubernetes.list
    apt-get update
    apt-get install -y kubelet kubeadm kubectl
    apt-mark hold kubelet kubeadm kubectl

  # Initialize the cluster on the private network IP
  - |
    set -eux

    # Discover IPs from the Scaleway metadata service
    METADATA=$(curl -fsSL http://169.254.42.42/conf?format=json)
    PUBLIC_IP=$(echo "${METADATA}" | jq -r '.public_ip.address')

    # Get the private NIC MAC address, find the matching interface, extract its IP
    PRIVATE_MAC=$(echo "${METADATA}" | jq -r '.private_nics[0].mac_address')
    PRIVATE_IFACE=$(ip -o link | grep "${PRIVATE_MAC}" | awk -F': ' '{print $2}')
    PRIVATE_IP=$(ip -4 addr show "${PRIVATE_IFACE}" | grep -oP '(?<=inet\s)[\d.]+' | head -1)

    echo "Public IP:  ${PUBLIC_IP}"
    echo "Private IP: ${PRIVATE_IP}"

    kubeadm init \
      --pod-network-cidr=10.244.0.0/16 \
      --apiserver-advertise-address="${PUBLIC_IP}" \
      --apiserver-cert-extra-sans="${PRIVATE_IP}"

    export KUBECONFIG=/etc/kubernetes/admin.conf

    # Deploy Flannel CNI
    kubectl apply -f https://github.com/flannel-io/flannel/releases/latest/download/kube-flannel.yml

    # Switch kube-proxy to the multi-arch compatible image
    kubectl set image daemonset/kube-proxy -n kube-system kube-proxy=cloudv10x/kube-proxy:1.35.0

    # Create user kubeconfigs (these will use the private IP as server address;
    # the script replaces it with the public IP when printing)
    kubeadm kubeconfig user --client-name=luhenry   > /etc/kubernetes/kubeconfig-luhenry.conf
    kubeadm kubeconfig user --client-name=gh-deploy > /etc/kubernetes/kubeconfig-gh-deploy.conf
    kubeadm kubeconfig user --client-name=gh-app    > /etc/kubernetes/kubeconfig-gh-app.conf

    # Apply cluster roles
    kubectl apply -f /etc/kubernetes/clusterroles.yml

    # Apply device plugin DaemonSets
    curl -fsSL https://raw.githubusercontent.com/riseproject-dev/riscv-runner-device-plugin/refs/heads/@@ENVIRONMENT@@/k8s-ds-device-plugin.yaml | env TAG="@@ENVIRONMENT@@" envsubst | kubectl apply -f -
    curl -fsSL https://raw.githubusercontent.com/riseproject-dev/riscv-runner-device-plugin/refs/heads/@@ENVIRONMENT@@/k8s-ds-node-labeller.yaml | env TAG="@@ENVIRONMENT@@" envsubst | kubectl apply -f -
"""


def get_next_instance_index(staging):
    resp = instance_api.list_servers()
    prefix = "riscv-runner-control-plane-" + ("staging-" if staging else "")
    pattern = re.compile(rf"^{re.escape(prefix)}(\d+)$")
    used = set()
    for server in resp.servers:
        m = pattern.match(server.name or "")
        if m:
            used.add(int(m.group(1)))
    i = 0
    while i in used:
        i += 1
    return i


def cmd_control_plane_create(args):
    staging = args.staging
    index = get_next_instance_index(staging)
    hostname = f"riscv-runner-control-plane%s-{index}" % ("-staging" if staging else "")

    print(f"\n{'='*60}")
    print(f"Creating control plane {hostname}")
    print(f"{'='*60}")

    environment = "staging" if staging else "main"

    cloud_init = CLOUD_INIT.replace("@@ENVIRONMENT@@", environment)

    server = Instance.create(hostname, CONTROL_PLANE_SERVER_TYPE, BLOCK_STORAGE_SIZE, cloud_init)
    print(f"Server created: {server.id}")

    # Attach to private network (cloud-init discovers the IP at runtime)
    server.attach_private_network()
    print("Private network attached")

    public_ip = server.get_public_ip()
    print(f"Public IP: {public_ip}")

    ssh = ssh_connect(host=public_ip, user="root")

    print("Waiting for cloud-init to complete...")
    ssh.run("cloud-init status --wait", hide=False)

    # Get the private IP that cloud-init discovered
    result = ssh.run("ip -4 addr show scope global", hide=True)
    all_ips = re.findall(r'inet ([\d.]+)', result.stdout)
    private_ip = next((ip for ip in all_ips if ip != public_ip), None)

    # Fetch and print kubeconfigs, replacing private IP with public IP
    # so they're usable from outside the private network
    print(f"\n{'='*60}")
    print("Kubeconfig for luhenry:")
    print(f"{'='*60}")
    result = ssh.run("cat /etc/kubernetes/kubeconfig-luhenry.conf", hide=True)
    print(result.stdout)


    print(f"Run the following commands to update GitHub secrets:")
    print(f"(set -o pipefail; ssh root@{public_ip} cat /etc/kubernetes/kubeconfig-gh-deploy.conf | gh secret set K8S_KUBECONFIG --repo riseproject-dev/riscv-runner-images --env {environment})")
    print(f"(set -o pipefail; ssh root@{public_ip} cat /etc/kubernetes/kubeconfig-gh-deploy.conf | gh secret set K8S_KUBECONFIG --repo riseproject-dev/riscv-runner-device-plugin --env {environment})")
    print(f"(set -o pipefail; ssh root@{public_ip} cat /etc/kubernetes/kubeconfig-gh-app.conf | gh secret set K8S_KUBECONFIG --repo riseproject-dev/riscv-runner-app --env {environment})")

    print(f"\n{'='*60}")
    print(f"Control plane {hostname} provisioned successfully")
    print(f"Public IP:  {public_ip}")
    if private_ip:
        print(f"Private IP: {private_ip}")
    print(f"{'='*60}")


# =============================================================================
# CLI
# =============================================================================

def main():
    parser = argparse.ArgumentParser(description="Provision RISE RISC-V runner infrastructure on Scaleway")
    resource_subparsers = parser.add_subparsers(dest="resource", required=True)

    # runner ...
    runner_parser = resource_subparsers.add_parser("runner", help="Manage RISC-V runner bare metal nodes")
    runner_subparsers = runner_parser.add_subparsers(dest="command", required=True)

    runner_create = runner_subparsers.add_parser("create", help="Create new runners")
    runner_create.add_argument("--control-plane", type=str, required=True, help="Name of the control plane instance")
    runner_create.add_argument("count", nargs="?", type=int, default=1, help="Number of new runners to create")
    runner_create.set_defaults(func=cmd_runner_create)

    runner_list = runner_subparsers.add_parser("list", help="List runners")
    runner_list.add_argument("--control-plane", type=str, required=True, help="Name of the control plane instance")
    runner_list.set_defaults(func=cmd_runner_list)

    runner_reinstall = runner_subparsers.add_parser("reinstall", help="Reinstall OS on existing runners")
    runner_reinstall.add_argument("--to-control-plane", type=str, help="Name of the control plane instance to switch the runner to")
    runner_reinstall.add_argument("runners", nargs="+", type=str, help="Runner to reinstall")
    runner_reinstall.set_defaults(func=cmd_runner_reinstall)

    runner_setup = runner_subparsers.add_parser("setup", help="Setup existing runners")
    runner_setup.add_argument("--to-control-plane", type=str, help="Name of the control plane instance to switch the runner to")
    runner_setup.add_argument("runners", nargs="+", type=str, help="Runner to reinstall")
    runner_setup.set_defaults(func=cmd_runner_setup)

    runner_delete = runner_subparsers.add_parser("delete", help="Delete existing runners")
    runner_delete.add_argument("runners", nargs="+", type=str, help="Runners to delete")
    runner_delete.set_defaults(func=cmd_runner_delete)

    # control-plane ...
    cp_parser = resource_subparsers.add_parser("control-plane", help="Manage RISC-V runner control planes")
    cp_subparsers = cp_parser.add_subparsers(dest="command", required=True)

    cp_create = cp_subparsers.add_parser("create", help="Create a new control plane")
    cp_create.add_argument("--staging", action="store_true", help="Create a staging control plane")
    cp_create.set_defaults(func=cmd_control_plane_create)

    args = parser.parse_args()
    args.func(args)


if __name__ == "__main__":
    main()
