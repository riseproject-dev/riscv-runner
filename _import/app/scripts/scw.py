#!/usr/bin/env python3
"""Provision RISE RISC-V runner control planes and runners on Scaleway."""

import argparse
import concurrent.futures
import contextlib
import itertools
import kubernetes
import os
import re
import sys
import threading
import time
import yaml

import logging
# logging.basicConfig(level=logging.INFO)

from enum import StrEnum

from fabric import Connection
from paramiko.ssh_exception import NoValidConnectionsError, SSHException

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
from scaleway.cockpit.v1 import (
    CockpitV1RegionalAPI,
    DataSource,
    DataSourceOrigin,
    DataSourceType,
    Token,
    TokenScope,
)


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
cockpit_api = CockpitV1RegionalAPI(scw_client)


class ProvisioningException(Exception):
    pass


# --- Parallel execution helpers ---

class TaggedStream:
    """File-like wrapper that prefixes each line with a per-thread tag.

    Replaces sys.stdout/sys.stderr so both Python `print` and fabric's
    subprocess output (which writes to the global streams by default) get
    consistent per-runner prefixes when running concurrently.
    """

    def __init__(self, target):
        self._target = target
        self._lock = threading.Lock()
        self._buffers = {}  # thread_id -> partial line str
        self._tag = threading.local()
        self._tag_len = 0

    def set_tag(self, tag):
        self._tag.value = tag

    def clear_tag(self):
        self._tag.value = None

    def set_tag_len(self, n):
        self._tag_len = n

    def _current_tag(self):
        return getattr(self._tag, "value", None)

    def _format_tag(self, tag):
        return f"[{tag:<{self._tag_len}}]"

    def write(self, s):
        if not s:
            return
        tag = self._current_tag()
        if not tag:
            with self._lock:
                self._target.write(s)
            return
        tid = threading.get_ident()
        with self._lock:
            buf = self._buffers.get(tid, "") + s
            parts = buf.split("\n")
            prefix = self._format_tag(tag)
            for line in parts[:-1]:
                self._target.write(f"{prefix} {line}\n")
            self._buffers[tid] = parts[-1]

    def flush(self):
        with self._lock:
            tid = threading.get_ident()
            partial = self._buffers.get(tid, "")
            if partial:
                tag = self._current_tag()
                if tag:
                    self._target.write(f"{self._format_tag(tag)} {partial}\n")
                else:
                    self._target.write(partial)
                self._buffers[tid] = ""
            self._target.flush()

    def isatty(self):
        return getattr(self._target, "isatty", lambda: False)()

    def bind(self, tag):
        """Return a stream that always uses `tag`, regardless of the calling
        thread. Hand to libraries (e.g. Invoke) that do their I/O on a helper
        thread which won't have our threading.local context."""
        return _BoundTaggedStream(self, tag)


class _BoundTaggedStream:
    def __init__(self, parent, tag):
        self._parent = parent
        self._tag = tag
        self._buffer = ""

    def write(self, s):
        if not s:
            return
        with self._parent._lock:
            buf = self._buffer + s
            parts = buf.split("\n")
            prefix = self._parent._format_tag(self._tag)
            for line in parts[:-1]:
                self._parent._target.write(f"{prefix} {line}\n")
            self._buffer = parts[-1]

    def flush(self):
        with self._parent._lock:
            if self._buffer:
                prefix = self._parent._format_tag(self._tag)
                self._parent._target.write(f"{prefix} {self._buffer}\n")
                self._buffer = ""
            self._parent._target.flush()

    def isatty(self):
        return False


class Throttle:
    """Ensure at least `delay` seconds elapse between successive starts."""

    def __init__(self, delay):
        self._delay = delay
        self._lock = threading.Lock()
        self._last = 0.0

    def wait(self):
        if self._delay <= 0:
            return
        with self._lock:
            now = time.monotonic()
            wait = max(0.0, self._last + self._delay - now)
            self._last = max(self._last + self._delay, now)
        if wait > 0:
            time.sleep(wait)


_concurrency_local = threading.local()


class _Concurrency:
    """Active-slot semaphore paired with a dynamically-grown ThreadPoolExecutor.

    A worker acquires a slot at task start and releases it at task end. While
    blocked on slow external waits (e.g. cordon_k8s_node polling pods), the
    worker yields its slot so another queued item can start; the executor's
    _max_workers is bumped so a new worker thread is actually spawned to host
    that item.
    """

    def __init__(self, jobs):
        self._sem = threading.Semaphore(jobs)
        self._lock = threading.Lock()
        self._executor = None

    def bind(self, executor):
        self._executor = executor

    def acquire(self):
        self._sem.acquire()

    def release(self):
        self._sem.release()

    def yield_slot(self):
        with self._lock:
            self._executor._max_workers += 1
            self._executor._adjust_thread_count()
        self._sem.release()

    def resume_slot(self):
        self._sem.acquire()
        with self._lock:
            self._executor._max_workers = max(1, self._executor._max_workers - 1)


@contextlib.contextmanager
def yield_concurrency_slot():
    """Loan the current worker's active slot while parked on a long wait.

    No-op when called outside a _run_parallel worker so the wrapped function
    remains callable from any context.
    """
    ctx = getattr(_concurrency_local, "ctx", None)
    if ctx is None:
        yield
        return
    ctx.yield_slot()
    try:
        yield
    finally:
        ctx.resume_slot()


# Installed in main() so all output flows through it
_tagged_stdout = None
_tagged_stderr = None


def _tagged_streams():
    """Return out_stream/err_stream kwargs for fabric's Connection.run that
    forward subprocess output through the calling thread's tag. Empty dict
    when no tag is set so main-thread callers get the default streams.

    Needed because Invoke reads remote stdout/stderr on a helper thread that
    doesn't share our threading.local tag — passing a bound stream captures
    the tag from the worker thread that actually called run().
    """
    assert not ((_tagged_stdout is not None) ^ (_tagged_stderr is not None)), \
        "both _tagged_stdout and _tagged_stderr should be set or None at the same time"

    if _tagged_stdout is None:
        return {}
    tag = _tagged_stdout._current_tag()
    if not tag:
        return {}
    return {
        "out_stream": _tagged_stdout.bind(tag),
        "err_stream": _tagged_stderr.bind(tag),
    }


