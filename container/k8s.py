from __future__ import annotations

import datetime
import functools
import logging
from enum import Enum
import kubernetes as k8s
import yaml

from constants import *

logger = logging.getLogger(__name__)


class FailureReason(str, Enum):
    POD_FAILED              = "pod_failed"
    RUNNER_NEVER_REGISTERED = "runner_never_registered"
    POD_STUCK_PENDING       = "pod_stuck_pending"


@functools.lru_cache(maxsize=1)
def _init_client():
    """Create a Kubernetes API client from a kubeconfig env var."""
    return k8s.config.new_client_from_config_dict(yaml.safe_load(K8S_KUBECONFIG))


def provision_runner(jit_config, runner_name, k8s_image, k8s_pool, entity_id, entity_name):
    """Provision a new runner in a Kubernetes pod.

    k8s_pool is the board name (e.g. "scw-em-rv1"). The nodeSelector is
    reconstructed internally from it.
    """
    node_selector = {"riseproject.dev/board": k8s_pool}

    with _init_client() as client:
        api = k8s.client.CoreV1Api(client)

        pod_manifest = {
            "apiVersion": "v1",
            "kind": "Pod",
            "metadata": {
                "name": runner_name,
                "labels": {
                    "app": "rise-riscv-runner",
                    "riseproject.com/entity_id": str(entity_id),
                    "riseproject.com/entity_name": str(entity_name),
                    "riseproject.com/board": k8s_pool,
                },
            },
            "spec": {
                "nodeSelector": node_selector,
                # 24h queue limit + 5d execution limit + 2h buffer = 525600s
                "activeDeadlineSeconds": 525600,
                "restartPolicy": "Never",
                # Cloud-V hosted boards are on a private network behind a NAT, breaking DNS across pods. Use
                # the host network which has access to the internet
                "hostNetwork": k8s_pool.startswith("cloudv10x-"),
                "containers": [
                    {
                        "name": "runner",
                        "image": k8s_image,
                        "imagePullPolicy": "IfNotPresent",
                        "securityContext": {"privileged": True},
                        "command": ["/bin/bash", "-eux", "-o", "pipefail", "-c"],
                        "args": [
                            f"""
                            if [[ -n "${{RUNNER_VERSION+x}}" && -d "/home/runner/actions-runner/cached/${{RUNNER_VERSION}}" ]]; then
                                /home/runner/actions-runner/cached/${{RUNNER_VERSION}}/run.sh --jitconfig {jit_config}
                            elif [[ -f ./run.sh ]]; then
                                ./run.sh --jitconfig {jit_config}
                            else
                                echo "can't find ./run.sh"
                                exit 1
                            fi
                            """
                        ],
                        "env": [
                            {"name": "GITHUB_ACTIONS_RUNNER_TRACE", "value": "1"},
                            {"name": "RUNNER_WAIT_FOR_DOCKER_IN_SECONDS", "value": "60"},
                            {"name": "DOCKER_HOST", "value": "tcp://localhost:2376"},
                            {"name": "DOCKER_TLS_CERTDIR", "value": "/docker-certs"},
                            {"name": "DOCKER_TLS_VERIFY", "value": "1"},
                            {"name": "DOCKER_CERT_PATH", "value": "/docker-certs/client"},
                        ],
                        "volumeMounts": [
                            {
                                "name": "docker-certs",
                                "mountPath": "/docker-certs",
                                "readOnly": True,
                            },
                            {
                                "name": "workspace",
                                "mountPath": "/home/runner/_work",
                            },
                        ],
                        "resources": {
                            "limits": {
                                "riseproject.com/runner": "1",
                            }
                        }
                    },
                ],
                "initContainers": [
                    {
                        # Docker-in-Docker sidecar for runner container to run DinD-enabled jobs
                        "name": "dind",
                        "image": RUNNER_IMAGE_DIND,
                        "imagePullPolicy": "IfNotPresent",
                        "restartPolicy": "Always", # makes it a "sidecar"
                        "securityContext": {"privileged": True},
                        "args": [
                            # The DinD container's docker0 bridge defaults to MTU 1500, but the
                            # underlying Flannel/CNI overlay network only supports 1450, causing
                            # large packets (like TLS ClientHello) to be silently dropped and TLS
                            # handshakes to hang.
                            # Fix: set dockerd --mtu=1450 in the DinD container to match the pod
                            # network's path MTU.
                            "--mtu=1450",
                        ],
                        "env": [
                            {"name": "DOCKER_TLS_CERTDIR", "value": "/docker-certs"},
                        ],
                        "volumeMounts": [
                            {
                                "name": "docker-certs",
                                "mountPath": "/docker-certs",
                            },
                            {
                                "name": "docker-storage",
                                "mountPath": "/var/lib/docker",
                            },
                            {
                                "name": "workspace",
                                "mountPath": "/home/runner/_work",
                            },
                        ],
                    },
                ],
                "volumes": [
                    {
                        "name": "docker-certs",
                        "emptyDir": {},
                    },
                    {
                        "name": "docker-storage",
                        "emptyDir": {},
                    },
                    {
                        "name": "workspace",
                        "emptyDir": {},
                    },
                ],
            }
        }

        api.create_namespaced_pod(body=pod_manifest, namespace="default")


