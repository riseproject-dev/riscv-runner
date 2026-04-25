import datetime
import json
from unittest.mock import patch, MagicMock

import pytest

from constants import (
    EntityType,
    POD_DELETE_GRACE_SECONDS,
    POD_PENDING_TIMEOUT_SECONDS,
    RUNNER_NAME_PREFIX,
    RUNNER_REGISTRATION_TIMEOUT_SECONDS,
)
from k8s import FailureReason
from scheduler import (
    app,
    demand_match,
    sync_jobs_state,
    sync_workers_state,
    _parse_date_param,
    _build_link_header,
    _scheduler_iteration,
)


@pytest.fixture(autouse=True)
def _default_gh_mocks():
    """Provide harmless defaults for the GH API calls Phase 3/4 always makes so
    tests that don't explicitly care about GitHub don't fall through to the real
    authenticate_app (which would try to decode a fake PEM)."""
    with patch("scheduler.gh.authenticate_app", return_value="default-token"), \
         patch("scheduler.gh.ensure_runner_group", return_value=42), \
         patch("scheduler.gh.list_runners_org_group", return_value=[]), \
         patch("scheduler.gh.list_runners_repo", return_value=[]), \
         patch("scheduler.gh.delete_runner_org"), \
         patch("scheduler.gh.delete_runner_repo"):
        yield


def make_pod(name, phase="Running", entity_id=None, board=None, creation_timestamp=None):
    """Helper to create a mock k8s pod object."""
    import datetime
    pod = MagicMock()
    pod.metadata.name = name
    pod.metadata.labels = {"app": "rise-riscv-runner"}
    if entity_id:
        pod.metadata.labels["riseproject.com/entity_id"] = str(entity_id)
    if board:
        pod.metadata.labels["riseproject.com/board"] = board
    pod.metadata.creation_timestamp = creation_timestamp or datetime.datetime.now(datetime.timezone.utc)
    pod.status.phase = phase
    pod.status.container_statuses = []
    pod.status.init_container_statuses = []
    pod.status.conditions = []
    return pod


def make_job(job_id, entity_id="1000", entity_name="test-org", k8s_pool="scw-em-rv1",
             status="pending", installation_id="999", repo_full_name="test-org/repo",
             entity_type=EntityType.ORGANIZATION, provider="github"):
    """Helper to create a mock job dict matching what get_pending_jobs returns."""
    return {
        "status": status,
        "job_id": str(job_id),
        "provider": provider,
        "entity_id": str(entity_id),
        "entity_name": entity_name,
        "entity_type": entity_type.value,
        "repo_full_name": repo_full_name,
        "installation_id": str(installation_id),
        "job_labels": ["ubuntu-24.04-riscv"],
        "k8s_pool": k8s_pool,
        "k8s_image": "test-image:latest",
        "created_at": "1000000.0",
    }


# --- demand_match tests ---

@patch("scheduler.db")
@patch("scheduler.k8s.has_available_slot", return_value=True)
@patch("scheduler.k8s.provision_runner")
@patch("scheduler.gh.authenticate_app", return_value="token-123")
@patch("scheduler.gh.ensure_runner_group", return_value=42)
@patch("scheduler.gh.create_jit_runner_config_org", return_value="jit-config-encoded")
def test_demand_match_provisions_org_job(mock_jit, mock_group, mock_auth, mock_provision, mock_slot, mock_db):
    """Test that demand_match provisions a pending org job when capacity exists."""
    job = make_job(111)
    mock_db.get_pending_jobs.return_value = [job]
    mock_db.get_pool_demand.return_value = (1, 0)  # 1 job, 0 workers = deficit
    mock_db.get_total_workers_for_entity.return_value = 0

    demand_match()

    mock_auth.assert_called_once_with(999, entity_type=EntityType.ORGANIZATION)
    mock_group.assert_called_once()
    mock_jit.assert_called_once()
    mock_provision.assert_called_once()
    mock_db.add_worker.assert_called_once()


@patch("scheduler.db")
@patch("scheduler.k8s.has_available_slot", return_value=True)
@patch("scheduler.k8s.provision_runner")
@patch("scheduler.gh.authenticate_app", return_value="token-123")
@patch("scheduler.gh.create_jit_runner_config_repo", return_value="jit-config-repo")
def test_demand_match_provisions_personal_job(mock_jit_repo, mock_auth, mock_provision, mock_slot, mock_db):
    """Test that demand_match provisions a pending personal account job (repo-scoped)."""
    job = make_job(222, entity_id="200", entity_name="someuser", entity_type=EntityType.USER,
                   repo_full_name="someuser/myrepo")
    mock_db.get_pending_jobs.return_value = [job]
    mock_db.get_pool_demand.return_value = (1, 0)
    mock_db.get_total_workers_for_entity.return_value = 0

    demand_match()

    mock_auth.assert_called_once_with(999, entity_type=EntityType.USER)
    mock_jit_repo.assert_called_once()
    # Verify repo_full_name is passed
    call_args = mock_jit_repo.call_args
    assert call_args[0][2] == "someuser/myrepo"  # repo_full_name
    mock_provision.assert_called_once()
    mock_db.add_worker.assert_called_once()


@patch("scheduler.db")
@patch("scheduler.k8s.has_available_slot", return_value=True)
@patch("scheduler.k8s.provision_runner")
def test_demand_match_skips_when_demand_met(mock_provision, mock_slot, mock_db):
    """Test that jobs are skipped when pool demand is already met."""
    job = make_job(111)
    mock_db.get_pending_jobs.return_value = [job]
    mock_db.get_pool_demand.return_value = (1, 1)  # demand met

    demand_match()

    mock_provision.assert_not_called()


@patch("scheduler.db")
@patch("scheduler.k8s.has_available_slot", return_value=False)
@patch("scheduler.k8s.provision_runner")
def test_demand_match_skips_no_k8s_capacity(mock_provision, mock_slot, mock_db):
    """Test that jobs are skipped when no k8s capacity."""
    job = make_job(111)
    mock_db.get_pending_jobs.return_value = [job]
    mock_db.get_pool_demand.return_value = (1, 0)
    mock_db.get_total_workers_for_entity.return_value = 0

    demand_match()

    mock_provision.assert_not_called()


@patch("scheduler.db")
@patch("scheduler.k8s.has_available_slot", return_value=True)
@patch("scheduler.k8s.provision_runner")
def test_demand_match_respects_max_workers(mock_provision, mock_slot, mock_db):
    """Test that max_workers cap is respected."""
    job = make_job(111, entity_id="660779", entity_name="luhenry")  # max_workers defaults to 20
    mock_db.get_pending_jobs.return_value = [job]
    mock_db.get_pool_demand.return_value = (1, 0)
    mock_db.get_total_workers_for_entity.return_value = 20  # at default cap

    demand_match()

    mock_provision.assert_not_called()