def _run_parallel(items, fn, jobs, delay):
    """Run `fn(item)` for each item in `items` across a thread pool.

    - tags each line of output with the item (via TaggedStream)
    - staggers worker starts by at least `delay` seconds
    - never halts on per-item failure; logs and continues
    - 1st Ctrl-C: cancel queued (not-yet-started) futures; in-flight finish
    - 2nd Ctrl-C: warn (1 more to abort)
    - 3rd Ctrl-C: hard exit 130
    """
    if not items:
        return 0

    assert not ((_tagged_stdout is not None) ^ (_tagged_stderr is not None)), \
        "both _tagged_stdout and _tagged_stderr should be set or None at the same time"

    tag_len = max(len(str(item)) for item in items)
    if _tagged_stdout is not None:
        _tagged_stdout.set_tag_len(tag_len)
    if _tagged_stderr is not None:
        _tagged_stderr.set_tag_len(tag_len)

    throttle = Throttle(delay)
    concurrency = _Concurrency(max(1, jobs))

    def _worker(item):
        import traceback
        if _tagged_stdout is not None:
            _tagged_stdout.set_tag(str(item))
        if _tagged_stderr is not None:
            _tagged_stderr.set_tag(str(item))
        try:
            throttle.wait()
            _concurrency_local.ctx = concurrency
            concurrency.acquire()
            try:
                fn(item)
            finally:
                concurrency.release()
                _concurrency_local.ctx = None
            return None
        except Exception as e:
            print(f"FAILED: {e}\n{traceback.format_exc()}")
            return e
        finally:
            if _tagged_stdout is not None:
                _tagged_stdout.flush()
                _tagged_stdout.clear_tag()
            if _tagged_stderr is not None:
                _tagged_stderr.flush()
                _tagged_stderr.clear_tag()

    executor = concurrent.futures.ThreadPoolExecutor(max_workers=max(1, jobs))
    concurrency.bind(executor)
    futures = {executor.submit(_worker, item): item for item in items}

    sigint_count = 0
    sigint_last_time = 0
    pending = set(futures.keys())
    while pending:
        try:
            done, pending = concurrent.futures.wait(
                pending, return_when=concurrent.futures.FIRST_COMPLETED,
            )
        except KeyboardInterrupt:
            if time.time() > sigint_last_time + 10:
                # reset sigint_count if it's been more than 10s
                sigint_count = 0
            sigint_count += 1
            sigint_last_time = time.time()
            if sigint_count >= 5:
                print("\nCtrl-C received 5+ times: aborting now. In-flight work is abandoned. THIS IS UNSAFE!")
                # os._exit bypasses normal interpreter shutdown so we don't
                # wait on non-daemon worker threads doing SSH/HTTP I/O
                os._exit(130)
            elif sigint_count >= 2:
                print("\nCtrl-C received again: press a few more times to abort.")
            else:
                # fut.cancel() returns True only for futures the executor
                # hasn't started yet; running futures return False and are
                # left to finish naturally
                cancelled_now = sum(1 for fut in pending if fut.cancel())
                in_flight = len(pending) - cancelled_now
                print(f"\nCtrl-C received: cancelled {cancelled_now} queued task(s); {in_flight} in-flight will finish. Press Ctrl-C a few more times to abort (unsafe!).")
            continue

    executor.shutdown(wait=True)

    succeeded = []
    failed = []
    cancelled = []
    for fut, item in futures.items():
        if fut.cancelled():
            cancelled.append(item)
            continue
        err = fut.exception()
        if err is None:
            err = fut.result()  # _worker returns the exception or None
        if err is None:
            succeeded.append(item)
        else:
            failed.append((item, err))

    print(f"\n{'='*60}")
    parts = [f"{len(succeeded)} succeeded", f"{len(failed)} failed"]
    if cancelled:
        parts.append(f"{len(cancelled)} cancelled")
    print(f"Summary: {', '.join(parts)}")
    for item, err in failed:
        print(f"  FAILED {item}: {err}")
    for item in cancelled:
        print(f"  CANCELLED {item}")
    print(f"{'='*60}")

    return 0 if not failed else 1


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
            conn.run("true", hide=True)
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

set -eux -o pipefail

# Fresh packages
sudo apt-get update -qq
sudo apt-get upgrade -qq -y

###############################################################################
## Build necessary kernel modules

pushd /usr/lib/modules/$(uname -r)/source

## Install toolchains
sudo apt-get install -y --no-install-recommends \
    build-essential libelf-dev libssl-dev bc bison flex gcc-14 ipset
sudo update-alternatives --install /usr/bin/gcc gcc /usr/bin/gcc-14 100

## Build

MODULES=(
    net/netfilter/ipset
    fs/erofs
    drivers/md
)

# 1. Seed config from the running kernel's /proc
zcat /proc/config.gz | sudo tee .config >/dev/null

# 2.a Enable missing ipset module
sudo scripts/config -m IP_SET_HASH_NET
sudo scripts/config -m IP_SET_HASH_IPPORT
sudo scripts/config -m IP_SET_HASH_IPPORTIP
sudo scripts/config -m IP_SET_HASH_IPPORTNET
sudo scripts/config -m IP_SET_HASH_NETPORT
sudo scripts/config -m IP_SET_HASH_NETIFACE
sudo scripts/config -m IP_SET_HASH_NETNET
sudo scripts/config -m IP_SET_HASH_NETPORTNET
sudo scripts/config -m IP_SET_HASH_IPMARK
sudo scripts/config -m IP_SET_HASH_IPMAC
sudo scripts/config -m IP_SET_HASH_MAC
sudo scripts/config -m IP_SET_BITMAP_IP
sudo scripts/config -m IP_SET_BITMAP_IPMAC
sudo scripts/config -m IP_SET_BITMAP_PORT
sudo scripts/config -m IP_SET_LIST_SET

# 2.b Enable missing erofs module
sudo scripts/config -m EROFS_FS
sudo scripts/config -e EROFS_FS_ZIP
sudo scripts/config -e EROFS_FS_ZIP_LZMA
sudo scripts/config -e EROFS_FS_ZIP_DEFLATE
sudo scripts/config -e EROFS_FS_POSIX_ACL
sudo scripts/config -e EROFS_FS_XATTR

# 2.c Enable missing dm_verity module
sudo scripts/config -m DM_VERITY
sudo scripts/config -e DM_VERITY_VERIFY_ROOTHASH_SIG
# sudo scripts/config -e DM_VERITY_FEC # pulls in CONFIG_REED_SOLOMON which doesn't work
sudo scripts/config -e BLK_DEV_DM
sudo scripts/config -e MD
sudo scripts/config -m CRYPTO_SHA256

# 3. Pin the version suffix so vermagic matches the running kernel
sudo scripts/config --set-str LOCALVERSION "-scw1"
sudo scripts/config -d LOCALVERSION_AUTO

