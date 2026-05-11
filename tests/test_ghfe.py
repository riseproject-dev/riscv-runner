import json
import pytest
from unittest.mock import patch, MagicMock

from constants import EntityType
from ghfe import (
    WebhookError,
    _trim_workflow_job_payload,
    check_webhook_signature,
    authorize_entity,
    compute_signature,
    match_labels_to_k8s,
    GHAPP_WEBHOOK_SECRET,
    TRACE_API_SECRET,
)


@pytest.fixture(autouse=True)
def _mock_add_installation_event():
    """Stub the event log writer so webhook tests don't try to hit Postgres.

    Returns a fresh MagicMock per test; tests that care about the call args
    can grab it via the request fixture if needed.
    """
    with patch("ghfe.db.add_installation_event", return_value=1) as m:
        yield m


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
    assert match_labels_to_k8s(0, "", ["unsupported-label"]) is None


def test_match_labels_missing_platform():
    assert match_labels_to_k8s(0, "", ["random-label"]) is None


def test_match_labels_pytorch_org_no_match():
    """PyTorch org with non-PyTorch labels → no match (covers the org-specific
    no-match branch separately from the general-case fall-through)."""
    from constants import PYTORCH_ORG_ID
    assert match_labels_to_k8s(PYTORCH_ORG_ID, "", ["ubuntu-latest"]) is None


# --- _trim_workflow_job_payload ---

def test_trim_drops_sender_url_fields():
    payload = {"sender": {
        "id": 1, "login": "ggerganov", "type": "User", "node_id": "MDQ6...",
        "url": "drop", "html_url": "drop",
        "gists_url": "drop", "repos_url": "drop", "avatar_url": "drop",
        "events_url": "drop", "starred_url": "drop", "followers_url": "drop",
        "following_url": "drop", "organizations_url": "drop",
        "subscriptions_url": "drop", "received_events_url": "drop",
        "site_admin": False, "gravatar_id": "", "user_view_type": "public",
    }}
    assert _trim_workflow_job_payload(payload) == {"sender": {
        "id": 1, "login": "ggerganov", "type": "User", "node_id": "MDQ6...",
        "site_admin": False, "gravatar_id": "", "user_view_type": "public",
    }}


def test_trim_drops_repository_url_fields_and_license():
    payload = {"repository": {
        "id": 100, "full_name": "o/r", "name": "r", "private": False,
        "default_branch": "main", "language": "C++",
        # all dropped:
        "url": "drop", "html_url": "drop", "license": {"key": "mit"},
        "git_url": "drop", "ssh_url": "drop", "svn_url": "drop",
        "keys_url": "drop", "tags_url": "drop", "blobs_url": "drop",
        "clone_url": "drop", "forks_url": "drop", "hooks_url": "drop",
        "pulls_url": "drop", "teams_url": "drop", "trees_url": "drop",
        "events_url": "drop", "issues_url": "drop", "labels_url": "drop",
        "merges_url": "drop", "mirror_url": None, "archive_url": "drop",
        "commits_url": "drop", "compare_url": "drop", "branches_url": "drop",
        "comments_url": "drop", "contents_url": "drop", "git_refs_url": "drop",
        "git_tags_url": "drop", "releases_url": "drop", "statuses_url": "drop",
        "assignees_url": "drop", "downloads_url": "drop", "languages_url": "drop",
        "milestones_url": "drop", "stargazers_url": "drop", "deployments_url": "drop",
        "git_commits_url": "drop", "subscribers_url": "drop",
        "contributors_url": "drop", "issue_events_url": "drop",
        "subscription_url": "drop", "collaborators_url": "drop",
        "issue_comment_url": "drop", "notifications_url": "drop",
    }}
    assert _trim_workflow_job_payload(payload) == {"repository": {
        "id": 100, "full_name": "o/r", "name": "r", "private": False,
        "default_branch": "main", "language": "C++",
    }}