@patch("scheduler.db")
@patch("scheduler.k8s.has_available_slot", return_value=True)
@patch("scheduler.k8s.provision_runner", side_effect=Exception("K8s error"))
@patch("scheduler.gh.authenticate_app", return_value="token-123")
@patch("scheduler.gh.ensure_runner_group", return_value=42)
@patch("scheduler.gh.create_jit_runner_config_org", return_value="jit-config")
def test_demand_match_handles_provision_failure(mock_jit, mock_group, mock_auth, mock_provision, mock_slot, mock_db):
    """Test that provisioning failure is handled gracefully.

    add_worker is called BEFORE provision_runner to reserve the pod name.
    If provisioning fails, the orphan worker (status=pending, no pod) will
    be cleaned up by cleanup_pods() orphan detection.
    """
    job = make_job(111)
    mock_db.get_pending_jobs.return_value = [job]
    mock_db.get_pool_demand.return_value = (1, 0)
    mock_db.get_total_workers_for_entity.return_value = 0

    demand_match()  # should not raise

    # add_worker is called before provision_runner to reserve the name
    mock_db.add_worker.assert_called_once()


# --- sync_workers_state helpers + tests ---


def make_worker(pod_name="pod-1", status="pending", entity_id="1000",
                 entity_name="test-org", entity_type=EntityType.ORGANIZATION,
                 installation_id="999", repo_full_name=None,
                 running_at=None, k8s_node=None, k8s_pool="scw-em-rv1"):
    """Build a worker row dict mirroring what get_workers_for_reconcile returns."""
    if entity_type == EntityType.USER and repo_full_name is None:
        repo_full_name = "someuser/myrepo"
    return {
        "pod_name": pod_name,
        "status": status,
        "entity_id": entity_id,
        "entity_name": entity_name,
        "entity_type": entity_type.value,
        "installation_id": installation_id,
        "repo_full_name": repo_full_name,
        "running_at": running_at,
        "k8s_node": k8s_node,
        "k8s_pool": k8s_pool,
    }


def make_running_pod(name, running_at=None, node_name="node-1"):
    """Running pod with a 'runner' container whose running.started_at is set."""
    pod = make_pod(name, phase="Running")
    pod.spec.node_name = node_name
    cs = MagicMock()
    cs.name = "runner"
    cs.state.running.started_at = running_at or datetime.datetime.now(datetime.timezone.utc)
    cs.state.terminated = None
    cs.state.waiting = None
    pod.status.container_statuses = [cs]
    return pod


def make_terminal_pod(name, phase="Succeeded", finished_at=None, node_name="node-1"):
    """Pod that has reached a terminal phase, with a container.state.terminated.finished_at."""
    pod = make_pod(name, phase=phase)
    pod.spec.node_name = node_name
    cs = MagicMock()
    cs.name = "runner"
    cs.state.terminated.finished_at = finished_at or datetime.datetime.now(datetime.timezone.utc)
    cs.state.running = None
    cs.state.waiting = None
    pod.status.container_statuses = [cs]
    return pod


def _configure_db_mock(mock_db, workers=None):
    """Wire up the mock db so sync_workers_state can enter `hold_connection` and
    the mock's in-memory worker state transitions in response to mark_worker_*
    calls — mirroring how PostgreSQL would behave between the reconcile's
    get_workers_for_reconcile refreshes.
    """
    state = {w["pod_name"]: dict(w) for w in (workers or [])}

    def get_workers():
        return [dict(w) for w in state.values()]

    def _set(pod_name, status, **extra):
        if pod_name in state:
            state[pod_name]["status"] = status
            state[pod_name].update(extra)

    mock_db.get_workers_for_reconcile.side_effect = get_workers
    mock_db.job_exists_for_pod.return_value = False
    mock_db.mark_worker_orphaned.side_effect = lambda pn: _set(pn, "completed")
    mock_db.mark_worker_running.side_effect = lambda pn, node, ra: _set(
        pn, "running", k8s_node=node, running_at=ra)
    mock_db.mark_worker_completed.side_effect = lambda pn, node, ca: _set(
        pn, "completed", k8s_node=node, completed_at=ca)
    mock_db.mark_worker_failed.side_effect = lambda pn, node, fi, ca: _set(
        pn, "failed", k8s_node=node, failure_info=fi, completed_at=ca)

    conn = MagicMock()
    cur = MagicMock()
    conn.cursor.return_value.__enter__ = MagicMock(return_value=cur)
    conn.cursor.return_value.__exit__ = MagicMock(return_value=False)
    mock_db.hold_connection.return_value.__enter__ = MagicMock(return_value=conn)
    mock_db.hold_connection.return_value.__exit__ = MagicMock(return_value=False)
    return conn, cur


# Phase 1 — orphan sweep

@patch("scheduler.db")
@patch("scheduler.k8s.list_pods", return_value=[])
def test_reconcile_orphans_worker_with_no_pod(mock_list, mock_db):
    worker = make_worker(pod_name="pod-1", status="pending")
    _configure_db_mock(mock_db, workers=[worker])

    sync_workers_state()

    mock_db.mark_worker_orphaned.assert_called_once_with("pod-1")


@patch("scheduler.db")
@patch("scheduler.k8s.list_pods", return_value=[])
def test_reconcile_does_not_orphan_terminal_workers(mock_list, mock_db):
    w1 = make_worker(pod_name="pod-completed", status="completed")
    w2 = make_worker(pod_name="pod-failed", status="failed")
    _configure_db_mock(mock_db, workers=[w1, w2])

    sync_workers_state()

    mock_db.mark_worker_orphaned.assert_not_called()


@patch("scheduler.db")
@patch("scheduler.k8s.list_pods")
def test_reconcile_does_not_orphan_worker_with_matching_pod(mock_list, mock_db):
    pod = make_running_pod("pod-1")
    mock_list.return_value = [pod]
    worker = make_worker(pod_name="pod-1", status="running")
    _configure_db_mock(mock_db, workers=[worker])

    sync_workers_state()

    mock_db.mark_worker_orphaned.assert_not_called()


# Phase 2 — pod phase -> worker status sync

@patch("scheduler.db")
@patch("scheduler.k8s.list_pods")
def test_reconcile_syncs_pending_to_running(mock_list, mock_db):
    started_at = datetime.datetime(2026, 1, 1, tzinfo=datetime.timezone.utc)
    pod = make_running_pod("pod-1", running_at=started_at)
    mock_list.return_value = [pod]
    worker = make_worker(pod_name="pod-1", status="pending")
    _configure_db_mock(mock_db, workers=[worker])

    sync_workers_state()

    mock_db.mark_worker_running.assert_called_once()
    args = mock_db.mark_worker_running.call_args[0]
    assert args[0] == "pod-1"
    assert args[2] == started_at


