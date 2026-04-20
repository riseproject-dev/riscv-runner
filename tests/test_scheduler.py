import json
from unittest.mock import patch, MagicMock

from constants import EntityType
from scheduler import (
    app,
    demand_match,
    cleanup_pods,
    gh_reconcile_jobs,
    _parse_date_param,
    _build_link_header,
)


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
             entity_type=EntityType.ORGANIZATION):
    """Helper to create a mock job dict."""
    return {
        "status": status,
        "job_id": str(job_id),
        "entity_id": str(entity_id),
        "entity_name": entity_name,
        "entity_type": entity_type.value,
        "repo_full_name": repo_full_name,
        "installation_id": str(installation_id),
        "job_labels": json.dumps(["ubuntu-24.04-riscv"]),
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
    mock_db.get_pending_jobs.return_value = ["111"]
    mock_db.get_job.return_value = job
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
    mock_db.get_pending_jobs.return_value = ["222"]
    mock_db.get_job.return_value = job
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
    mock_db.get_pending_jobs.return_value = ["111"]
    mock_db.get_job.return_value = job
    mock_db.get_pool_demand.return_value = (1, 1)  # demand met

    demand_match()

    mock_provision.assert_not_called()


@patch("scheduler.db")
@patch("scheduler.k8s.has_available_slot", return_value=False)
@patch("scheduler.k8s.provision_runner")
def test_demand_match_skips_no_k8s_capacity(mock_provision, mock_slot, mock_db):
    """Test that jobs are skipped when no k8s capacity."""
    job = make_job(111)
    mock_db.get_pending_jobs.return_value = ["111"]
    mock_db.get_job.return_value = job
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
    mock_db.get_pending_jobs.return_value = ["111"]
    mock_db.get_job.return_value = job
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
    mock_db.get_pending_jobs.return_value = ["111"]
    mock_db.get_job.return_value = job
    mock_db.get_pool_demand.return_value = (1, 0)
    mock_db.get_total_workers_for_entity.return_value = 0

    demand_match()  # should not raise

    # add_worker is called before provision_runner to reserve the name
    mock_db.add_worker.assert_called_once()


# --- cleanup_pods tests ---

@patch("scheduler.db")
@patch("scheduler.k8s.list_pods")
@patch("scheduler.k8s.delete_pod")
def test_cleanup_deletes_succeeded_pod_past_grace(mock_delete, mock_list, mock_db):
    """Succeeded pods past the 6h grace period are deleted from k8s.

    The worker row transition is driven by sync_worker_status (not a separate
    remove_worker call after delete), so we only assert the k8s delete happened.
    """
    import datetime
    from constants import POD_DELETE_GRACE_SECONDS

    old = datetime.datetime.now(datetime.timezone.utc) - datetime.timedelta(seconds=POD_DELETE_GRACE_SECONDS + 60)
    pod = make_pod("pod-1", phase="Succeeded", entity_id="1000", board="scw-em-rv1",
                   creation_timestamp=old)
    mock_list.return_value = [pod]
    mock_db.iter_workers.return_value = []

    cleanup_pods()

    mock_delete.assert_called_once_with(pod)


@patch("scheduler.db")
@patch("scheduler.k8s.list_pods")
@patch("scheduler.k8s.delete_pod")
def test_cleanup_skips_succeeded_pod_within_grace(mock_delete, mock_list, mock_db):
    """Recently-terminated pods are kept around for the grace window."""
    pod = make_pod("pod-1", phase="Succeeded", entity_id="1000", board="scw-em-rv1")
    mock_list.return_value = [pod]
    mock_db.iter_workers.return_value = []

    cleanup_pods()

    mock_delete.assert_not_called()


@patch("scheduler.db")
@patch("scheduler.k8s.list_pods")
@patch("scheduler.k8s.delete_pod")
def test_cleanup_skips_running_pod(mock_delete, mock_list, mock_db):
    """Running pods are never deleted."""
    pod = make_pod("pod-1", phase="Running", entity_id="1000", board="scw-em-rv1")
    mock_list.return_value = [pod]
    mock_db.iter_workers.return_value = []

    cleanup_pods()

    mock_delete.assert_not_called()


# --- gh_reconcile_jobs tests ---

@patch("scheduler.db")
@patch("scheduler.gh.authenticate_app", return_value="token-123")
@patch("scheduler.gh.get_job_status", return_value="completed")
def test_gh_reconcile_jobs_completes_job(mock_status, mock_auth, mock_db):
    """Reconciliation marks a job completed when GH says so."""
    job = make_job(111, status="running")
    mock_db.get_active_jobs.return_value = [job]

    gh_reconcile_jobs()

    mock_db.update_job_completed.assert_called_once_with("111")


@patch("scheduler.db")
@patch("scheduler.gh.authenticate_app", return_value="token-123")
@patch("scheduler.gh.get_job_status", return_value="in_progress")
def test_gh_reconcile_jobs_updates_running(mock_status, mock_auth, mock_db):
    """Reconciliation updates pending→running when GH says in_progress."""
    job = make_job(111, status="pending")
    mock_db.get_active_jobs.return_value = [job]

    gh_reconcile_jobs()

    mock_db.update_job_running.assert_called_once_with("111")


@patch("scheduler.db")
@patch("scheduler.gh.authenticate_app", return_value="token-123")
@patch("scheduler.gh.get_job_status")
def test_gh_reconcile_jobs_marks_job_failed_on_404(mock_status, mock_auth, mock_db):
    """A 404 from get_job_status marks the job as failed."""
    from github import GitHubAPIError

    job = make_job(111, status="running")
    mock_db.get_active_jobs.return_value = [job]
    mock_status.side_effect = GitHubAPIError(404, "Not Found")

    gh_reconcile_jobs()

    mock_db.update_job_failed.assert_called_once()
    call_args = mock_db.update_job_failed.call_args[0]
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

    gh_reconcile_jobs()

    assert mock_db.update_job_failed.call_count == 2
    job_ids = [call_args[0][0] for call_args in mock_db.update_job_failed.call_args_list]
    assert set(job_ids) == {"111", "222"}
    for call_args in mock_db.update_job_failed.call_args_list:
        assert "version" in call_args[0][1] and isinstance(call_args[0][1]["version"], int) and call_args[0][1]["version"] >= 1
        assert "installation not found" in call_args[0][1]["message"]


@patch("scheduler.db")
def test_gh_reconcile_jobs_no_active_jobs(mock_db):
    """No-op when no active jobs."""
    mock_db.get_active_jobs.return_value = []

    gh_reconcile_jobs()

    mock_db.update_job_completed.assert_not_called()
    mock_db.update_job_running.assert_not_called()


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