def test_trim_drops_repository_owner_url_fields():
    payload = {"repository": {"full_name": "o/r", "owner": {
        "id": 1, "login": "ggml-org", "type": "Organization", "node_id": "X",
        # dropped:
        "url": "drop", "html_url": "drop",
        "gists_url": "drop", "repos_url": "drop", "avatar_url": "drop",
        "events_url": "drop", "starred_url": "drop", "followers_url": "drop",
        "following_url": "drop", "organizations_url": "drop",
        "subscriptions_url": "drop", "received_events_url": "drop",
    }}}
    assert _trim_workflow_job_payload(payload) == {"repository": {
        "full_name": "o/r",
        "owner": {"id": 1, "login": "ggml-org", "type": "Organization", "node_id": "X"},
    }}


def test_trim_drops_organization_url_fields():
    payload = {"organization": {
        "id": 134263123, "login": "ggml-org", "description": "AI at the edge",
        "node_id": "X",
        # dropped:
        "url": "drop", "hooks_url": "drop", "repos_url": "drop",
        "avatar_url": "drop", "events_url": "drop", "issues_url": "drop",
        "members_url": "drop", "public_members_url": "drop",
    }}
    assert _trim_workflow_job_payload(payload) == {"organization": {
        "id": 134263123, "login": "ggml-org", "description": "AI at the edge",
        "node_id": "X",
    }}


def test_trim_drops_workflow_job_url_fields_and_steps():
    """workflow_job: only html_url survives. url/run_url/check_run_url and the
    steps[] array are dropped."""
    payload = {"workflow_job": {
        "id": 1, "name": "test", "labels": ["ubuntu-24.04-riscv"],
        "html_url": "https://github.com/o/r/actions/runs/1/job/1",
        "head_sha": "abc", "conclusion": "success", "status": "completed",
        "runner_name": "rn", "run_id": 99, "runner_id": 42,
        # dropped:
        "url": "drop", "run_url": "drop", "check_run_url": "drop",
        "steps": [{"name": "Set up job", "number": 1, "status": "completed"}],
    }}
    assert _trim_workflow_job_payload(payload) == {"workflow_job": {
        "id": 1, "name": "test", "labels": ["ubuntu-24.04-riscv"],
        "html_url": "https://github.com/o/r/actions/runs/1/job/1",
        "head_sha": "abc", "conclusion": "success", "status": "completed",
        "runner_name": "rn", "run_id": 99, "runner_id": 42,
    }}


def test_trim_does_not_introduce_missing_sections():
    """A payload with only top-level fields is returned essentially unchanged."""
    payload = {"action": "queued", "installation": {"id": 999}}
    assert _trim_workflow_job_payload(payload) == payload


def test_trim_handles_missing_owner_in_repository():
    """If `repository` has no `owner`, the trimmer doesn't crash on it."""
    payload = {"repository": {"full_name": "o/r", "url": "drop"}}
    assert _trim_workflow_job_payload(payload) == {"repository": {"full_name": "o/r"}}


def test_match_labels_ggml_org_no_match():
    from constants import GGML_ORG_ORG_ID
    assert match_labels_to_k8s(GGML_ORG_ORG_ID, "", ["ubuntu-latest"]) is None


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
            "X-GitHub-Hook-Installation-Target-Id": "2167633",
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
            "X-GitHub-Hook-Installation-Target-Id": "2167633",
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
        "workflow_job": {"id": 12345, "name": "test", "labels": ["ubuntu-24.04-riscv"], "runner_name": "my-runner"},
        "repository": {"id": 100, "full_name": "riseproject-dev/sample", "owner": {"id": 152654596, "login": "riseproject-dev", "type": "Organization"}},
        "installation": {"id": 999},
    }
    body = json.dumps(payload)
    sig = "sha256=" + compute_signature(body, GHAPP_WEBHOOK_SECRET).hexdigest()

    with app.test_client() as client:
        resp = client.post("/", data=body, headers={
            "X-Hub-Signature-256": sig,
            "X-Github-Event": "workflow_job",
            "X-GitHub-Hook-Installation-Target-Id": "2167633",
            "Content-Type": "application/json",
        })
        assert resp.status_code == 200
        assert b"running" in resp.data
        mock_update.assert_called_once_with(12345, "my-runner")