@patch("scheduler.db")
@patch("scheduler.k8s.list_pods")
def test_reconcile_syncs_to_completed_on_succeeded(mock_list, mock_db):
    finished_at = datetime.datetime(2026, 1, 1, tzinfo=datetime.timezone.utc)
    pod = make_terminal_pod("pod-1", phase="Succeeded", finished_at=finished_at)
    mock_list.return_value = [pod]
    worker = make_worker(pod_name="pod-1", status="running")
    _configure_db_mock(mock_db, workers=[worker])

    sync_workers_state()

    mock_db.mark_worker_completed.assert_called_once()
    args = mock_db.mark_worker_completed.call_args[0]
    assert args[0] == "pod-1"
    assert args[2] == finished_at


@patch("scheduler.db")
@patch("scheduler.k8s.list_pods")
@patch("scheduler.k8s.collect_pod_failure_info", return_value={"version": 2, "reason": "pod_failed"})
def test_reconcile_syncs_to_failed_on_pod_failed(mock_collect, mock_list, mock_db):
    finished_at = datetime.datetime(2026, 1, 1, tzinfo=datetime.timezone.utc)
    pod = make_terminal_pod("pod-1", phase="Failed", finished_at=finished_at)
    mock_list.return_value = [pod]
    worker = make_worker(pod_name="pod-1", status="running")
    _configure_db_mock(mock_db, workers=[worker])

    sync_workers_state()

    mock_db.mark_worker_failed.assert_called_once()
    args = mock_db.mark_worker_failed.call_args[0]
    assert args[0] == "pod-1"
    assert args[2]["reason"] == "pod_failed"


@patch("scheduler.db")
@patch("scheduler.k8s.list_pods")
def test_reconcile_skips_sync_when_worker_already_terminal(mock_list, mock_db):
    pod = make_running_pod("pod-1")
    mock_list.return_value = [pod]
    worker = make_worker(pod_name="pod-1", status="completed")
    _configure_db_mock(mock_db, workers=[worker])

    sync_workers_state()

    mock_db.mark_worker_running.assert_not_called()
    mock_db.mark_worker_failed.assert_not_called()


# Phase 3 — stuck-runner health checks

@patch("scheduler.db")
@patch("scheduler.k8s.list_pods")
@patch("scheduler.k8s.kill_pod")
@patch("scheduler.k8s.collect_pod_failure_info", return_value={"version": 2, "reason": "runner_never_registered"})
@patch("scheduler.gh.authenticate_app", return_value="token-123")
@patch("scheduler.gh.ensure_runner_group", return_value=42)
@patch("scheduler.gh.list_runners_org_group", return_value=[])
def test_reconcile_fails_runner_that_never_registered_past_timeout(
        mock_list_gh, mock_group, mock_auth, mock_collect, mock_kill, mock_list_pods, mock_db):
    old = datetime.datetime.now(datetime.timezone.utc) - datetime.timedelta(
        seconds=RUNNER_REGISTRATION_TIMEOUT_SECONDS + 30)
    pod = make_running_pod("rise-riscv-runner-staging-pod-1")
    mock_list_pods.return_value = [pod]
    worker = make_worker(pod_name="rise-riscv-runner-staging-pod-1",
                         status="running", running_at=old)
    _configure_db_mock(mock_db, workers=[worker])

    sync_workers_state()

    mock_db.mark_worker_failed.assert_called_once()
    args = mock_db.mark_worker_failed.call_args[0]
    assert args[0] == "rise-riscv-runner-staging-pod-1"
    assert args[2]["reason"] == "runner_never_registered"
    mock_kill.assert_called_once_with(pod)


@patch("scheduler.db")
@patch("scheduler.k8s.list_pods")
@patch("scheduler.k8s.kill_pod")
@patch("scheduler.gh.authenticate_app", return_value="token-123")
@patch("scheduler.gh.ensure_runner_group", return_value=42)
@patch("scheduler.gh.list_runners_org_group", return_value=[])
def test_reconcile_skips_runner_that_already_ran_a_job(
        mock_list_gh, mock_group, mock_auth, mock_kill, mock_list_pods, mock_db):
    """A runner missing from GH but with a matching jobs.k8s_pod row has already
    run its job and self-unregistered — do not flag as runner_never_registered."""
    old = datetime.datetime.now(datetime.timezone.utc) - datetime.timedelta(
        seconds=RUNNER_REGISTRATION_TIMEOUT_SECONDS + 30)
    name = "rise-riscv-runner-staging-pod-1"
    pod = make_running_pod(name)
    mock_list_pods.return_value = [pod]
    worker = make_worker(pod_name=name, status="running", running_at=old)
    _configure_db_mock(mock_db, workers=[worker])
    mock_db.job_exists_for_pod.return_value = True

    sync_workers_state()

    mock_db.job_exists_for_pod.assert_called_with(name)
    mock_db.mark_worker_failed.assert_not_called()
    mock_kill.assert_not_called()


@patch("scheduler.db")
@patch("scheduler.k8s.list_pods")
@patch("scheduler.k8s.kill_pod")
@patch("scheduler.gh.authenticate_app", return_value="token-123")
@patch("scheduler.gh.ensure_runner_group", return_value=42)
@patch("scheduler.gh.list_runners_org_group", return_value=[])
def test_reconcile_keeps_runner_within_registration_timeout(
        mock_list_gh, mock_group, mock_auth, mock_kill, mock_list_pods, mock_db):
    recent = datetime.datetime.now(datetime.timezone.utc) - datetime.timedelta(seconds=10)
    pod = make_running_pod("rise-riscv-runner-staging-pod-1")
    mock_list_pods.return_value = [pod]
    worker = make_worker(pod_name="rise-riscv-runner-staging-pod-1",
                         status="running", running_at=recent)
    _configure_db_mock(mock_db, workers=[worker])

    sync_workers_state()

    mock_db.mark_worker_failed.assert_not_called()
    mock_kill.assert_not_called()


@patch("scheduler.db")
@patch("scheduler.k8s.list_pods")
@patch("scheduler.k8s.kill_pod")
@patch("scheduler.gh.authenticate_app", return_value="token-123")
@patch("scheduler.gh.ensure_runner_group", return_value=42)
@patch("scheduler.gh.list_runners_org_group")
def test_reconcile_keeps_registered_runner(
        mock_list_gh, mock_group, mock_auth, mock_kill, mock_list_pods, mock_db):
    old = datetime.datetime.now(datetime.timezone.utc) - datetime.timedelta(days=1)
    name = "rise-riscv-runner-staging-pod-1"
    pod = make_running_pod(name)
    mock_list_pods.return_value = [pod]
    mock_list_gh.return_value = [{"id": 11, "name": name, "status": "online"}]
    worker = make_worker(pod_name=name, status="running", running_at=old)
    _configure_db_mock(mock_db, workers=[worker])

    sync_workers_state()

    mock_db.mark_worker_failed.assert_not_called()
    mock_kill.assert_not_called()


