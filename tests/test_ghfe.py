import json
import pytest
from unittest.mock import patch, MagicMock

from constants import EntityType
from ghfe import (
    WebhookError,
    check_webhook_signature,
    check_webhook_event,
    authorize_entity,
    compute_signature,
    match_labels_to_k8s,
    GHAPP_WEBHOOK_SECRET,
)


# --- Signature verification ---

def test_valid_signature():
    from ghfe import app
    body = '{"action":"queued"}'
    expected_signature = compute_signature(body, GHAPP_WEBHOOK_SECRET).hexdigest()
    headers = {"X-Hub-Signature-256": f"sha256={expected_signature}", "X-Github-Event": "workflow_job"}

    with app.test_request_context(headers=headers):
        event, result_body = check_webhook_signature(headers, body)
        assert event == "workflow_job"
        assert result_body == body


def test_invalid_signature():
    from ghfe import app
    headers = {"X-Hub-Signature-256": "sha256=invalid", "X-Github-Event": "workflow_job"}
    with app.test_request_context(headers=headers):
        with pytest.raises(WebhookError) as exc:
            check_webhook_signature(headers, "")
        assert exc.value.status_code == 401


def test_missing_signature():
    from ghfe import app
    headers = {"X-Github-Event": "workflow_job"}
    with app.test_request_context(headers=headers):
        with pytest.raises(WebhookError) as exc:
            check_webhook_signature(headers, "")
        assert exc.value.status_code == 400


# --- Event parsing ---

def test_queued_event():
    body = json.dumps({"action": "queued", "workflow_job": {"id": 1, "name": "test", "labels": ["ubuntu-24.04-riscv"]}, "repository": {"full_name": "org/repo"}})
    payload, action = check_webhook_event(body)
    assert action == "queued"


def test_completed_event():
    body = json.dumps({"action": "completed", "workflow_job": {"id": 123, "name": "test", "labels": ["ubuntu-24.04-riscv"]}, "repository": {"full_name": "org/repo"}})
    payload, action = check_webhook_event(body)
    assert action == "completed"


def test_in_progress_event():
    body = json.dumps({"action": "in_progress", "workflow_job": {"id": 123, "name": "test", "labels": ["ubuntu-24.04-riscv"]}, "repository": {"full_name": "org/repo"}})
    payload, action = check_webhook_event(body)
    assert action == "in_progress"


def test_ignored_event():
    body = '{"action":"waiting"}'
    with pytest.raises(WebhookError) as exc:
        check_webhook_event(body)
    assert exc.value.status_code == 200
    assert "Ignoring action" in exc.value.message


def test_invalid_json():
    with pytest.raises(WebhookError) as exc:
        check_webhook_event("{")
    assert exc.value.status_code == 400


# --- Owner authorization ---

def test_authorized_org():
    org_id = 152654596
    payload = {"repository": {"owner": {"id": org_id, "login": "riseproject-dev", "type": "Organization"}}}
    entity_id, entity_type = authorize_entity(payload)
    assert entity_id == org_id
    assert entity_type == EntityType.ORGANIZATION


def test_any_org_accepted():
    """Any org that installs the app is accepted."""
    payload = {"repository": {"owner": {"id": 1, "login": "unknown-org", "type": "Organization"}}}
    entity_id, entity_type = authorize_entity(payload)
    assert entity_id == 1
    assert entity_type == EntityType.ORGANIZATION


def test_personal_account_accepted():
    payload = {"repository": {"owner": {"id": 99999, "login": "some-user", "type": "User"}}}
    entity_id, entity_type = authorize_entity(payload)
    assert entity_id == 99999
    assert entity_type == EntityType.USER


# --- Label matching ---

def test_match_labels_riscv():
    k8s_pool, k8s_image = match_labels_to_k8s(0, "", ["ubuntu-24.04-riscv"])
    assert k8s_pool == "scw-em-rv1"
    assert k8s_image == "riscv-runner:ubuntu-24.04-latest"


def test_match_labels_unsupported():
    with pytest.raises(WebhookError) as exc:
        match_labels_to_k8s(0, "", ["unsupported-label"])
    assert "missing required platform label" in exc.value.message