@patch("db.mark_job_completed", return_value="running")
def test_webhook_completed(mock_complete):
    """Test that a completed webhook marks the job as completed."""
    from ghfe import app

    payload = {
        "action": "completed",
        "workflow_job": {"id": 12345, "name": "test", "labels": ["ubuntu-24.04-riscv"], "runner_name": "my-runner"},
        "repository": {"id": 100, "full_name": "riseproject-dev/sample", "owner": {"id": 152654596, "login": "riseproject-dev", "type": "Organization"}},
        "installation": {"id": 999},
    }
    body = json.dumps(payload)
    sig = "sha256=" + compute_signature(body, GHAPP_WEBHOOK_SECRET).hexdigest()

    with app.test_client() as client:
        resp = client.post("/", data=body, headers={
            "X-Hub-Signature-256": sig,
            "X-Github-Event": "workflow_job",
            "X-GitHub-Hook-Installation-Target-Id": "2167633",
            "Content-Type": "application/json",
        })
        assert resp.status_code == 200
        assert b"completed" in resp.data
        mock_complete.assert_called_once_with(12345, "my-runner")


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


# --- Installation event logging on the webhook handler ---

DELIVERY_HEADERS = {
    "X-GitHub-Hook-Installation-Target-Id": "2167633",
}


def _post_webhook(client, body, event):
    sig = "sha256=" + compute_signature(body, GHAPP_WEBHOOK_SECRET).hexdigest()
    headers = {
        "X-Hub-Signature-256": sig,
        "X-Github-Event": event,
        "Content-Type": "application/json",
        **DELIVERY_HEADERS,
    }
    return client.post("/", data=body, headers=headers)


def _last_log_call(mock):
    """Return the kwargs of the last call to db.add_installation_event."""
    assert mock.call_count >= 1, "expected db.add_installation_event to be called"
    return mock.call_args.kwargs


def test_webhook_invalid_json_returns_400(_mock_add_installation_event):
    from ghfe import app
    body = "{not-json"
    sig = "sha256=" + compute_signature(body, GHAPP_WEBHOOK_SECRET).hexdigest()
    with app.test_client() as client:
        resp = client.post("/", data=body, headers={
            "X-Hub-Signature-256": sig,
            "X-Github-Event": "installation",
            "Content-Type": "application/json",
        })
        assert resp.status_code == 400


def test_webhook_ping_logs_ok(_mock_add_installation_event):
    from ghfe import app
    body = json.dumps({"zen": "Approachable is better than simple."})
    with app.test_client() as client:
        resp = _post_webhook(client, body, "ping")
        assert resp.status_code == 200
        assert resp.data == b"pong"
    kwargs = _last_log_call(_mock_add_installation_event)
    assert kwargs["event"] == "ping"
    assert kwargs["outcome"] == "ok"
    assert kwargs["source"] == "webhook"


def test_webhook_installation_created_logs(_mock_add_installation_event):
    from ghfe import app
    payload = {
        "action": "created",
        "installation": {
            "id": 999,
            "app_id": 2167633,
            "target_id": 152654596,
            "target_type": "Organization",
            "account": {"id": 152654596, "login": "riseproject-dev", "type": "Organization"},
        },
        "repositories": [{"id": 1, "full_name": "riseproject-dev/sample"}],
    }
    body = json.dumps(payload)
    with app.test_client() as client:
        resp = _post_webhook(client, body, "installation")
        assert resp.status_code == 200

    kwargs = _last_log_call(_mock_add_installation_event)
    assert kwargs["event"] == "installation.created"
    assert kwargs["outcome"] == "ok"
    assert kwargs["installation_id"] == 999
    assert kwargs["app_id"] == 2167633
    assert kwargs["entity_id"] == 152654596
    assert kwargs["entity_name"] == "riseproject-dev"