@patch("scheduler.db")
@patch("scheduler.k8s.list_pods")
@patch("scheduler.k8s.kill_pod")
@patch("scheduler.k8s.collect_pod_failure_info", return_value={"version": 2, "reason": "runner_never_registered"})
@patch("scheduler.gh.authenticate_app", return_value="token-123")
@patch("scheduler.gh.ensure_runner_group", return_value=42)
@patch("scheduler.gh.list_runners_org_group")
def test_reconcile_fails_offline_runner_past_timeout(
        mock_list_gh, mock_group, mock_auth, mock_collect, mock_kill, mock_list_pods, mock_db):
    """A runner registered with GH but reported as `offline` past the registration
    timeout must be treated like an unregistered runner and marked failed."""
    old = datetime.datetime.now(datetime.timezone.utc) - datetime.timedelta(
        seconds=RUNNER_REGISTRATION_TIMEOUT_SECONDS + 30)
    name = "rise-riscv-runner-staging-pod-1"
    pod = make_running_pod(name)
    mock_list_pods.return_value = [pod]
    mock_list_gh.return_value = [{"id": 11, "name": name, "status": "offline"}]
    worker = make_worker(pod_name=name, status="running", running_at=old)
    _configure_db_mock(mock_db, workers=[worker])

    sync_workers_state()

    mock_db.mark_worker_failed.assert_called_once()
    args = mock_db.mark_worker_failed.call_args[0]
    assert args[0] == name
    assert args[2]["reason"] == "runner_never_registered"
    mock_kill.assert_called_once_with(pod)


@patch("scheduler.db")
@patch("scheduler.k8s.list_pods")
@patch("scheduler.k8s.kill_pod")
@patch("scheduler.gh.authenticate_app", return_value="token-123")
@patch("scheduler.gh.ensure_runner_group", return_value=42)
@patch("scheduler.gh.list_runners_org_group")
def test_reconcile_keeps_offline_runner_within_timeout(
        mock_list_gh, mock_group, mock_auth, mock_kill, mock_list_pods, mock_db):
    """An offline runner within the registration timeout window must be left alone —
    it may still come online before the deadline."""
    recent = datetime.datetime.now(datetime.timezone.utc) - datetime.timedelta(seconds=10)
    name = "rise-riscv-runner-staging-pod-1"
    pod = make_running_pod(name)
    mock_list_pods.return_value = [pod]
    mock_list_gh.return_value = [{"id": 11, "name": name, "status": "offline"}]
    worker = make_worker(pod_name=name, status="running", running_at=recent)
    _configure_db_mock(mock_db, workers=[worker])

    sync_workers_state()

    mock_db.mark_worker_failed.assert_not_called()
    mock_kill.assert_not_called()


@patch("scheduler.db")
@patch("scheduler.k8s.list_pods")
@patch("scheduler.k8s.kill_pod")
@patch("scheduler.k8s.collect_pod_failure_info", return_value={"version": 2, "reason": "pod_stuck_pending"})
@patch("scheduler.gh.authenticate_app", return_value="token-123")
@patch("scheduler.gh.ensure_runner_group", return_value=42)
@patch("scheduler.gh.list_runners_org_group", return_value=[])
def test_reconcile_fails_stuck_pending_pod_past_timeout(
        mock_list_gh, mock_group, mock_auth, mock_collect, mock_kill, mock_list_pods, mock_db):
    old = datetime.datetime.now(datetime.timezone.utc) - datetime.timedelta(
        seconds=POD_PENDING_TIMEOUT_SECONDS + 60)
    pod = make_pod("rise-riscv-runner-staging-pod-1", phase="Pending", creation_timestamp=old)
    pod.spec.node_name = None
    mock_list_pods.return_value = [pod]
    worker = make_worker(pod_name="rise-riscv-runner-staging-pod-1", status="pending")
    _configure_db_mock(mock_db, workers=[worker])

    sync_workers_state()

    mock_db.mark_worker_failed.assert_called_once()
    args = mock_db.mark_worker_failed.call_args[0]
    assert args[2]["reason"] == "pod_stuck_pending"
    mock_kill.assert_called_once_with(pod)


@patch("scheduler.db")
@patch("scheduler.k8s.list_pods")
@patch("scheduler.k8s.kill_pod")
@patch("scheduler.gh.authenticate_app", return_value="token-123")
@patch("scheduler.gh.ensure_runner_group", return_value=42)
@patch("scheduler.gh.list_runners_org_group", return_value=[])
def test_reconcile_keeps_pending_pod_within_timeout(
        mock_list_gh, mock_group, mock_auth, mock_kill, mock_list_pods, mock_db):
    pod = make_pod("rise-riscv-runner-staging-pod-1", phase="Pending")
    pod.spec.node_name = None
    mock_list_pods.return_value = [pod]
    worker = make_worker(pod_name="rise-riscv-runner-staging-pod-1", status="pending")
    _configure_db_mock(mock_db, workers=[worker])

    sync_workers_state()

    mock_db.mark_worker_failed.assert_not_called()
    mock_kill.assert_not_called()


@patch("scheduler.db")
@patch("scheduler.k8s.list_pods")
@patch("scheduler.k8s.kill_pod", side_effect=Exception("k8s patch failed"))
@patch("scheduler.k8s.collect_pod_failure_info", return_value={"version": 2, "reason": "runner_never_registered"})
@patch("scheduler.gh.authenticate_app", return_value="token-123")
@patch("scheduler.gh.ensure_runner_group", return_value=42)
@patch("scheduler.gh.list_runners_org_group", return_value=[])
def test_reconcile_fail_and_cleanup_is_best_effort_on_kill_failure(
        mock_list_gh, mock_group, mock_auth, mock_collect, mock_kill, mock_list_pods, mock_db):
    """kill_pod failure must not prevent the mark_worker_failed call."""
    old = datetime.datetime.now(datetime.timezone.utc) - datetime.timedelta(
        seconds=RUNNER_REGISTRATION_TIMEOUT_SECONDS + 30)
    pod = make_running_pod("rise-riscv-runner-staging-pod-1")
    mock_list_pods.return_value = [pod]
    worker = make_worker(pod_name="rise-riscv-runner-staging-pod-1",
                         status="running", running_at=old)
    _configure_db_mock(mock_db, workers=[worker])

    sync_workers_state()

    mock_db.mark_worker_failed.assert_called_once()
    mock_kill.assert_called_once_with(pod)