def delete_pod(pod):
    """Delete a runner pod."""
    assert pod, "Pod must be provided to delete it"
    with _init_client() as client:
        api = k8s.client.CoreV1Api(client)
        try:
            api.delete_namespaced_pod(name=pod.metadata.name, namespace="default")
            logger.info("Deleted runner pod %s", pod.metadata.name)
            return f"Pod {pod.metadata.name} deleted successfully."
        except k8s.client.exceptions.ApiException as e:
            if e.status == 404:
                logger.debug("Pod %s not found, already deleted", pod.metadata.name)
                return f"Pod {pod.metadata.name} not found."
            raise


def kill_pod(pod):
    """Force a pod to transition to Failed phase (reason=DeadlineExceeded).

    Patches spec.activeDeadlineSeconds to 1. The kubelet compares this against
    now() - pod.status.startTime and, when exceeded, marks phase=Failed and
    SIGTERM/SIGKILLs the containers. Unlike delete_pod(), the pod stays in the
    cluster so logs/events remain inspectable until the grace window removes it.
    """
    assert pod, "Pod must be provided to kill it"
    body = {"spec": {"activeDeadlineSeconds": 1}}
    with _init_client() as client:
        api = k8s.client.CoreV1Api(client)
        try:
            api.patch_namespaced_pod(name=pod.metadata.name, namespace="default", body=body)
            logger.info("Killed runner pod %s (activeDeadlineSeconds=1)", pod.metadata.name)
        except k8s.client.exceptions.ApiException as e:
            if e.status == 404:
                logger.debug("Pod %s not found, already gone", pod.metadata.name)
                return
            raise


def has_available_slot(node_selector):
    """Check if there's an available runner slot on nodes matching the selector."""
    with _init_client() as client:
        api = k8s.client.CoreV1Api(client)

        nodes = api.list_node()
        matching_nodes = [
            node for node in nodes.items
            if all(node.metadata.labels.get(k) == v for k, v in node_selector.items())
        ]
        total = sum(
            int(node.status.allocatable.get("riseproject.com/runner", "0"))
            for node in matching_nodes
        )

        pods = api.list_namespaced_pod(label_selector="app=rise-riscv-runner", namespace="default")
        active = sum(
            1 for p in pods.items
            if p.status.phase in ("Pending", "Running")
            and p.spec.node_selector == node_selector
        )

        available = total - active
        logger.debug("Capacity check: node_selector=%s, total=%d, active=%d, available=%d",
                     node_selector, total, active, available)
        return available > 0


def get_pod_events(pod_name):
    """Get events for a specific pod, sorted by last timestamp."""
    with _init_client() as client:
        api = k8s.client.CoreV1Api(client)
        events = api.list_namespaced_event(field_selector=f"involvedObject.name={pod_name}", namespace="default")
        sorted_events = sorted(
            events.items,
            key=lambda e: e.last_timestamp or e.event_time or e.metadata.creation_timestamp,
        )
        return sorted_events