def test_webhook_installation_repositories_added_logs(_mock_add_installation_event):
    from ghfe import app
    payload = {
        "action": "added",
        "installation": {"id": 999, "app_id": 2167633,
                         "target_id": 152654596, "target_type": "Organization",
                         "account": {"id": 152654596, "login": "riseproject-dev",
                                     "type": "Organization"}},
        "repositories_added": [{"id": 2, "full_name": "riseproject-dev/new-repo"}],
        "repositories_removed": [],
    }
    body = json.dumps(payload)
    with app.test_client() as client:
        resp = _post_webhook(client, body, "installation_repositories")
        assert resp.status_code == 200
    kwargs = _last_log_call(_mock_add_installation_event)
    assert kwargs["event"] == "installation_repositories.added"


def test_webhook_installation_target_renamed_logs(_mock_add_installation_event):
    """installation_target.renamed payload carries the new account at top
    level; we record the new login (not the cached one in installation.account)."""
    from ghfe import app
    payload = {
        "action": "renamed",
        # Top-level account = new state. installation.account would be stale.
        "account": {"id": 152654596, "login": "renamed-org", "type": "Organization"},
        "changes": {"login": {"from": "riseproject-dev"}},
        "target_type": "Organization",
        "installation": {"id": 999, "app_id": 2167633, "target_id": 152654596,
                         "target_type": "Organization"},
    }
    body = json.dumps(payload)
    with app.test_client() as client:
        resp = _post_webhook(client, body, "installation_target")
        assert resp.status_code == 200
    kwargs = _last_log_call(_mock_add_installation_event)
    assert kwargs["event"] == "installation_target.renamed"
    assert kwargs["entity_name"] == "renamed-org"
    assert kwargs["entity_id"] == 152654596


def test_webhook_unhandled_event_logs_ignored_event(_mock_add_installation_event):
    from ghfe import app
    body = json.dumps({"ref": "refs/heads/main"})
    with app.test_client() as client:
        resp = _post_webhook(client, body, "push")
        assert resp.status_code == 200
        assert b"Ignoring push event" in resp.data
    kwargs = _last_log_call(_mock_add_installation_event)
    assert kwargs["event"] == "push"
    assert kwargs["outcome"] == "ignored_event"