@patch("scheduler.db")
@patch("scheduler.k8s.list_pods")
@patch("scheduler.k8s.kill_pod")
@patch("scheduler.k8s.collect_pod_failure_info")
@patch("scheduler.gh.authenticate_app", return_value="token-123")
@patch("scheduler.gh.ensure_runner_group", return_value=42)
@patch("scheduler.gh.list_runners_org_group")
@patch("scheduler.gh.delete_runner_org", side_effect=Exception("422 runner is busy"))
def test_reconcile_aborts_cleanup_when_github_refuses_delete(
        mock_delete, mock_list_gh, mock_group, mock_auth, mock_collect, mock_kill,
        mock_list_pods, mock_db):
    """If GitHub refuses to delete the runner (e.g. 422 "runner is busy") we must
    NOT kill the pod or mark the worker failed — GH believes the runner is doing
    useful work, so we leave it alone and try again next reconcile."""
    old = datetime.datetime.now(datetime.timezone.utc) - datetime.timedelta(
        seconds=POD_PENDING_TIMEOUT_SECONDS + 60)
    name = "rise-riscv-runner-staging-pod-1"
    pod = make_pod(name, phase="Pending", creation_timestamp=old)
    pod.spec.node_name = None
    mock_list_pods.return_value = [pod]
    # GH has the runner listed (so gh_runner is not None in _fail_and_cleanup).
    mock_list_gh.return_value = [{"id": 77, "name": name}]
    worker = make_worker(pod_name=name, status="pending")
    _configure_db_mock(mock_db, workers=[worker])

    sync_workers_state()

    # We attempted the delete.
    mock_delete.assert_called_once_with("token-123", "test-org", 77)
    # But aborted before killing / marking failed / collecting diagnostics.
    mock_kill.assert_not_called()
    mock_db.mark_worker_failed.assert_not_called()
    mock_collect.assert_not_called()


@patch("scheduler.db")
@patch("scheduler.k8s.list_pods")
@patch("scheduler.k8s.kill_pod")
@patch("scheduler.k8s.collect_pod_failure_info", side_effect=RuntimeError("boom"))
@patch("scheduler.gh.authenticate_app", return_value="token-123")
@patch("scheduler.gh.ensure_runner_group", return_value=42)
@patch("scheduler.gh.list_runners_org_group", return_value=[])
def test_reconcile_fail_and_cleanup_is_best_effort_on_collect_failure(
        mock_list_gh, mock_group, mock_auth, mock_collect, mock_kill, mock_list_pods, mock_db):
    """collect_pod_failure_info exception -> mark_worker_failed still called with fallback."""
    old = datetime.datetime.now(datetime.timezone.utc) - datetime.timedelta(
        seconds=RUNNER_REGISTRATION_TIMEOUT_SECONDS + 30)
    pod = make_running_pod("rise-riscv-runner-staging-pod-1")
    mock_list_pods.return_value = [pod]
    worker = make_worker(pod_name="rise-riscv-runner-staging-pod-1",
                         status="running", running_at=old)
    _configure_db_mock(mock_db, workers=[worker])

    sync_workers_state()

    mock_db.mark_worker_failed.assert_called_once()
    failure_info = mock_db.mark_worker_failed.call_args[0][2]
    assert failure_info["reason"] == "runner_never_registered"
    assert "collect_error" in failure_info


# Phase 4 — GH-side cleanup

@patch("scheduler.db")
@patch("scheduler.k8s.list_pods", return_value=[])
@patch("scheduler.gh.authenticate_app", return_value="token-123")
@patch("scheduler.gh.ensure_runner_group", return_value=42)
@patch("scheduler.gh.list_runners_org_group")
@patch("scheduler.gh.delete_runner_org")
def test_reconcile_deletes_gh_runner_for_terminal_worker(
        mock_delete, mock_list_gh, mock_group, mock_auth, mock_list_pods, mock_db):
    """A terminal worker's GH runner must be deleted.

    Phase 3 listing is per-scope and driven off pending/running workers; we need at
    least one pending/running worker in the same scope so that the listing is fetched
    and Phase 4 sees the terminal runner.
    """
    name = "rise-riscv-runner-staging-pod-1"
    active_pod = make_running_pod("rise-riscv-runner-staging-pod-active")
    mock_list_pods.return_value = [active_pod]
    mock_list_gh.return_value = [{"id": 11, "name": name, "status": "online"},
                                  {"id": 12, "name": "rise-riscv-runner-staging-pod-active", "status": "online"}]
    terminal_worker = make_worker(pod_name=name, status="completed")
    active_worker = make_worker(pod_name="rise-riscv-runner-staging-pod-active",
                                status="running",
                                running_at=datetime.datetime.now(datetime.timezone.utc))
    _configure_db_mock(mock_db, workers=[terminal_worker, active_worker])

    sync_workers_state()

    mock_delete.assert_any_call("token-123", "test-org", 11)


@patch("scheduler.db")
@patch("scheduler.k8s.list_pods")
@patch("scheduler.gh.authenticate_app", return_value="token-123")
@patch("scheduler.gh.ensure_runner_group", return_value=42)
@patch("scheduler.gh.list_runners_org_group")
@patch("scheduler.gh.delete_runner_org")
def test_reconcile_deletes_gh_runner_with_no_worker_row(
        mock_delete, mock_list_gh, mock_group, mock_auth, mock_list_pods, mock_db):
    active_name = "rise-riscv-runner-staging-pod-active"
    orphan_name = "rise-riscv-runner-staging-pod-orphan"
    pod = make_running_pod(active_name)
    mock_list_pods.return_value = [pod]
    mock_list_gh.return_value = [{"id": 11, "name": active_name, "status": "online"},
                                  {"id": 99, "name": orphan_name, "status": "online"}]
    active_worker = make_worker(pod_name=active_name, status="running",
                                running_at=datetime.datetime.now(datetime.timezone.utc))
    _configure_db_mock(mock_db, workers=[active_worker])

    sync_workers_state()

    mock_delete.assert_any_call("token-123", "test-org", 99)


@patch("scheduler.db")
@patch("scheduler.k8s.list_pods")
@patch("scheduler.gh.authenticate_app", return_value="token-123")
@patch("scheduler.gh.ensure_runner_group", return_value=42)
@patch("scheduler.gh.list_runners_org_group")
@patch("scheduler.gh.delete_runner_org")
def test_reconcile_keeps_gh_runner_with_active_worker(
        mock_delete, mock_list_gh, mock_group, mock_auth, mock_list_pods, mock_db):
    name = "rise-riscv-runner-staging-pod-1"
    pod = make_running_pod(name)
    mock_list_pods.return_value = [pod]
    mock_list_gh.return_value = [{"id": 11, "name": name, "status": "online"}]
    worker = make_worker(pod_name=name, status="running",
                         running_at=datetime.datetime.now(datetime.timezone.utc))
    _configure_db_mock(mock_db, workers=[worker])

    sync_workers_state()

    mock_delete.assert_not_called()