def test_match_labels_missing_platform():
    with pytest.raises(WebhookError) as exc:
        match_labels_to_k8s(0, "", ["random-label"])
    assert "missing required platform label" in exc.value.message


# --- Webhook integration ---

@patch("db.add_job", return_value=True)
def test_webhook_queued_stores_job(mock_store):
    """Test that a queued webhook stores the job."""
    from ghfe import app

    payload = {
        "action": "queued",
        "workflow_job": {"id": 12345, "name": "test", "labels": ["ubuntu-24.04-riscv"], "html_url": "https://github.com/riseproject-dev/sample/actions/runs/1/job/12345"},
        "repository": {"id": 100, "full_name": "riseproject-dev/sample", "owner": {"id": 152654596, "login": "riseproject-dev", "type": "Organization"}},
        "installation": {"id": 999},
    }
    body = json.dumps(payload)
    sig = "sha256=" + compute_signature(body, GHAPP_WEBHOOK_SECRET).hexdigest()

    with app.test_client() as client:
        resp = client.post("/", data=body, headers={
            "X-Hub-Signature-256": sig,
            "X-Github-Event": "workflow_job",
            "Content-Type": "application/json",
        })
        assert resp.status_code == 200
        assert b"stored" in resp.data
        mock_store.assert_called_once_with(
            job_id=12345,
            provider="github",
            entity_id=152654596,
            entity_name="riseproject-dev",
            entity_type=EntityType.ORGANIZATION,
            repo_full_name="riseproject-dev/sample",
            installation_id=999,
            labels=["ubuntu-24.04-riscv"],
            k8s_pool="scw-em-rv1",
            k8s_image="riscv-runner:ubuntu-24.04-latest",
            html_url="https://github.com/riseproject-dev/sample/actions/runs/1/job/12345",
        )


@patch("db.add_job", return_value=True)
def test_webhook_queued_personal_account(mock_store):
    """Test that a queued webhook from a personal account uses repo_id as entity_id."""
    from ghfe import app

    payload = {
        "action": "queued",
        "workflow_job": {"id": 55555, "name": "test", "labels": ["ubuntu-24.04-riscv"], "html_url": "https://github.com/someuser/myrepo/actions/runs/1/job/55555"},
        "repository": {"id": 200, "full_name": "someuser/myrepo", "owner": {"id": 99999, "login": "someuser", "type": "User"}},
        "installation": {"id": 888},
    }
    body = json.dumps(payload)
    sig = "sha256=" + compute_signature(body, GHAPP_WEBHOOK_SECRET).hexdigest()

    with app.test_client() as client:
        resp = client.post("/", data=body, headers={
            "X-Hub-Signature-256": sig,
            "X-Github-Event": "workflow_job",
            "Content-Type": "application/json",
        })
        assert resp.status_code == 200
        assert b"stored" in resp.data
        mock_store.assert_called_once_with(
            job_id=55555,
            provider="github",
            entity_id=200,  # repo_id for personal accounts
            entity_name="someuser",
            entity_type=EntityType.USER,
            repo_full_name="someuser/myrepo",
            installation_id=888,
            labels=["ubuntu-24.04-riscv"],
            k8s_pool="scw-em-rv1",
            k8s_image="riscv-runner:ubuntu-24.04-latest",
            html_url="https://github.com/someuser/myrepo/actions/runs/1/job/55555",
        )


@patch("db.mark_job_running", return_value="pending")
def test_webhook_in_progress(mock_update):
    """Test that an in_progress webhook updates job status."""
    from ghfe import app

    payload = {
        "action": "in_progress",
        "workflow_job": {"id": 12345, "name": "test", "labels": ["ubuntu-24.04-riscv"]},
        "repository": {"id": 100, "full_name": "riseproject-dev/sample", "owner": {"id": 152654596, "login": "riseproject-dev", "type": "Organization"}},
    }
    body = json.dumps(payload)
    sig = "sha256=" + compute_signature(body, GHAPP_WEBHOOK_SECRET).hexdigest()

    with app.test_client() as client:
        resp = client.post("/", data=body, headers={
            "X-Hub-Signature-256": sig,
            "X-Github-Event": "workflow_job",
            "Content-Type": "application/json",
        })
        assert resp.status_code == 200
        assert b"running" in resp.data
        mock_update.assert_called_once_with(12345)