@patch("db.add_job", return_value=True)
def test_webhook_workflow_job_queued_logs_job_stored(mock_add_job, _mock_add_installation_event):
    """The logged payload for a processed workflow_job has the URL/license/steps
    noise dropped per _WORKFLOW_JOB_DROP_KEYS. Only workflow_job.html_url
    survives — the operational link to the run on GitHub.com."""
    from ghfe import app
    payload = {
        "action": "queued",
        "workflow_job": {
            "id": 12345, "name": "test", "labels": ["ubuntu-24.04-riscv"],
            "html_url": "https://github.com/o/r/actions/runs/1/job/12345",
            "url": "drop", "run_url": "drop", "check_run_url": "drop",
            "steps": [{"name": "Set up job"}],
        },
        "repository": {
            "id": 100, "full_name": "riseproject-dev/sample",
            "url": "drop", "html_url": "drop", "events_url": "drop",
            "owner": {"id": 152654596, "login": "riseproject-dev", "type": "Organization",
                      "url": "drop", "html_url": "drop",
                      "avatar_url": "drop", "repos_url": "drop"},
        },
        "installation": {"id": 999},
        "sender": {"id": 7, "login": "noisy", "type": "User",
                   "url": "drop", "html_url": "drop", "avatar_url": "drop"},
    }
    body = json.dumps(payload)
    with app.test_client() as client:
        resp = _post_webhook(client, body, "workflow_job")
        assert resp.status_code == 200
    kwargs = _last_log_call(_mock_add_installation_event)
    assert kwargs["event"] == "workflow_job.queued"
    assert kwargs["outcome"] == "job_stored"

    logged = kwargs["payload"]
    # workflow_job.html_url survives — it's our operational link.
    assert logged["workflow_job"]["html_url"] == "https://github.com/o/r/actions/runs/1/job/12345"
    assert "url" not in logged["workflow_job"]
    assert "run_url" not in logged["workflow_job"]
    assert "check_run_url" not in logged["workflow_job"]
    assert "steps" not in logged["workflow_job"]
    # No URLs survive on repository / owner / sender.
    assert "url" not in logged["repository"] and "html_url" not in logged["repository"]
    assert "events_url" not in logged["repository"]
    assert "url" not in logged["repository"]["owner"] and "html_url" not in logged["repository"]["owner"]
    assert "avatar_url" not in logged["repository"]["owner"]
    assert "url" not in logged["sender"] and "html_url" not in logged["sender"]
    assert "avatar_url" not in logged["sender"]
    # Non-noise fields stay.
    assert logged["workflow_job"]["labels"] == ["ubuntu-24.04-riscv"]
    assert logged["repository"]["full_name"] == "riseproject-dev/sample"
    assert logged["repository"]["owner"]["login"] == "riseproject-dev"
    assert logged["sender"]["login"] == "noisy"


@patch("db.add_job", return_value=False)
def test_webhook_workflow_job_queued_already_exists(mock_add_job, _mock_add_installation_event):
    from ghfe import app
    payload = {
        "action": "queued",
        "workflow_job": {"id": 12345, "name": "test", "labels": ["ubuntu-24.04-riscv"],
                         "html_url": "https://github.com/o/r/actions/runs/1/job/12345"},
        "repository": {"id": 100, "full_name": "riseproject-dev/sample",
                       "owner": {"id": 152654596, "login": "riseproject-dev",
                                 "type": "Organization"}},
        "installation": {"id": 999},
    }
    body = json.dumps(payload)
    with app.test_client() as client:
        resp = _post_webhook(client, body, "workflow_job")
        assert resp.status_code == 200
    kwargs = _last_log_call(_mock_add_installation_event)
    assert kwargs["outcome"] == "job_already_exists"


@patch("db.mark_job_running", return_value="pending")
def test_webhook_workflow_job_in_progress_logs_marked_running(mock_mr, _mock_add_installation_event):
    from ghfe import app
    payload = {
        "action": "in_progress",
        "workflow_job": {"id": 12345, "name": "test", "labels": ["ubuntu-24.04-riscv"], "runner_name": "rn"},
        "repository": {"id": 100, "full_name": "o/r",
                       "owner": {"id": 152654596, "login": "o", "type": "Organization"}},
        "installation": {"id": 999},
    }
    body = json.dumps(payload)
    with app.test_client() as client:
        resp = _post_webhook(client, body, "workflow_job")
        assert resp.status_code == 200
    kwargs = _last_log_call(_mock_add_installation_event)
    assert kwargs["event"] == "workflow_job.in_progress"
    assert kwargs["outcome"] == "job_marked_running"


@patch("db.mark_job_running", return_value=None)
def test_webhook_workflow_job_in_progress_not_found(mock_mr, _mock_add_installation_event):
    from ghfe import app
    payload = {
        "action": "in_progress",
        "workflow_job": {"id": 12345, "name": "test", "labels": ["ubuntu-24.04-riscv"]},
        "repository": {"id": 100, "full_name": "o/r",
                       "owner": {"id": 152654596, "login": "o", "type": "Organization"}},
        "installation": {"id": 999},
    }
    body = json.dumps(payload)
    with app.test_client() as client:
        resp = _post_webhook(client, body, "workflow_job")
        assert resp.status_code == 200
    kwargs = _last_log_call(_mock_add_installation_event)
    assert kwargs["outcome"] == "job_not_found"