@patch("scheduler.db")
@patch("scheduler.k8s.list_pods")
@patch("scheduler.gh.authenticate_app", return_value="token-123")
@patch("scheduler.gh.ensure_runner_group", return_value=42)
@patch("scheduler.gh.list_runners_org_group")
@patch("scheduler.gh.delete_runner_org")
def test_reconcile_skips_gh_runners_without_prefix(
        mock_delete, mock_list_gh, mock_group, mock_auth, mock_list_pods, mock_db):
    active_name = "rise-riscv-runner-staging-pod-active"
    pod = make_running_pod(active_name)
    mock_list_pods.return_value = [pod]
    # A foreign runner (no matching prefix) must never be deleted even if there is no
    # corresponding worker row.
    mock_list_gh.return_value = [{"id": 11, "name": active_name, "status": "online"},
                                  {"id": 99, "name": "some-other-teams-runner", "status": "online"}]
    active_worker = make_worker(pod_name=active_name, status="running",
                                running_at=datetime.datetime.now(datetime.timezone.utc))
    _configure_db_mock(mock_db, workers=[active_worker])

    sync_workers_state()

    # Only called (if at all) for runners matching our prefix — never for the foreign name.
    for call_args in mock_delete.call_args_list:
        assert call_args[0][2] != 99, "foreign runner must not be deleted"


# Phase 5 — grace period for terminal pods

@patch("scheduler.db")
@patch("scheduler.k8s.list_pods")
@patch("scheduler.k8s.delete_pod")
def test_reconcile_deletes_terminal_pod_past_grace(mock_delete, mock_list_pods, mock_db):
    old = datetime.datetime.now(datetime.timezone.utc) - datetime.timedelta(
        seconds=POD_DELETE_GRACE_SECONDS + 60)
    pod = make_terminal_pod("pod-1", phase="Succeeded", finished_at=old)
    mock_list_pods.return_value = [pod]
    _configure_db_mock(mock_db, workers=[])

    sync_workers_state()

    mock_delete.assert_called_once_with(pod)


@patch("scheduler.db")
@patch("scheduler.k8s.list_pods")
@patch("scheduler.k8s.delete_pod")
def test_reconcile_keeps_terminal_pod_within_grace(mock_delete, mock_list_pods, mock_db):
    pod = make_terminal_pod("pod-1", phase="Succeeded")
    mock_list_pods.return_value = [pod]
    _configure_db_mock(mock_db, workers=[])

    sync_workers_state()

    mock_delete.assert_not_called()


@patch("scheduler.db")
@patch("scheduler.k8s.list_pods")
@patch("scheduler.k8s.delete_pod")
def test_reconcile_does_not_delete_running_pod(mock_delete, mock_list_pods, mock_db):
    pod = make_running_pod("rise-riscv-runner-staging-pod-1")
    mock_list_pods.return_value = [pod]
    _configure_db_mock(mock_db, workers=[])

    sync_workers_state()

    mock_delete.assert_not_called()


# Cross-cutting

@patch("scheduler.db")
@patch("scheduler.k8s.list_pods")
@patch("scheduler.gh.authenticate_app", return_value="token-123")
@patch("scheduler.gh.ensure_runner_group", return_value=42)
@patch("scheduler.gh.list_runners_org_group", return_value=[])
def test_reconcile_uses_runner_group_for_org(
        mock_list_gh, mock_group, mock_auth, mock_list_pods, mock_db):
    name = "rise-riscv-runner-staging-pod-1"
    pod = make_running_pod(name)
    mock_list_pods.return_value = [pod]
    worker = make_worker(pod_name=name, status="running",
                         entity_type=EntityType.ORGANIZATION,
                         running_at=datetime.datetime.now(datetime.timezone.utc))
    _configure_db_mock(mock_db, workers=[worker])

    sync_workers_state()

    mock_group.assert_called_once()
    mock_list_gh.assert_called_once_with("token-123", "test-org", 42)


@patch("scheduler.db")
@patch("scheduler.k8s.list_pods")
@patch("scheduler.gh.authenticate_app", return_value="token-user")
@patch("scheduler.gh.list_runners_repo")
def test_reconcile_uses_repo_listing_for_user(
        mock_list_repo, mock_auth, mock_list_pods, mock_db):
    name = "rise-riscv-runner-staging-pod-1"
    pod = make_running_pod(name)
    mock_list_pods.return_value = [pod]
    # Two runners returned: one of ours, one foreign. The call-side filter in
    # _get_gh_runners strips the foreign one because it doesn't start with RUNNER_NAME_PREFIX.
    mock_list_repo.return_value = [
        {"id": 11, "name": name, "status": "online"},
        {"id": 22, "name": "random-self-hosted", "status": "online"},
    ]
    worker = make_worker(pod_name=name, status="running",
                         entity_type=EntityType.USER,
                         repo_full_name="someuser/myrepo",
                         running_at=datetime.datetime.now(datetime.timezone.utc))
    _configure_db_mock(mock_db, workers=[worker])

    sync_workers_state()

    mock_list_repo.assert_called_once_with("token-user", "someuser/myrepo")


@patch("scheduler.db")
@patch("scheduler.k8s.list_pods")
@patch("scheduler.k8s.kill_pod")
@patch("scheduler.k8s.collect_pod_failure_info", return_value={"version": 2, "reason": "runner_never_registered"})
@patch("scheduler.gh.authenticate_app", return_value="token-123")
@patch("scheduler.gh.ensure_runner_group", return_value=42)
@patch("scheduler.gh.list_runners_org_group", return_value=[])
def test_reconcile_groupby_tolerates_unsorted_workers(
        mock_list_gh, mock_group, mock_auth, mock_collect, mock_kill, mock_list_pods, mock_db):
    """Two workers share scope A; one worker is scope B. Interleaved via entity_id.

    Before the sort fix, `itertools.groupby` would put the two scope-A workers in
    separate groups and `_get_gh_runners` would still dedup via the cache, but the
    health-check loop should still iterate all three workers regardless of order.
    """
    old = datetime.datetime.now(datetime.timezone.utc) - datetime.timedelta(
        seconds=RUNNER_REGISTRATION_TIMEOUT_SECONDS + 30)
    a1 = make_worker(pod_name="rise-riscv-runner-staging-a1", status="running",
                     entity_id="1000", entity_name="org-a", running_at=old)
    b1 = make_worker(pod_name="rise-riscv-runner-staging-b1", status="running",
                     entity_id="2000", entity_name="org-b", running_at=old)
    a2 = make_worker(pod_name="rise-riscv-runner-staging-a2", status="running",
                     entity_id="1000", entity_name="org-a", running_at=old)
    mock_list_pods.return_value = [
        make_running_pod("rise-riscv-runner-staging-a1"),
        make_running_pod("rise-riscv-runner-staging-b1"),
        make_running_pod("rise-riscv-runner-staging-a2"),
    ]
    _configure_db_mock(mock_db, workers=[a1, b1, a2])  # deliberately interleaved

    sync_workers_state()

    # All three should have been checked and failed (none were in gh_by_name).
    assert mock_db.mark_worker_failed.call_count == 3
    failed_names = {c[0][0] for c in mock_db.mark_worker_failed.call_args_list}
    assert failed_names == {"rise-riscv-runner-staging-a1",
                             "rise-riscv-runner-staging-b1",
                             "rise-riscv-runner-staging-a2"}


