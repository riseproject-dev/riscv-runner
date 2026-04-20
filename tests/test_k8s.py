import pytest
from unittest.mock import patch, MagicMock
import kubernetes

from k8s import (
    provision_runner,
    delete_pod,
    kill_pod,
)


# --- Pod provisioning ---

@patch('k8s._init_client')
@patch('k8s.k8s.client.CoreV1Api')
def test_provision_runner_success(mock_core_v1_api, mock_init_client):
    """Test successful k8s provisioning with pool-based signature."""
    mock_api_client = MagicMock()
    mock_init_client.return_value = mock_api_client
    mock_api_client.__enter__ = MagicMock(return_value=mock_api_client)
    mock_api_client.__exit__ = MagicMock(return_value=False)

    mock_api_instance = MagicMock()
    mock_core_v1_api.return_value = mock_api_instance

    provision_runner("base64-jit-config", "runner-1", "test-image:latest", "scw-em-rv1", 1000, "entity-abc")

    mock_api_instance.create_namespaced_pod.assert_called_once()

    call_args = mock_api_instance.create_namespaced_pod.call_args
    pod_manifest = call_args[1]['body']
    assert pod_manifest['metadata']['name'] == "runner-1"
    assert pod_manifest['metadata']['labels']['app'] == "rise-riscv-runner"
    assert pod_manifest['metadata']['labels']['riseproject.com/entity_id'] == "1000"
    assert pod_manifest['metadata']['labels']['riseproject.com/entity_name'] == "entity-abc"
    assert pod_manifest['metadata']['labels']['riseproject.com/board'] == "scw-em-rv1"
    assert 'riseproject.com/job_id' not in pod_manifest['metadata']['labels']
    assert pod_manifest['spec']['nodeSelector'] == {"riseproject.dev/board": "scw-em-rv1"}
    assert pod_manifest['spec']['containers'][0]['image'] == "test-image:latest"
    assert "--jitconfig" in pod_manifest['spec']['containers'][0]['args'][0]


def test_provision_runner_config_exception():
    """Test k8s provisioning failure due to missing K8S_KUBECONFIG."""
    import k8s
    k8s._init_client.cache_clear()
    try:
        with pytest.raises(Exception):
            provision_runner("jit-config", "test-pod", "img", "scw-em-rv1", 99999, "entity-abc")
    finally:
        k8s._init_client.cache_clear()


@patch('k8s._init_client', side_effect=Exception("Test API Error"))
def test_provision_runner_api_exception(mock_init_client):
    """Test k8s provisioning failure due to a generic API error."""
    with pytest.raises(Exception) as excinfo:
        provision_runner("jit-config", "test-pod", "img", "scw-em-rv1", 99999, "entity-abc")
    assert "Test API Error" == str(excinfo.value)


# --- Pod deletion ---

@patch('k8s._init_client')
@patch('k8s.k8s.client.CoreV1Api')
def test_delete_pod_success(mock_core_v1_api, mock_init_client):
    mock_api_client = MagicMock()
    mock_init_client.return_value = mock_api_client
    mock_api_client.__enter__ = MagicMock(return_value=mock_api_client)
    mock_api_client.__exit__ = MagicMock(return_value=False)

    mock_api_instance = MagicMock()
    mock_core_v1_api.return_value = mock_api_instance

    mock_pod = MagicMock()
    mock_pod.metadata.name = "runner-1"

    result = delete_pod(mock_pod)
    assert "deleted successfully" in result
    mock_api_instance.delete_namespaced_pod.assert_called_once_with(
        name="runner-1", namespace="default"
    )


@patch('k8s._init_client')
@patch('k8s.k8s.client.CoreV1Api')
def test_delete_pod_not_found(mock_core_v1_api, mock_init_client):
    mock_api_client = MagicMock()
    mock_init_client.return_value = mock_api_client
    mock_api_client.__enter__ = MagicMock(return_value=mock_api_client)
    mock_api_client.__exit__ = MagicMock(return_value=False)

    mock_api_instance = MagicMock()
    mock_core_v1_api.return_value = mock_api_instance
    mock_api_instance.delete_namespaced_pod.side_effect = kubernetes.client.exceptions.ApiException(status=404)

    mock_pod = MagicMock()
    mock_pod.metadata.name = "runner-1"

    result = delete_pod(mock_pod)
    assert "not found" in result


# --- Pod kill ---

@patch('k8s._init_client')
@patch('k8s.k8s.client.CoreV1Api')
def test_kill_pod_patches_active_deadline_seconds(mock_core_v1_api, mock_init_client):
    """kill_pod patches spec.activeDeadlineSeconds=1 so kubelet transitions
    the pod to Failed with reason=DeadlineExceeded."""
    mock_api_client = MagicMock()
    mock_init_client.return_value = mock_api_client
    mock_api_client.__enter__ = MagicMock(return_value=mock_api_client)
    mock_api_client.__exit__ = MagicMock(return_value=False)

    mock_api_instance = MagicMock()
    mock_core_v1_api.return_value = mock_api_instance

    mock_pod = MagicMock()
    mock_pod.metadata.name = "runner-1"

    kill_pod(mock_pod)
    mock_api_instance.patch_namespaced_pod.assert_called_once_with(
        name="runner-1", namespace="default",
        body={"spec": {"activeDeadlineSeconds": 1}},
    )


@patch('k8s._init_client')
@patch('k8s.k8s.client.CoreV1Api')
def test_kill_pod_swallows_404(mock_core_v1_api, mock_init_client):
    """If the pod is already gone, a 404 from the patch API must not propagate."""
    mock_api_client = MagicMock()
    mock_init_client.return_value = mock_api_client
    mock_api_client.__enter__ = MagicMock(return_value=mock_api_client)
    mock_api_client.__exit__ = MagicMock(return_value=False)

    mock_api_instance = MagicMock()
    mock_core_v1_api.return_value = mock_api_instance
    mock_api_instance.patch_namespaced_pod.side_effect = kubernetes.client.exceptions.ApiException(status=404)

    mock_pod = MagicMock()
    mock_pod.metadata.name = "runner-1"

    kill_pod(mock_pod)  # should return without raising