@patch("db.mark_job_completed", return_value="running")
def test_webhook_workflow_job_completed_logs_marked_completed(mock_mc, _mock_add_installation_event):
    from ghfe import app
    payload = {
        "action": "completed",
        "workflow_job": {"id": 12345, "name": "test", "labels": ["ubuntu-24.04-riscv"]},
        "repository": {"id": 100, "full_name": "o/r",
                       "owner": {"id": 152654596, "login": "o", "type": "Organization"}},
        "installation": {"id": 999},
    }
    body = json.dumps(payload)
    with app.test_client() as client:
        resp = _post_webhook(client, body, "workflow_job")
        assert resp.status_code == 200
    kwargs = _last_log_call(_mock_add_installation_event)
    assert kwargs["event"] == "workflow_job.completed"
    assert kwargs["outcome"] == "job_marked_completed"


def test_webhook_workflow_job_unknown_action_logs_ignored_action(_mock_add_installation_event):
    """Verifies the action-whitelist removal from check_webhook_event: unknown
    workflow_job actions like 'waiting' now reach the workflow_job branch and
    log outcome='ignored_action' instead of being rejected wholesale."""
    from ghfe import app
    payload = {
        "action": "waiting",
        "workflow_job": {},
        "repository": {"id": 100, "full_name": "o/r",
                       "owner": {"id": 152654596, "login": "o", "type": "Organization"}},
        "installation": {"id": 999},
    }
    body = json.dumps(payload)
    with app.test_client() as client:
        resp = _post_webhook(client, body, "workflow_job")
        assert resp.status_code == 200
    kwargs = _last_log_call(_mock_add_installation_event)
    assert kwargs["event"] == "workflow_job.waiting"
    assert kwargs["outcome"] == "ignored_action"


def test_webhook_workflow_job_no_label_logs_ignored_no_label(_mock_add_installation_event):
    """ignored_no_label is the highest-volume event; we store a tiny payload
    with only labels, repo full_name, and the workflow_job html_url. Every
    other field that GitHub sends (steps, sender, organization, etc.) is
    dropped."""
    from ghfe import app
    payload = {
        "action": "queued",
        "workflow_job": {
            "id": 12345, "name": "test",
            "labels": ["unsupported-label"],
            "html_url": "https://github.com/o/r/actions/runs/1/job/12345",
            "steps": [{"name": "Set up job"}],     # large field, must be dropped
            "head_sha": "deadbeef",                # dropped
        },
        "repository": {"id": 100, "full_name": "o/r",
                       "owner": {"id": 152654596, "login": "o", "type": "Organization"},
                       "description": "noisy field"},  # dropped
        "installation": {"id": 999},
        "sender": {"login": "noisy"},              # dropped
        "organization": {"login": "noisy"},        # dropped
    }
    body = json.dumps(payload)
    with app.test_client() as client:
        resp = _post_webhook(client, body, "workflow_job")
        assert resp.status_code == 200
        assert b"missing required platform label" in resp.data
    kwargs = _last_log_call(_mock_add_installation_event)
    assert kwargs["event"] == "workflow_job.queued"
    assert kwargs["outcome"] == "ignored_no_label"
    assert kwargs["payload"] == {
        "workflow_job": {
            "labels": ["unsupported-label"],
            "html_url": "https://github.com/o/r/actions/runs/1/job/12345",
        },
        "repository": {"full_name": "o/r"},
    }