@patch("scheduler.db")
@patch("scheduler.k8s.list_pods", return_value=[])
@patch("scheduler.sync_jobs_state")
@patch("scheduler.demand_match")
def test_sync_workers_state_takes_table_lock(mock_demand_match, mock_sync_jobs_state, mock_list_pods, mock_db):
    """Verify the LOCK TABLE statement is executed inside hold_connection."""
    executed = []
    cur = MagicMock()
    def record_execute(sql, *args):
        executed.append(sql)
    cur.execute.side_effect = record_execute
    conn = MagicMock()
    conn.cursor.return_value.__enter__ = MagicMock(return_value=cur)
    conn.cursor.return_value.__exit__ = MagicMock(return_value=False)
    mock_db.hold_connection.return_value.__enter__ = MagicMock(return_value=conn)
    mock_db.hold_connection.return_value.__exit__ = MagicMock(return_value=False)
    mock_db.get_workers_for_reconcile.return_value = []

    _scheduler_iteration()

    assert any("LOCK TABLE workers IN EXCLUSIVE MODE" in s for s in executed)


# --- sync_jobs_state tests ---

@patch("scheduler.db")
@patch("scheduler.gh.authenticate_app", return_value="token-123")
@patch("scheduler.gh.get_job_info", return_value={"status": "completed", "runner_name": "my-runner"})
def test_gh_reconcile_jobs_completes_job(mock_status, mock_auth, mock_db):
    """Reconciliation marks a job completed when GH says so."""
    job = make_job(111, status="running")
    mock_db.get_active_jobs.return_value = [job]

    sync_jobs_state()

    mock_db.mark_job_completed.assert_called_once_with("111", "my-runner")


@patch("scheduler.db")
@patch("scheduler.gh.authenticate_app", return_value="token-123")
@patch("scheduler.gh.get_job_info", return_value={"status": "in_progress", "runner_name": "my-runner"})
def test_gh_reconcile_jobs_updates_running(mock_status, mock_auth, mock_db):
    """Reconciliation updates pending→running when GH says in_progress."""
    job = make_job(111, status="pending")
    mock_db.get_active_jobs.return_value = [job]

    sync_jobs_state()

    mock_db.mark_job_running.assert_called_once_with("111", "my-runner")


@patch("scheduler.db")
@patch("scheduler.gh.authenticate_app", return_value="token-123")
@patch("scheduler.gh.get_job_info")
def test_gh_reconcile_jobs_marks_job_failed_on_404(mock_status, mock_auth, mock_db):
    """A 404 from get_job_info marks the job as failed."""
    from github import GitHubAPIError

    job = make_job(111, status="running")
    mock_db.get_active_jobs.return_value = [job]
    mock_status.side_effect = GitHubAPIError(404, "Not Found")

    sync_jobs_state()

    mock_db.mark_job_failed.assert_called_once()
    call_args = mock_db.mark_job_failed.call_args[0]
    assert call_args[0] == "111"
    assert "version" in call_args[1] and isinstance(call_args[1]["version"], int) and call_args[1]["version"] >= 1
    assert "job not found" in call_args[1]["message"]


@patch("scheduler.db")
@patch("scheduler.gh.authenticate_app")
def test_gh_reconcile_jobs_marks_job_failed_on_installation_404(mock_auth, mock_db):
    """A 404 from authenticate_app marks the job as failed (per-job now, since we iterate flatly)."""
    from github import GitHubAPIError

    jobs = [make_job(111, status="running"), make_job(222, status="pending")]
    mock_db.get_active_jobs.return_value = jobs
    mock_auth.side_effect = GitHubAPIError(404, "Not Found")

    sync_jobs_state()

    assert mock_db.mark_job_failed.call_count == 2
    job_ids = [call_args[0][0] for call_args in mock_db.mark_job_failed.call_args_list]
    assert set(job_ids) == {"111", "222"}
    for call_args in mock_db.mark_job_failed.call_args_list:
        assert "version" in call_args[0][1] and isinstance(call_args[0][1]["version"], int) and call_args[0][1]["version"] >= 1
        assert "installation not found" in call_args[0][1]["message"]


@patch("scheduler.db")
def test_gh_reconcile_jobs_no_active_jobs(mock_db):
    """No-op when no active jobs."""
    mock_db.get_active_jobs.return_value = []

    sync_jobs_state()

    mock_db.mark_job_completed.assert_not_called()
    mock_db.mark_job_running.assert_not_called()


# --- _parse_date_param tests ---

def test_parse_date_param_none():
    assert _parse_date_param(None) is None

def test_parse_date_param_iso():
    assert _parse_date_param("2026-01-15") == "2026-01-15"

def test_parse_date_param_relative():
    import datetime
    result = _parse_date_param("-7d")
    expected = (datetime.date.today() - datetime.timedelta(days=7)).isoformat()
    assert result == expected

def test_parse_date_param_zero_days():
    import datetime
    assert _parse_date_param("-0d") == datetime.date.today().isoformat()


# --- _build_link_header tests ---

def test_link_header_first_page():
    link = _build_link_header("http://example.com/history", page=0, per_page=10, total=50)
    assert 'rel="next"' in link
    assert 'rel="last"' in link
    assert 'rel="prev"' not in link
    assert 'rel="first"' not in link

def test_link_header_middle_page():
    link = _build_link_header("http://example.com/history", page=2, per_page=10, total=50)
    assert 'rel="first"' in link
    assert 'rel="prev"' in link
    assert 'rel="next"' in link
    assert 'rel="last"' in link
    assert "page=1" in link  # prev
    assert "page=3" in link  # next

def test_link_header_last_page():
    link = _build_link_header("http://example.com/history", page=4, per_page=10, total=50)
    assert 'rel="first"' in link
    assert 'rel="prev"' in link
    assert 'rel="next"' not in link
    assert 'rel="last"' not in link

def test_link_header_single_page():
    link = _build_link_header("http://example.com/history", page=0, per_page=100, total=50)
    assert link == ""

def test_link_header_extra_params():
    link = _build_link_header("http://example.com/history", page=0, per_page=10, total=50,
                              extra_params={"start": "2026-01-01"})
    assert "start=2026-01-01" in link


# --- /usage tests ---

def _make_active_job(job_id=111, entity_id=1000, entity_name="test-org",
                     job_labels=None, k8s_pool="scw-em-rv1", status="pending",
                     repo_full_name="test-org/repo", html_url="https://example.com",
                     created_at="2026-04-01T00:00:00+00:00"):
    return {
        "job_id": job_id, "entity_id": entity_id, "entity_name": entity_name,
        "job_labels": job_labels or ["ubuntu-24.04-riscv"], "k8s_pool": k8s_pool,
        "status": status, "repo_full_name": repo_full_name,
        "html_url": html_url, "created_at": created_at,
    }