def list_pods():
    """Get all runner pods."""
    with _init_client() as client:
        api = k8s.client.CoreV1Api(client)
        pods = api.list_namespaced_pod(label_selector="app=rise-riscv-runner", namespace="default")
        return pods.items


def get_pod_logs(pod_name: str, container: str) -> str | None:
    """Get full logs for a container in a pod. Returns log string or None on failure."""
    try:
        with _init_client() as client:
            api = k8s.client.CoreV1Api(client)
            return api.read_namespaced_pod_log(
                name=pod_name,
                namespace="default",
                container=container,
            )
    except Exception as e:
        logger.debug("Failed to get logs for %s/%s: %s", pod_name, container, e)
        return None


def get_runner_running_at(pod) -> datetime.datetime | None:
    """When the 'runner' container actually began running. Best-effort."""
    for cs in (pod.status.container_statuses or []):
        if cs.name == "runner" and cs.state and cs.state.running:
            return cs.state.running.started_at
    for cond in (pod.status.conditions or []):
        if cond.type == "Ready" and cond.status == "True":
            return cond.last_transition_time
    return None


def get_pod_finished_at(pod) -> datetime.datetime | None:
    """Latest container termination time for Succeeded/Failed pods."""
    finishes = []
    for cs in (pod.status.container_statuses or []) + (pod.status.init_container_statuses or []):
        if cs.state and cs.state.terminated and cs.state.terminated.finished_at:
            finishes.append(cs.state.terminated.finished_at)
    return max(finishes) if finishes else None


def collect_pod_failure_info(pod, reason: FailureReason) -> dict:
    """Collect exhaustive diagnostic info from a pod for the workers.failure_info column.

    Gathers container termination/running info, full container logs, and pod events.
    Safe to call on Running or Pending pods too (logs are read live).
    Callers must pass a FailureReason to describe why the worker is being failed.
    """
    assert isinstance(reason, FailureReason), "reason must be a FailureReason enum value"
    pod_name = pod.metadata.name
    info = {
        "version": 2, # bump when the structure changes
        "reason": reason.value,
        "containers": {},
        "events": [],
        "pod_message": pod.status.message,
        "pod_reason": pod.status.reason,
    }

    # Container termination info + logs (main containers)
    for cs in (pod.status.container_statuses or []):
        container_info = _extract_container_info(cs)
        container_info["logs"] = get_pod_logs(pod_name, cs.name)
        info["containers"][cs.name] = container_info

    # Init container termination info + logs (dind sidecar is an init container)
    for cs in (pod.status.init_container_statuses or []):
        container_info = _extract_container_info(cs)
        container_info["logs"] = get_pod_logs(pod_name, cs.name)
        info["containers"][cs.name] = container_info

    # Pod events
    try:
        events = get_pod_events(pod_name)
        for ev in events:
            ts = ev.last_timestamp or ev.event_time or ev.metadata.creation_timestamp
            info["events"].append({
                "type": ev.type,
                "reason": ev.reason,
                "message": ev.message,
                "count": ev.count,
                "first_seen": str(ev.first_timestamp) if ev.first_timestamp else None,
                "last_seen": str(ts) if ts else None,
            })
    except Exception as e:
        logger.debug("Failed to get events for %s: %s", pod_name, e)

    return info


def _extract_container_info(container_status) -> dict:
    """Extract termination info from a V1ContainerStatus."""
    result = {
        "exit_code": None,
        "reason": None,
        "message": None,
    }
    if container_status.state and container_status.state.terminated:
        t = container_status.state.terminated
        result["exit_code"] = t.exit_code
        result["reason"] = t.reason
        result["message"] = t.message
    elif container_status.state and container_status.state.waiting:
        w = container_status.state.waiting
        result["reason"] = w.reason
        result["message"] = w.message
    return result