def test_webhook_workflow_job_no_label_payload_handles_missing_html_url(_mock_add_installation_event):
    """workflow_job payloads may omit html_url; the trimmer must not crash."""
    from ghfe import app
    payload = {
        "action": "queued",
        "workflow_job": {"id": 12345, "name": "test", "labels": ["unsupported-label"]},
        "repository": {"id": 100, "full_name": "o/r",
                       "owner": {"id": 152654596, "login": "o", "type": "Organization"}},
        "installation": {"id": 999},
    }
    body = json.dumps(payload)
    with app.test_client() as client:
        resp = _post_webhook(client, body, "workflow_job")
        assert resp.status_code == 200
    kwargs = _last_log_call(_mock_add_installation_event)
    assert kwargs["payload"]["workflow_job"]["html_url"] is None


@patch("db.add_job", return_value=True)
def test_webhook_app_id_comes_from_hook_target_id_header(mock_add_job, _mock_add_installation_event):
    """`app_id` is always taken from X-GitHub-Hook-Installation-Target-Id —
    this is the canonical signal of which app delivered the event, and is
    set by GitHub on every App webhook."""
    from ghfe import app
    payload = {
        "action": "queued",
        "workflow_job": {"id": 12345, "name": "test", "labels": ["ubuntu-24.04-riscv"],
                         "html_url": "https://example.com"},
        "repository": {"id": 100, "full_name": "o/r",
                       "owner": {"id": 152654596, "login": "o", "type": "Organization"}},
        "installation": {"id": 999},
    }
    body = json.dumps(payload)
    with app.test_client() as client:
        resp = _post_webhook(client, body, "workflow_job")
        assert resp.status_code == 200
    kwargs = _last_log_call(_mock_add_installation_event)
    assert kwargs["app_id"] == 2167633  # from X-GitHub-Hook-Installation-Target-Id


@patch("db.add_job", return_value=True)
def test_webhook_logs_after_jobs_commit_even_when_log_raises(mock_add_job, _mock_add_installation_event):
    """If add_installation_event raises, the side-effect (add_job) was already
    called and committed in its own transaction. The webhook returns 500 and
    GitHub retries; on retry add_job is a no-op via ON CONFLICT job_id."""
    from ghfe import app
    _mock_add_installation_event.side_effect = RuntimeError("DB hiccup")

    payload = {
        "action": "queued",
        "workflow_job": {"id": 12345, "name": "test", "labels": ["ubuntu-24.04-riscv"],
                         "html_url": "https://example.com"},
        "repository": {"id": 100, "full_name": "o/r",
                       "owner": {"id": 152654596, "login": "o", "type": "Organization"}},
        "installation": {"id": 999},
    }
    body = json.dumps(payload)
    with app.test_client() as client:
        resp = _post_webhook(client, body, "workflow_job")
        assert resp.status_code == 500
    mock_add_job.assert_called_once()


# --- /trace endpoints ---

def _trace_get(client, path, token=TRACE_API_SECRET):
    headers = {"Authorization": f"Bearer {token}"} if token is not None else {}
    return client.get(path, headers=headers)


def test_trace_entity_requires_bearer_token(_mock_add_installation_event):
    from ghfe import app
    with app.test_client() as client:
        resp = client.get("/trace/entity/123")
        assert resp.status_code == 401
        resp = _trace_get(client, "/trace/entity/123", token="wrong")
        assert resp.status_code == 401


@patch("db.get_events_by_entity_id", return_value=[{"id": 1, "event": "installation.created"}])
def test_trace_entity_returns_events(mock_get, _mock_add_installation_event):
    from ghfe import app
    with app.test_client() as client:
        resp = _trace_get(client, "/trace/entity/152654596")
        assert resp.status_code == 200
        assert resp.headers["Content-Type"] == "application/json"
        data = json.loads(resp.data)
        assert data == {"events": [{"id": 1, "event": "installation.created"}]}
    mock_get.assert_called_once_with(152654596)