@patch("db.mark_job_completed", return_value="running")
def test_webhook_completed(mock_complete):
    """Test that a completed webhook marks the job as completed."""
    from ghfe import app

    payload = {
        "action": "completed",
        "workflow_job": {"id": 12345, "name": "test", "labels": ["ubuntu-24.04-riscv"]},
        "repository": {"id": 100, "full_name": "riseproject-dev/sample", "owner": {"id": 152654596, "login": "riseproject-dev", "type": "Organization"}},
    }
    body = json.dumps(payload)
    sig = "sha256=" + compute_signature(body, GHAPP_WEBHOOK_SECRET).hexdigest()

    with app.test_client() as client:
        resp = client.post("/", data=body, headers={
            "X-Hub-Signature-256": sig,
            "X-Github-Event": "workflow_job",
            "Content-Type": "application/json",
        })
        assert resp.status_code == 200
        assert b"completed" in resp.data
        mock_complete.assert_called_once_with(12345)


# --- Setup redirect page ---

def test_setup_missing_installation_id():
    from ghfe import app
    with app.test_client() as client:
        resp = client.get("/setup/org")
        assert resp.status_code == 400
        assert b"Missing installation id" in resp.data


@patch("ghfe.gh.get_installation")
def test_setup_org_success(mock_get_installation):
    from ghfe import app
    mock_get_installation.return_value = {"account": {"type": "Organization", "login": "riseproject-dev"}}
    with app.test_client() as client:
        resp = client.get("/setup/org?installation_id=42")
        assert resp.status_code == 200
        assert b"All set" in resp.data
        assert b"riseproject-dev" in resp.data
    mock_get_installation.assert_called_once()
    args, kwargs = mock_get_installation.call_args
    assert args[0] == "42"
    assert kwargs["entity_type"] == EntityType.ORGANIZATION


@patch("ghfe.gh.get_installation")
def test_setup_org_on_personal_account_mismatch(mock_get_installation):
    from ghfe import app
    mock_get_installation.return_value = {"account": {"type": "User", "login": "alice"}}
    with app.test_client() as client:
        resp = client.get("/setup/org?installation_id=42")
        assert resp.status_code == 400
        assert b"organization app on a personal account" in resp.data
        assert b"rise-risc-v-runners-personal" in resp.data


@patch("ghfe.gh.get_installation")
def test_setup_personal_success(mock_get_installation):
    from ghfe import app
    mock_get_installation.return_value = {"account": {"type": "User", "login": "alice"}}
    with app.test_client() as client:
        resp = client.get("/setup/personal?installation_id=99")
        assert resp.status_code == 200
        assert b"All set" in resp.data
        assert b"alice" in resp.data
    args, kwargs = mock_get_installation.call_args
    assert kwargs["entity_type"] == EntityType.USER


@patch("ghfe.gh.get_installation")
def test_setup_personal_on_org_mismatch(mock_get_installation):
    from ghfe import app
    mock_get_installation.return_value = {"account": {"type": "Organization", "login": "acme"}}
    with app.test_client() as client:
        resp = client.get("/setup/personal?installation_id=99")
        assert resp.status_code == 400
        assert b"personal app on an organization" in resp.data
        assert b"apps/rise-risc-v-runners/installations/new" in resp.data


@patch("ghfe.gh.get_installation")
def test_setup_installation_not_found(mock_get_installation):
    from ghfe import app
    from github import GitHubAPIError
    mock_get_installation.side_effect = GitHubAPIError(404, "Not Found")
    with app.test_client() as client:
        resp = client.get("/setup/org?installation_id=123")
        assert resp.status_code == 404
        assert b"Installation not found" in resp.data
        assert b"rise-risc-v-runners-personal" in resp.data


@patch("ghfe.gh.get_installation")
def test_setup_upstream_error(mock_get_installation):
    from ghfe import app
    from github import GitHubAPIError
    mock_get_installation.side_effect = GitHubAPIError(500, "boom")
    with app.test_client() as client:
        resp = client.get("/setup/org?installation_id=123")
        assert resp.status_code == 502
        assert b"Something went wrong" in resp.data