def _make_active_worker(pod_name="pod-1", entity_id=1000, entity_name="test-org",
                         job_labels=None, k8s_pool="scw-em-rv1", k8s_node=None,
                         status="running", created_at="2026-04-01T00:00:00+00:00"):
    return {
        "pod_name": pod_name, "entity_id": entity_id, "entity_name": entity_name,
        "job_labels": job_labels or ["ubuntu-24.04-riscv"], "k8s_pool": k8s_pool,
        "k8s_node": k8s_node, "status": status, "created_at": created_at,
    }


@patch("scheduler.db")
def test_usage_json_empty(mock_db):
    mock_db.get_active_jobs_and_workers.return_value = ([], [])

    with app.test_client() as client:
        resp = client.get("/usage.json")
        assert resp.status_code == 200
        assert resp.content_type == "application/json"
        data = resp.get_json()
        assert data["jobs"] == []
        assert data["workers"] == []


@patch("scheduler.db")
def test_usage_json_jobs_only(mock_db):
    jobs = [_make_active_job(job_id=111), _make_active_job(job_id=222, status="running")]
    mock_db.get_active_jobs_and_workers.return_value = (jobs, [])

    with app.test_client() as client:
        resp = client.get("/usage.json")
        data = resp.get_json()
        assert len(data["jobs"]) == 2
        assert data["workers"] == []
        assert data["jobs"][0]["job_id"] == 111
        assert data["jobs"][1]["job_id"] == 222
        assert data["jobs"][0]["status"] == "pending"
        assert data["jobs"][1]["status"] == "running"


@patch("scheduler.db")
def test_usage_json_workers_only(mock_db):
    workers = [
        _make_active_worker(pod_name="pod-1", k8s_node="node-1"),
        _make_active_worker(pod_name="pod-2", status="pending", k8s_node=None),
    ]
    mock_db.get_active_jobs_and_workers.return_value = ([], workers)

    with app.test_client() as client:
        resp = client.get("/usage.json")
        data = resp.get_json()
        assert data["jobs"] == []
        assert len(data["workers"]) == 2
        assert data["workers"][0]["pod_name"] == "pod-1"
        assert data["workers"][0]["k8s_node"] == "node-1"
        assert data["workers"][1]["pod_name"] == "pod-2"
        assert data["workers"][1]["k8s_node"] is None
        assert data["workers"][0]["status"] == "running"
        assert data["workers"][1]["status"] == "pending"


@patch("scheduler.db")
def test_usage_json_jobs_and_workers(mock_db):
    jobs = [_make_active_job(job_id=111, entity_name="org-a", k8s_pool="pool-1")]
    workers = [_make_active_worker(pod_name="pod-1", entity_name="org-a", k8s_pool="pool-1")]
    mock_db.get_active_jobs_and_workers.return_value = (jobs, workers)

    with app.test_client() as client:
        resp = client.get("/usage.json")
        data = resp.get_json()
        assert len(data["jobs"]) == 1
        assert len(data["workers"]) == 1
        assert data["jobs"][0]["entity_name"] == "org-a"
        assert data["jobs"][0]["job_labels"] == ["ubuntu-24.04-riscv"]
        assert data["workers"][0]["entity_name"] == "org-a"
        assert data["workers"][0]["k8s_pool"] == "pool-1"


@patch("scheduler.db")
def test_usage_json_preserves_all_fields(mock_db):
    """Verify JSON output contains all fields from the DB row."""
    job = _make_active_job(job_id=999, entity_id=42, entity_name="myorg",
                           job_labels=["label-a", "label-b"], k8s_pool="my-pool",
                           status="running", repo_full_name="myorg/myrepo",
                           html_url="https://github.com/myorg/myrepo/actions/runs/1/job/999")
    mock_db.get_active_jobs_and_workers.return_value = ([job], [])

    with app.test_client() as client:
        data = client.get("/usage.json").get_json()
        out = data["jobs"][0]
        assert out["job_id"] == 999
        assert out["entity_id"] == 42
        assert out["entity_name"] == "myorg"
        assert out["job_labels"] == ["label-a", "label-b"]
        assert out["k8s_pool"] == "my-pool"
        assert out["status"] == "running"
        assert out["repo_full_name"] == "myorg/myrepo"
        assert out["html_url"] == "https://github.com/myorg/myrepo/actions/runs/1/job/999"
        assert out["created_at"] == "2026-04-01T00:00:00+00:00"


@patch("scheduler.db")
def test_usage_html(mock_db):
    mock_db.get_active_jobs_and_workers.return_value = ([], [])

    with app.test_client() as client:
        resp = client.get("/usage")
        assert resp.status_code == 200
        assert "text/html" in resp.content_type


# --- /history JSON + paging tests ---

@patch("scheduler.db")
def test_history_json(mock_db):
    mock_db.get_all_jobs.return_value = ([{"job_id": "1", "status": "completed"}], 1)

    with app.test_client() as client:
        resp = client.get("/history.json")
        assert resp.status_code == 200
        assert resp.content_type == "application/json"
        data = resp.get_json()
        assert len(data) == 1


@patch("scheduler.db")
def test_history_json_with_paging(mock_db):
    mock_db.get_all_jobs.return_value = ([{"job_id": "1"}], 250)

    with app.test_client() as client:
        resp = client.get("/history.json?page=1&per_page=100")
        assert resp.status_code == 200
        assert "link" in resp.headers
        link = resp.headers["link"]
        assert 'rel="first"' in link
        assert 'rel="prev"' in link
        assert 'rel="next"' in link
        assert 'rel="last"' in link


@patch("scheduler.db")
def test_history_passes_params_to_db(mock_db):
    mock_db.get_all_jobs.return_value = ([], 0)

    with app.test_client() as client:
        client.get("/history.json?start=2026-01-01&end=2026-02-01&page=2&per_page=50")

    mock_db.get_all_jobs.assert_called_once_with(
        start="2026-01-01", end="2026-02-01", page=2, per_page=50)


@patch("scheduler.db")
def test_history_relative_dates(mock_db):
    import datetime
    mock_db.get_all_jobs.return_value = ([], 0)

    with app.test_client() as client:
        client.get("/history.json?start=-7d")

    call_args = mock_db.get_all_jobs.call_args
    expected_start = (datetime.date.today() - datetime.timedelta(days=7)).isoformat()
    assert call_args.kwargs["start"] == expected_start


@patch("scheduler.db")
def test_history_no_link_header_single_page(mock_db):
    mock_db.get_all_jobs.return_value = ([{"job_id": "1"}], 1)

    with app.test_client() as client:
        resp = client.get("/history.json")
        assert "link" not in resp.headers


@patch("scheduler.db")
def test_history_html_default(mock_db):
    mock_db.get_all_jobs.return_value = ([], 0)

    with app.test_client() as client:
        resp = client.get("/history")
        assert resp.status_code == 200
        assert "text/html" in resp.content_type