@patch("db.get_events_by_entity_id", return_value=[{"id": 1}])
@patch("db.get_entity_id_for_installation", return_value=152654596)
def test_trace_installation_translates_to_entity(mock_get_eid, mock_get_events, _mock_add_installation_event):
    from ghfe import app
    with app.test_client() as client:
        resp = _trace_get(client, "/trace/installation/999")
        assert resp.status_code == 200
    mock_get_eid.assert_called_once_with(999)
    mock_get_events.assert_called_once_with(152654596)


@patch("db.get_entity_id_for_installation", return_value=None)
def test_trace_installation_unknown_returns_404(mock_get_eid, _mock_add_installation_event):
    from ghfe import app
    with app.test_client() as client:
        resp = _trace_get(client, "/trace/installation/999")
        assert resp.status_code == 404
        assert b"Entity not found" in resp.data


@patch("db.get_entity_id_for_job", return_value=None)
def test_trace_job_unknown_returns_404(mock_get_eid, _mock_add_installation_event):
    from ghfe import app
    with app.test_client() as client:
        resp = _trace_get(client, "/trace/job/12345")
        assert resp.status_code == 404
        assert b"Entity not found" in resp.data


@patch("db.get_events_by_entity_id", return_value=[{"id": 1}])
@patch("db.get_entity_id_for_job", return_value=152654596)
def test_trace_job_uses_one_hop_lookup(mock_get_eid, mock_get_events, _mock_add_installation_event):
    """job_id -> entity_id direct via jobs.entity_id (one query)."""
    from ghfe import app
    with app.test_client() as client:
        resp = _trace_get(client, "/trace/job/12345")
        assert resp.status_code == 200
    mock_get_eid.assert_called_once_with(12345)
    mock_get_events.assert_called_once_with(152654596)


@patch("db.get_payload_by_id", return_value={"action": "created", "installation": {"id": 999}})
def test_trace_payload_returns_payload_only(mock_get_payload, _mock_add_installation_event):
    from ghfe import app
    with app.test_client() as client:
        resp = _trace_get(client, "/trace/payload/42")
        assert resp.status_code == 200
        data = json.loads(resp.data)
        assert set(data.keys()) == {"payload"}
        assert data["payload"] == {"action": "created", "installation": {"id": 999}}
    mock_get_payload.assert_called_once_with(42)


@patch("db.get_payload_by_id", return_value=None)
def test_trace_payload_not_found(mock_get_payload, _mock_add_installation_event):
    from ghfe import app
    with app.test_client() as client:
        resp = _trace_get(client, "/trace/payload/42")
        assert resp.status_code == 404
        assert b"Payload not found" in resp.data


def test_webhook_missing_hook_target_id_returns_400(_mock_add_installation_event):
    """X-GitHub-Hook-Installation-Target-Id is the canonical app_id signal;
    if it's missing we reject the request with 400 rather than guessing."""
    from ghfe import app
    body = json.dumps({"zen": "ok"})
    sig = "sha256=" + compute_signature(body, GHAPP_WEBHOOK_SECRET).hexdigest()
    with app.test_client() as client:
        # Header omitted on purpose
        resp = client.post("/", data=body, headers={
            "X-Hub-Signature-256": sig,
            "X-Github-Event": "ping",
            "Content-Type": "application/json",
        })
        assert resp.status_code == 400
        assert b"X-GitHub-Hook-Installation-Target-Id" in resp.data


def test_webhook_invalid_hook_target_id_returns_400(_mock_add_installation_event):
    from ghfe import app
    body = json.dumps({"zen": "ok"})
    sig = "sha256=" + compute_signature(body, GHAPP_WEBHOOK_SECRET).hexdigest()
    with app.test_client() as client:
        resp = client.post("/", data=body, headers={
            "X-Hub-Signature-256": sig,
            "X-Github-Event": "ping",
            "X-GitHub-Hook-Installation-Target-Id": "not-an-int",
            "Content-Type": "application/json",
        })
        assert resp.status_code == 400