# 4. Resolve config and prepare the tree
sudo make ARCH=riscv olddefconfig
sudo make ARCH=riscv prepare
sudo make ARCH=riscv modules_prepare

# 5. Use the running kernel's symvers (must come AFTER modules_prepare)
sudo cp /lib/modules/$(uname -r)/build/Module.symvers .

# 6. Sanity-check the version string before building
cat include/config/kernel.release | grep "5.10.113-scw1"   # must print: 5.10.113-scw1

# 7. Build only the ipset modules
for m in ${MODULES[*]}; do
    sudo make ARCH=riscv \
         KCFLAGS="-mno-relax -fno-asynchronous-unwind-tables -fno-unwind-tables -g0" \
         KAFLAGS="-mno-relax" \
         M="${m}" modules -j$(nproc)
done

## Verify before installing
for m in ${MODULES[*]}; do
    sudo mkdir -p /lib/modules/$(uname -r)/kernel/${m}
    for ko in ${m}/*.ko; do
        # vermagic must match the running kernel
        modinfo "$ko" | grep '^vermagic:' | grep -q "5.10.113-scw1 SMP preempt mod_unload riscv" || {
            echo "FAIL vermagic mismatch: $ko" >&2
            modinfo "$ko" | grep '^vermagic:' >&2
            exit 1
        }

        # must NOT contain relocations the 5.10 RISC-V module loader can't handle
        if riscv64-linux-gnu-readelf -r "$ko" \
             | awk '{print $3}' | sort -u \
             | grep -qE '(R_RISCV_ALIGN|R_RISCV_32_PCREL)'; then
            echo "FAIL forbidden relocations: $ko" >&2
            exit 1
        fi

        sudo cp -v "$ko" /lib/modules/$(uname -r)/kernel/${m}/
    done
done

sudo depmod -a

popd

###############################################################################
## Setup kernel modules

# Load modules required for kubernetes
cat <<EOF | sudo tee /etc/modules-load.d/k8s.conf
overlay
br_netfilter
nf_conntrack
tun
EOF

# Load modules needed for customers' workloads
cat <<EOF | sudo tee /etc/modules-load.d/users.conf
ip_set
ip_set_bitmap_ip
ip_set_bitmap_ipmac
ip_set_bitmap_port
ip_set_hash_ip
ip_set_hash_ipmac
ip_set_hash_ipmark
ip_set_hash_ipport
ip_set_hash_ipportip
ip_set_hash_ipportnet
ip_set_hash_mac
ip_set_hash_net
ip_set_hash_netiface
ip_set_hash_netnet
ip_set_hash_netport
ip_set_hash_netportnet
ip_set_list_set
erofs
dm_verity
EOF

sudo modprobe overlay
sudo modprobe br_netfilter
sudo modprobe nf_conntrack
sudo modprobe tun

# Blacklist some modules
cat <<EOF | sudo tee /etc/modprobe.d/blacklist-copyfail.conf
blacklist algif_aead
install algif_aead /bin/true
EOF

###############################################################################
## Configure sysctl params for Kubernetes networking

cat <<EOF | sudo tee /etc/sysctl.d/k8s.conf
net.bridge.bridge-nf-call-iptables  = 1
net.bridge.bridge-nf-call-ip6tables = 1
net.ipv4.ip_forward                 = 1
EOF

# Apply the changes
sudo sysctl --system

###############################################################################
## Configure private network VLAN interface

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
# sudo apt-get install -y --no-install-recommends retry
# retry --delay=2 --times=5 -- ip addr show end0.@@PN_VLAN_ID@@

# # Configure private network VLAN interface
# sudo ip link add link end0 name end0.@@PN_VLAN_ID@@ type vlan id @@PN_VLAN_ID@@
# sudo ip link set end0.@@PN_VLAN_ID@@ up
# sudo ip addr add @@PN_IP@@ dev end0.@@PN_VLAN_ID@@

###############################################################################
## Install node_exporter

NODE_EXPORTER_VERSION=1.11.1
curl  -fsSL \
  --retry 5 \
  --retry-delay 5 \
  --retry-all-errors \
  https://github.com/prometheus/node_exporter/releases/download/v${NODE_EXPORTER_VERSION}/node_exporter-${NODE_EXPORTER_VERSION}.linux-$(uname -m).tar.gz | \
    sudo tar -xvzf - -C /usr/local/bin --strip-components 1 node_exporter-${NODE_EXPORTER_VERSION}.linux-$(uname -m)/node_exporter

sudo chown root:root /usr/local/bin/node_exporter
sudo chmod 0755 /usr/local/bin/node_exporter

id -u node_exporter >/dev/null 2>&1 || \
  sudo useradd --system --no-create-home --shell /usr/sbin/nologin node_exporter

sudo install -d -o node_exporter -g node_exporter -m 0755 \
  /var/lib/node_exporter \
  /var/lib/node_exporter/textfile_collector

# Setup node_exporter systemd service
cat <<'EOF' | sudo tee /etc/systemd/system/node_exporter.service
[Unit]
Description=Prometheus node_exporter
Documentation=https://github.com/prometheus/node_exporter
After=network-online.target
Wants=network-online.target
[Service]
Type=simple
User=node_exporter
Group=node_exporter
ExecStart=/usr/local/bin/node_exporter \
  --collector.disable-defaults \
  --collector.uname \
  --collector.cpu \
  --collector.stat \
  --collector.vmstat \
  --collector.loadavg \
  --collector.meminfo \
  --collector.filesystem \
  --collector.filesystem.fs-types-exclude='^(tmpfs|devtmpfs|overlay|squashfs|nsfs|proc|sysfs|cgroup.*|fuse.*|autofs|binfmt_misc|debugfs|tracefs|configfs|hugetlbfs|mqueue|pstore|bpf)$' \
  --collector.filesystem.mount-points-exclude='^/(sys|proc|dev|run|var/lib/(docker|containerd|kubelet))($|/)' \
  --collector.diskstats \
  --collector.diskstats.device-exclude='^(loop|ram|dm-|sr|zram).*$' \
  --collector.filefd \
  --collector.timex \
  --collector.processes \
  --collector.entropy \
  --collector.softirqs \
  --collector.softnet \
  --collector.netdev \
  --collector.netdev.device-include='^end0$' \
  --collector.netstat \
  --collector.netstat.fields='^(Tcp_(InSegs|OutSegs|RetransSegs)|TcpExt_(TCPTimeouts|TCPLostRetransmit|TCPSpuriousRTOs|TCPFastRetrans|TCPSlowStartRetrans|TCPSackRecovery|TCPSACKReorder|TCPRcvCollapsed|ListenOverflows))$' \
  --collector.sockstat \
  --collector.conntrack \
  --collector.textfile \
  --collector.textfile.directory=/var/lib/node_exporter/textfile_collector \
  --web.listen-address=127.0.0.1:9100
Restart=on-failure
RestartSec=5
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
ReadWritePaths=/var/lib/node_exporter
AmbientCapabilities=CAP_NET_ADMIN
CapabilityBoundingSet=CAP_NET_ADMIN
[Install]
WantedBy=multi-user.target
EOF

sudo systemctl daemon-reload
sudo systemctl enable node_exporter

###############################################################################
## Install prometheus

# Install Prometheus in agent mode to ship node_exporter metrics to
# Scaleway Cockpit. Agent mode keeps only a short WAL on disk and uses
# remote_write — no local TSDB, no query API.
PROMETHEUS_VERSION="3.11.3"
curl -fsSL \
  --retry 5 \
  --retry-delay 5 \
  --retry-all-errors \
  https://github.com/prometheus/prometheus/releases/download/v${PROMETHEUS_VERSION}/prometheus-${PROMETHEUS_VERSION}.linux-$(uname -m).tar.gz | \
    sudo tar -C /usr/local/bin -xvzf - --strip-components=1 prometheus-${PROMETHEUS_VERSION}.linux-$(uname -m)/prometheus

sudo chown root:root /usr/local/bin/prometheus
sudo chmod 0755 /usr/local/bin/prometheus

id -u prometheus >/dev/null 2>&1 || \
  sudo useradd --system --no-create-home --shell /usr/sbin/nologin prometheus

sudo install -d -o prometheus -g prometheus -m 0750 \
  /etc/prometheus \
  /var/lib/prometheus

cat <<EOF | sudo tee /etc/prometheus/agent.yml >/dev/null
global:
  scrape_interval: 5m
  external_labels:
    node: $(hostname)
scrape_configs:
- job_name: node_exporter_base
  scrape_interval: 5m
  static_configs:
  - targets: ['127.0.0.1:9100']
  metric_relabel_configs:
    - source_labels: [__name__]
      regex: 'node_cpu_seconds_total|node_load(1|5|15)|node_uname_info|node_boot_time_seconds|node_time_seconds|node_memory_(MemTotal|MemAvailable|MemFree|Cached|Buffers|SwapTotal|SwapFree)_bytes|node_vmstat_(oom_kill|pgmajfault|pswpin|pswpout)|node_filesystem_(avail|size|files|files_free)_bytes|node_filesystem_readonly|node_filesystem_device_error|node_disk_(read|written)_bytes_total|node_disk_io_time_seconds_total|node_disk_io_now|node_filefd_(allocated|maximum)|node_processes_(state|threads|max_threads|pids|max_processes)|node_timex_(sync_status|offset_seconds|maxerror_seconds)|node_entropy_available_bits|node_textfile_(mtime_seconds|scrape_error)|raw_github_probe_.*|runner_dns_.*|node_network_(receive|transmit)_(bytes|packets|drop|errs)_total|node_netstat_.*|node_sockstat_TCP_.*|node_nf_conntrack_entries|node_softnet_(processed|dropped|times_squeezed)_total|node_softirqs_functions_total'
      action: keep
remote_write:
- url: '@@COCKPIT_METRICS_PUSH_URL@@/api/v1/push'
  headers:
    X-TOKEN: '@@COCKPIT_METRICS_TOKEN@@'
  write_relabel_configs:
    - source_labels: [__name__]
      regex: '(go_|process_|promhttp_).*'
      action: drop
EOF

sudo chown prometheus:prometheus /etc/prometheus/agent.yml
sudo chmod 0640 /etc/prometheus/agent.yml

cat <<'EOF' | sudo tee /etc/systemd/system/prometheus-agent.service
[Unit]
Description=Prometheus (agent mode) shipping to Scaleway Cockpit
Documentation=https://prometheus.io/docs/prometheus/latest/feature_flags/#prometheus-agent
After=network-online.target node_exporter.service
Wants=network-online.target
[Service]
Type=simple
User=prometheus
Group=prometheus
ExecStart=/usr/local/bin/prometheus \
  --config.file=/etc/prometheus/agent.yml \
  --agent \
  --storage.agent.path=/var/lib/prometheus \
  --web.listen-address=127.0.0.1:9090
Restart=on-failure
RestartSec=5
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
ReadWritePaths=/var/lib/prometheus
[Install]
WantedBy=multi-user.target
EOF

sudo systemctl daemon-reload
sudo systemctl enable prometheus-agent

###############################################################################
## Install probe scripts (textfile collector)

# Sources live in scripts/probes/; substituted in by run_setup().

# GitHub PAT used by the authenticated raw.githubusercontent.com probe variant.
# Empty when unset → probe falls back to running only the unauthenticated variants.
sudo install -d -m 0755 -o root -g root /etc/raw-github-probe
cat <<'EOF' | sudo tee /etc/raw-github-probe/token >/dev/null
@@GITHUB_PROBE_TOKEN@@
EOF
sudo chown root:node_exporter /etc/raw-github-probe/token
sudo chmod 0440 /etc/raw-github-probe/token

cat <<'EOF' | sudo tee /usr/local/bin/raw-github-probe.py >/dev/null
#!/usr/bin/env python3

# Probe raw.githubusercontent.com download speed for the node_exporter
# textfile collector.
#
# Three variants:
#   target=raw.githubusercontent.com, auth=none   — current behaviour
#   target=raw.githubusercontent.com, auth=token  — same URL with PAT in
#     Authorization header. Disambiguates whether the steady ~131 kB/s
#     ceiling is a per-IP rate limit (auth lifts it) or a per-request
#     bandwidth throttle (auth doesn't).
#   target=cloudflare-control, auth=none          — non-Fastly comparison.
#
# curl is shelled out so we get %{remote_ip} reflecting the IP libcurl
# actually connected to.
#
# Token is loaded from /etc/raw-github-probe/token (root:node_exporter, 0440).
# Empty or missing → authed variant is skipped silently.

import os
import subprocess
import tempfile
from pathlib import Path

OUT = Path("/var/lib/node_exporter/textfile_collector/raw_github_probe.prom")
TOKEN_FILE = Path("/etc/raw-github-probe/token")

# Both are ~5MB
URL_RAW_GITHUB = "https://raw.githubusercontent.com/usnistgov/ACVP-Server/15c0f3deeefbfa8cb6cd32a99e1ca3b738c66bf0/gen-val/json-files/ML-DSA-sigGen-FIPS204/prompt.json"
URL_CLOUDFLARE = "https://speed.cloudflare.com/__down?bytes=5242880"

CURL_FORMAT = "%{time_total} %{speed_download} %{remote_ip} %{exitcode}\n"

HEADER  = "# HELP raw_github_probe_seconds Wallclock to download a fixed test artefact.\n"
HEADER += "# TYPE raw_github_probe_seconds gauge\n"
HEADER += "# HELP raw_github_probe_bytes_per_second Average download throughput in bytes/sec.\n"
HEADER += "# TYPE raw_github_probe_bytes_per_second gauge\n"
HEADER += "# HELP raw_github_probe_curl_exit_code Curl exit code; 0 on success.\n"
HEADER += "# TYPE raw_github_probe_curl_exit_code gauge\n"


def load_token() -> str:
    try:
        return TOKEN_FILE.read_text().strip()
    except (FileNotFoundError, PermissionError, OSError):
        return ""


def targets(token):
    out = [
        ({"target": "raw.githubusercontent.com", "auth": "none"}, URL_RAW_GITHUB, None),
        ({"target": "cloudflare-control",        "auth": "none"}, URL_CLOUDFLARE, None),
    ]
    if token:
        out.append(
            ({"target": "raw.githubusercontent.com", "auth": "token"},
             URL_RAW_GITHUB, f"Authorization: Bearer {token}")
        )
    return out


def fmt_labels(labels):
    return ",".join(f'{k}="{v}"' for k, v in labels.items())


def probe(labels, url, auth_header):
    # Headers piped via `curl -K -` so the token never lands in argv / ps.
    config = f'header = "{auth_header}"\n' if auth_header else None
    args = ["curl", "-o", "/dev/null", "-s", "--max-time", "30", "-w", CURL_FORMAT]
    if config:
        args += ["-K", "-"]
    args.append(url)
    try:
        result = subprocess.run(
            args, input=config, capture_output=True, text=True, timeout=35,
        )
        fields = (result.stdout.strip() or "0 0 unknown 99").split()
    except (subprocess.TimeoutExpired, FileNotFoundError):
        fields = ["0", "0", "unknown", "99"]
    seconds, bps, ip, code = (fields + ["0", "0", "unknown", "99"])[:4]
    base = fmt_labels(labels)
    return (
        f'raw_github_probe_seconds{{{base},remote_ip="{ip}"}} {seconds}\n'
        f'raw_github_probe_bytes_per_second{{{base}}} {bps}\n'
        f'raw_github_probe_curl_exit_code{{{base}}} {code}\n'
    )


def write_atomic(path: Path, content: str) -> None:
    # Write content via tempfile + os.replace so node_exporter never sees a half-written file.
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, tmp = tempfile.mkstemp(dir=path.parent, prefix="." + path.name + ".")
    try:
        with os.fdopen(fd, "w") as f:
            f.write(content)
        os.replace(tmp, path)
    except BaseException:
        Path(tmp).unlink(missing_ok=True)
        raise


def main() -> None:
    write_atomic(OUT, HEADER + "".join(probe(labels, url, auth) for labels, url, auth in targets(load_token())))


if __name__ == "__main__":
    main()
EOF

sudo chmod 0755 /usr/local/bin/raw-github-probe.py
sudo chown root:root /usr/local/bin/raw-github-probe.py

cat <<'EOF' | sudo tee /usr/local/bin/dns-probe.py >/dev/null
#!/usr/bin/env python3

# Snapshot which IPs raw.githubusercontent.com currently resolves to, for
# the node_exporter textfile collector.
#
# Resolves via socket.getaddrinfo (the libc resolver curl uses), so the IPs
# we record match the customer's real traffic path. Used to correlate slow
# windows with Fastly cache-region or POP flips (H1).

import os
import socket
import tempfile
from pathlib import Path

OUT = Path("/var/lib/node_exporter/textfile_collector/dns_probe.prom")
HOST = "raw.githubusercontent.com"

HEADER  = "# HELP runner_dns_resolved_ip Info metric: 1 per IP currently resolved for HOST.\n"
HEADER += "# TYPE runner_dns_resolved_ip gauge\n"
HEADER += "# HELP runner_dns_resolved_ip_count Number of IPs returned for HOST.\n"
HEADER += "# TYPE runner_dns_resolved_ip_count gauge\n"

def resolve(host: str) -> list[str]:
    ips: set[str] = set()
    for family in (socket.AF_INET, socket.AF_INET6):
        try:
            ips.update(info[4][0] for info in socket.getaddrinfo(host, None, family, socket.SOCK_STREAM))
        except socket.gaierror:
            pass
    return sorted(ips)


def write_atomic(path: Path, content: str) -> None:
    # Write content via tempfile + os.replace so node_exporter never sees a half-written file.
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, tmp = tempfile.mkstemp(dir=path.parent, prefix="." + path.name + ".")
    try:
        with os.fdopen(fd, "w") as f:
            f.write(content)
        os.replace(tmp, path)
    except BaseException:
        Path(tmp).unlink(missing_ok=True)
        raise


def main() -> None:
    ips = resolve(HOST)
    body = HEADER + "".join(
        f'runner_dns_resolved_ip{{host="{HOST}",ip="{ip}"}} 1\n' for ip in ips
    ) + f'runner_dns_resolved_ip_count{{host="{HOST}"}} {len(ips)}\n'
    write_atomic(OUT, body)


if __name__ == "__main__":
    main()
EOF
sudo chmod 0755 /usr/local/bin/dns-probe.py
sudo chown root:root /usr/local/bin/dns-probe.py

# Systemd timers running each probe as the node_exporter user.
cat <<'EOF' | sudo tee /etc/systemd/system/raw-github-probe.service
[Unit]
Description=Synthetic probe of raw.githubusercontent.com and a comparison target
After=network-online.target
Wants=network-online.target
[Service]
Type=oneshot
User=node_exporter
Group=node_exporter
ExecStart=/usr/local/bin/raw-github-probe.py
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
ReadWritePaths=/var/lib/node_exporter
ReadOnlyPaths=/etc/raw-github-probe
EOF

cat <<'EOF' | sudo tee /etc/systemd/system/raw-github-probe.timer
[Unit]
Description=Run raw-github-probe every 5 minutes
[Timer]
OnBootSec=1min
OnUnitActiveSec=5min
Unit=raw-github-probe.service
[Install]
WantedBy=timers.target
EOF

cat <<'EOF' | sudo tee /etc/systemd/system/dns-probe.service
[Unit]
Description=DNS resolution snapshot for raw.githubusercontent.com
After=network-online.target
Wants=network-online.target
[Service]
Type=oneshot
User=node_exporter
Group=node_exporter
ExecStart=/usr/local/bin/dns-probe.py
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
ReadWritePaths=/var/lib/node_exporter
EOF

cat <<'EOF' | sudo tee /etc/systemd/system/dns-probe.timer
[Unit]
Description=Run dns-probe every 60 seconds
[Timer]
OnBootSec=30s
OnUnitActiveSec=60s
Unit=dns-probe.service
[Install]
WantedBy=timers.target
EOF

sudo systemctl daemon-reload
sudo systemctl enable raw-github-probe.timer
sudo systemctl enable dns-probe.timer


###############################################################################
## Install containerd

sudo apt-get install -y --no-install-recommends containerd
sudo mkdir -p /etc/containerd
containerd config default | sudo tee /etc/containerd/config.toml > /dev/null

## Enable SystemdCgroup driver
sudo sed -i 's/SystemdCgroup = false/SystemdCgroup = true/g' /etc/containerd/config.toml

## Set the multi-arch (amd64/riscv64) compatible pause image
## This ensures that both architectures can pull a valid sandbox image
sudo sed -i 's|sandbox_image = ".*"|sandbox_image = "cloudv10x/pause:3.10"|' /etc/containerd/config.toml

## Restart the service
sudo systemctl restart containerd

###############################################################################
## Install crictl

CRICTL_VERSION="v1.35.0" # https://github.com/kubernetes-sigs/cri-tools/releases/tag/v1.35.0
curl -fsSL \
  --retry 5 \
  --retry-delay 5 \
  --retry-all-errors \
  https://github.com/kubernetes-sigs/cri-tools/releases/download/${CRICTL_VERSION}/crictl-${CRICTL_VERSION}-linux-$(uname -m).tar.gz | \
    sudo tar -C /usr/local/bin -xvzf -

###############################################################################
## Install CNI plugins

sudo mkdir -p /opt/cni/bin
curl -fsSL \
  --retry 5 \
  --retry-delay 5 \
  --retry-all-errors \
  https://github.com/containernetworking/plugins/releases/download/v1.4.0/cni-plugins-linux-riscv64-v1.4.0.tgz | \
    sudo tar -C /opt/cni/bin -xvzf -

###############################################################################
## Install kubernetes cli tools: kubeadm, kubelet, kubectl

sudo apt-get install -y --no-install-recommends curl unzip
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
"""

KUBEADM_SCRIPT=r"""
sudo kubeadm reset -f || true
sudo @@KUBEADM_JOIN_CMD@@
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
    result = ssh_cp.run("kubeadm token create --ttl 5m", hide=True)
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


def get_or_create_cockpit_metrics_data_source() -> DataSource:
    """Return the external metrics data source, creating it if absent."""
    METRICS_DATA_SOURCE_NAME = f"riscv-runner-metrics-datasource"
    for ds in cockpit_api.list_data_sources_all(origin=DataSourceOrigin.EXTERNAL, types=[DataSourceType.METRICS]):
        if ds.name == METRICS_DATA_SOURCE_NAME:
            return ds

    return cockpit_api.create_data_source(name=METRICS_DATA_SOURCE_NAME, type_=DataSourceType.METRICS)


def create_cockpit_metrics_push_token(name: str) -> Token:
    """Create a write-only metrics token. `secret_key` is populated only on the returned object."""
    for token in cockpit_api.list_tokens_all():
        if token.name == name:
            cockpit_api.delete_token(token_id=token.id)

    return cockpit_api.create_token(
        name=name,
        token_scopes=[TokenScope.WRITE_ONLY_METRICS],
    )


def get_github_probe_token() -> str:
    if not "GITHUB_PROBE_TOKEN" in os.environ:
        print(f"WARNING! The environment variable GITHUB_PROBE_TOKEN is not defined, the host will be setup without")
    return os.environ.get("GITHUB_PROBE_TOKEN", "")


def setup_runner(ssh, runner, pn):
    cockpit_metrics_ds = get_or_create_cockpit_metrics_data_source()
    cockpit_metrics_token = create_cockpit_metrics_push_token(f"{runner}-metrics-token")
    github_probe_token = get_github_probe_token()
    script = SETUP_SCRIPT.replace("@@COCKPIT_METRICS_PUSH_URL@@", cockpit_metrics_ds.url) \
                         .replace("@@COCKPIT_METRICS_TOKEN@@", cockpit_metrics_token.secret_key) \
                         .replace("@@GITHUB_PROBE_TOKEN@@", github_probe_token) \
                         #FIXME(pn): enable private address again
                         # .replace("@@PN_IP@@", pn.ip)
                         # .replace("@@PN_VLAN_ID@@", pn.vlan_id)
    ssh.run(script, **_tagged_streams())


def setup_runner_kubeadm(ssh, ssh_cp, cp_public_ip):
    join_cmd = get_kubeadm_join_cmd(ssh_cp, cp_public_ip)
    script = KUBEADM_SCRIPT.replace("@@KUBEADM_JOIN_CMD@@", join_cmd)
    ssh.run(script, **_tagged_streams())


def reboot_runner(ssh):
    ssh.run("sudo reboot", **_tagged_streams())


def find_server_by_name(hostname):
    resp = baremetal_api.list_servers(name=hostname)
    for server in resp.servers:
        if server.name == hostname:
            return server
    raise ServerNotFoundException(f"Server '{hostname}' not found in project {PROJECT_ID}")


def setup_k8s_client(ssh_cp):
    result = ssh_cp.run("cat /etc/kubernetes/admin.conf", hide=True)
    return kubernetes.config.new_client_from_config_dict(yaml.safe_load(result.stdout))


def cordon_k8s_node(hostname, k8s):
    core = kubernetes.client.CoreV1Api(api_client=k8s)

    # Cordon the node so no new pods are scheduled on it
    try:
        core.patch_node(hostname, {"spec": {"unschedulable": True}})
    except kubernetes.client.ApiException as e:
        if e.status != 404:
            raise

    def _remaining():
        try:
            return [
                pod for pod in core.list_namespaced_pod(
                    namespace="default",
                    field_selector=f"spec.nodeName={hostname}",
                ).items
                if pod.status.phase not in ("Succeeded", "Failed")
            ]
        except kubernetes.client.ApiException as e:
            if e.status != 404:
                raise
            return []

    remaining = _remaining()
    if not remaining:
        return

    # The node is already cordoned, so no new pods will land here. Yield our
    # active-parallelism slot while we wait so another runner can make progress.
    with yield_concurrency_slot():
        while remaining:
            print(f"  waiting for {len(remaining)} pod(s) to finish on node {hostname}")
            time.sleep(15)
            remaining = _remaining()


def delete_k8s_node(hostname, k8s):
    core = kubernetes.client.CoreV1Api(api_client=k8s)

    try:
        core.delete_node(hostname)
    except kubernetes.client.ApiException as e:
        if e.status != 404:
            raise


def wait_k8s_node(hostname, k8s):
    core = kubernetes.client.CoreV1Api(api_client=k8s)

    while True:
        try:
            core.read_node(hostname)
            print(f"  node {hostname} available but not ready yet!")
            break
        except kubernetes.client.ApiException as e:
            if e.status != 404:
                raise
            print(f"  node {hostname} not available yet!")
            time.sleep(15)

    deadline = time.time() + 600
    while time.time() < deadline:
        node = core.read_node(hostname)
        for cond in (node.status.conditions or []):
            if cond.type == "Ready" and cond.status == "True":
                print(f"  node {hostname} available and ready!")
                return
        time.sleep(5)
    raise RuntimeError(f"Timeout waiting for node {hostname} to be ready")


def create_server(hostname, os_id, tags=None):
    while True:
        try:
            return BareMetal.create(hostname, RUNNER_SERVER_TYPE, os_id, tags=tags)
        except Exception:
            print(f"Server creation failed, retrying in {RETRY_DELAY}s...")
            time.sleep(RETRY_DELAY)


def _allocate_runner_names(count):
    """Pre-allocate `count` distinct riscv-runner-N names from the unused index pool."""
    prefix = "riscv-runner-"
    pattern = re.compile(rf"^{re.escape(prefix)}(\d+)$")
    used = set()
    for page in itertools.count(start=0):
        resp = baremetal_api.list_servers(page=page)
        if len(resp.servers) == 0:
            break
        for server in resp.servers:
            m = pattern.match(server.name or "")
            if m:
                used.add(int(m.group(1)))
    names = []
    i = 0
    while len(names) < count:
        if i not in used:
            names.append(f"{prefix}{i}")
            used.add(i)
        i += 1
    return names


def cmd_runner_create(args):
    os_id = get_os_id()
    print(f"Using OS ID: {os_id}")

    control_plane = args.control_plane
    try:
        cp_public_ip, cp_private_ip = get_control_plane_host(control_plane)
        print(f"Using control plane: {control_plane} (public: {cp_public_ip}, private: {cp_private_ip})")
    except ServerNotFoundException:
        print(f"Failed to find control plane {control_plane}")
        return 1

    runners = _allocate_runner_names(args.count)
    print(f"Allocated runner names: {', '.join(runners)}")

    def _do_runner_create(runner):
        print(f"\n{'='*60}")
        print(f"Creating runner {runner}")
        print(f"{'='*60}")

        ssh_cp = ssh_connect(host=cp_public_ip, user="root")
        k8s = setup_k8s_client(ssh_cp)

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
        setup_runner(ssh, runner, pn)
        setup_runner_kubeadm(ssh, ssh_cp, cp_public_ip)
        reboot_runner(ssh)

        print(f"Waiting for node {runner} to be ready in k8s")
        wait_k8s_node(runner, k8s)

        print(f"Server {runner} provisioned")

    return _run_parallel(runners, _do_runner_create, jobs=args.jobs, delay=args.delay)


def cmd_runner_reinstall(args):
    os_id = get_os_id()
    print(f"Using OS ID: {os_id}")

    def _do_runner_reinstall(runner):
        print(f"\n{'='*60}")
        print(f"Reinstalling runner {runner}")
        print(f"{'='*60}")

        server = find_server_by_name(runner)
        print(f"Found existing server: {server.id}")

        control_plane = next(tag[14:] for tag in server.tags if tag.startswith("control-plane:"))
        if not control_plane:
            raise ProvisioningException(f"missing 'control-plane:*' tag, tags = [{",".join(server.tags)}]")

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
                raise ProvisioningException(f"Failed to find control plane {control_plane}")

        if cp_public_ip:
            ssh_cp = ssh_connect(host=cp_public_ip, user="root")
            k8s = setup_k8s_client(ssh_cp)

            print(f"Draining and removing {runner} from k8s")
            cordon_k8s_node(runner, k8s)
            delete_k8s_node(runner, k8s)

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
                k8s = setup_k8s_client(ssh_cp)

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
        setup_runner(ssh, runner, pn)
        setup_runner_kubeadm(ssh, ssh_cp, cp_public_ip)
        reboot_runner(ssh)

        print(f"Waiting for node {runner} to be ready on k8s")
        wait_k8s_node(runner, k8s)

        print(f"Server {runner} provisioned")

    return _run_parallel(args.runners, _do_runner_reinstall, jobs=args.jobs, delay=args.delay)


def cmd_runner_setup(args):
    def _do_runner_setup(runner):
        print(f"\n{'='*60}")
        print(f"Setting up runner {runner}")
        print(f"{'='*60}")

        server = find_server_by_name(runner)
        print(f"Found existing server: {server.id}")

        control_plane = next(tag[14:] for tag in server.tags if tag.startswith("control-plane:"))
        if not control_plane:
            raise ProvisioningException(f"missing 'control-plane:*' tag, tags = [{",".join(server.tags)}]")

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
                raise ProvisioningException(f"Failed to find control plane {control_plane}")

        ssh_cp = None
        if cp_public_ip:
            ssh_cp = ssh_connect(host=cp_public_ip, user="root")
            k8s = setup_k8s_client(ssh_cp)

            print(f"Draining and removing {runner} from k8s")
            cordon_k8s_node(runner, k8s)
            delete_k8s_node(runner, k8s)

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
                k8s = setup_k8s_client(ssh_cp)

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
        setup_runner(ssh, runner, pn)
        setup_runner_kubeadm(ssh, ssh_cp, cp_public_ip)
        reboot_runner(ssh)

        print(f"Waiting for node {runner} to be ready on k8s")
        wait_k8s_node(runner, k8s)

        print(f"Server {runner} provisioned")

    return _run_parallel(args.runners, _do_runner_setup, jobs=args.jobs, delay=args.delay)


def cmd_runner_reboot(args):
    def _do_runner_reboot(runner):
        print(f"\n{'='*60}")
        print(f"Setting up runner {runner}")
        print(f"{'='*60}")

        server = find_server_by_name(runner)
        print(f"Found existing server: {server.id}")

        control_plane = next(tag[14:] for tag in server.tags if tag.startswith("control-plane:"))
        if not control_plane:
            raise ProvisioningException(f"missing 'control-plane:*' tag, tags = [{",".join(server.tags)}]")

        cp_public_ip = None
        cp_private_ip = None
        try:
            cp_public_ip, cp_private_ip = get_control_plane_host(control_plane)
            print(f"Using control plane: {control_plane} (public: {cp_public_ip}, private: {cp_private_ip})")
        except ServerNotFoundException:
            raise ProvisioningException(f"Failed to find control plane {control_plane}")

        ssh_cp = None
        ssh_cp = ssh_connect(host=cp_public_ip, user="root")
        k8s = setup_k8s_client(ssh_cp)

        print(f"Draining and removing {runner} from k8s")
        cordon_k8s_node(runner, k8s)
        delete_k8s_node(runner, k8s)

        server = BareMetal(server.id)

        ip = server.get_public_ip()
        print(f"Public IP: {ip}")

        ssh = ssh_connect(host=ip, user="ubuntu")
        reboot_runner(ssh)

        print(f"Waiting for node {runner} to be ready on k8s")
        wait_k8s_node(runner, k8s)

        print(f"Server {runner} rebooted")

    return _run_parallel(args.runners, _do_runner_reboot, jobs=args.jobs, delay=args.delay)


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
    def _do_runner_delete(runner):
        print(f"\n{'='*60}")
        print(f"Deleting runner {runner}")
        print(f"{'='*60}")

        server = find_server_by_name(runner)
        print(f"Found server: {server.id}")

        control_plane = next(tag[14:] for tag in server.tags if tag.startswith("control-plane:"))
        if not control_plane:
            raise ProvisioningException(f"missing 'control-plane:*' tag, tags = [{",".join(server.tags)}]")

        cp_public_ip, cp_private_ip = get_control_plane_host(control_plane)
        print(f"Using control plane: {control_plane} (public: {cp_public_ip}, private: {cp_private_ip})")
        ssh_cp = ssh_connect(host=cp_public_ip, user="root")
        k8s = setup_k8s_client(ssh_cp)

        print(f"Draining and removing {runner} from k8s")
        cordon_k8s_node(runner, k8s)
        delete_k8s_node(runner, k8s)

        server = BareMetal(server.id)
        server.delete()
        print(f"Server {runner} deleted")

    return _run_parallel(args.runners, _do_runner_delete, jobs=args.jobs, delay=args.delay)


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

  # Install kubelet, kubeadm, kubectl from official apt-get repo
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
    ssh.run("cloud-init status --wait", hide=False, **_tagged_streams())

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

def _add_parallel_args(subparser):
    subparser.add_argument("-j", "--jobs", type=int, default=4, help="Max concurrent runner operations (default: 4)")
    subparser.add_argument("--delay", type=float, default=3.0, help="Min seconds between successive task starts (default: 3)")


def main():
    global _tagged_stdout, _tagged_stderr
    _tagged_stdout = TaggedStream(sys.stdout)
    _tagged_stderr = TaggedStream(sys.stderr)
    assert not ((_tagged_stdout is not None) ^ (_tagged_stderr is not None)), \
        "both _tagged_stdout and _tagged_stderr should be set or None at the same time"
    sys.stdout = _tagged_stdout
    sys.stderr = _tagged_stderr

    parser = argparse.ArgumentParser(description="Provision RISE RISC-V runner infrastructure on Scaleway")
    resource_subparsers = parser.add_subparsers(dest="resource", required=True)

    # runner ...
    runner_parser = resource_subparsers.add_parser("runner", help="Manage RISC-V runner bare metal nodes")
    runner_subparsers = runner_parser.add_subparsers(dest="command", required=True)

    runner_create = runner_subparsers.add_parser("create", help="Create new runners")
    runner_create.add_argument("--control-plane", type=str, required=True, help="Name of the control plane instance")
    runner_create.add_argument("count", nargs="?", type=int, default=1, help="Number of new runners to create")
    _add_parallel_args(runner_create)
    runner_create.set_defaults(func=cmd_runner_create)

    runner_list = runner_subparsers.add_parser("list", help="List runners")
    runner_list.add_argument("--control-plane", type=str, required=True, help="Name of the control plane instance")
    runner_list.set_defaults(func=cmd_runner_list)

    runner_reinstall = runner_subparsers.add_parser("reinstall", help="Reinstall OS on existing runners")
    runner_reinstall.add_argument("--to-control-plane", type=str, help="Name of the control plane instance to switch the runner to")
    runner_reinstall.add_argument("runners", nargs="+", type=str, help="Runner to reinstall")
    _add_parallel_args(runner_reinstall)
    runner_reinstall.set_defaults(func=cmd_runner_reinstall)

    runner_setup = runner_subparsers.add_parser("setup", help="Setup existing runners")
    runner_setup.add_argument("--to-control-plane", type=str, help="Name of the control plane instance to switch the runner to")
    runner_setup.add_argument("runners", nargs="+", type=str, help="Runner to reinstall")
    _add_parallel_args(runner_setup)
    runner_setup.set_defaults(func=cmd_runner_setup)

    runner_reboot = runner_subparsers.add_parser("reboot", help="Reboot existing runners")
    runner_reboot.add_argument("runners", nargs="+", type=str, help="Runner to reinstall")
    _add_parallel_args(runner_reboot)
    runner_reboot.set_defaults(func=cmd_runner_reboot)

    runner_delete = runner_subparsers.add_parser("delete", help="Delete existing runners")
    runner_delete.add_argument("runners", nargs="+", type=str, help="Runners to delete")
    _add_parallel_args(runner_delete)
    runner_delete.set_defaults(func=cmd_runner_delete)

    # control-plane ...
    cp_parser = resource_subparsers.add_parser("control-plane", help="Manage RISC-V runner control planes")
    cp_subparsers = cp_parser.add_subparsers(dest="command", required=True)

    cp_create = cp_subparsers.add_parser("create", help="Create a new control plane")
    cp_create.add_argument("--staging", action="store_true", help="Create a staging control plane")
    cp_create.set_defaults(func=cmd_control_plane_create)

    args = parser.parse_args()
    rc = args.func(args)
    sys.exit(rc or 0)


if __name__ == "__main__":
    main()
