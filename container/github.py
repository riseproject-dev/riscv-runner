import functools
import logging
import time

import jwt
import requests

from constants import *

logger = logging.getLogger(__name__)


class GitHubAPIError(Exception):
    """Exception raised for GitHub API errors."""
    def __init__(self, status_code: int, message: str):
        self.status_code = int(status_code)
        self.message = message
        super().__init__(message)


@functools.lru_cache(maxsize=1)
def init_ghapp_private_key_org():
    private_key = jwt.jwk_from_pem(GHAPP_ORG_PRIVATE_KEY.encode('utf-8'))
    assert private_key, "Failed to load private key from GHAPP_ORG_PRIVATE_KEY"
    return private_key


@functools.lru_cache(maxsize=1)
def init_ghapp_private_key_personal():
    private_key = jwt.jwk_from_pem(GHAPP_PERSONAL_PRIVATE_KEY.encode('utf-8'))
    assert private_key, "Failed to load private key from GHAPP_PERSONAL_PRIVATE_KEY"
    return private_key


def generate_jwt(app_id, private_key):
    """Generate a JWT for GitHub App authentication."""
    payload = {
        "iat": int(time.time()),
        "exp": int(time.time()) + (10 * 60),
        "iss": app_id,
    }
    return jwt.JWT().encode(payload, private_key, alg="RS256")


def authenticate_app(installation_id, entity_type):
    """Authenticate the app and get an installation token.

    Uses the org GitHub App for organizations, personal GitHub App for users.
    """
    assert entity_type is not None
    if entity_type == EntityType.USER:
        jwt_token = generate_jwt(GHAPP_PERSONAL_ID, init_ghapp_private_key_personal())
    else:
        jwt_token = generate_jwt(GHAPP_ORG_ID, init_ghapp_private_key_org())

    headers = {
        "Authorization": f"Bearer {jwt_token}",
        "Accept": "application/vnd.github.v3+json",
    }
    url = f"https://api.github.com/app/installations/{installation_id}/access_tokens"
    response = requests.post(url, headers=headers, json={})

    if response.status_code == 201:
        logger.debug("Obtained installation access token for installation %s", installation_id)
        return response.json().get("token")
    else:
        error = response.json().get("message")
        logger.error("Failed to get installation access token for installation %s: %s", installation_id, error)
        raise GitHubAPIError(response.status_code, f"Failed to get installation access token: {error}")


def get_installation(installation_id, entity_type):
    """Fetch installation metadata for the app matching entity_type.

    Returns the parsed JSON (includes account.type, account.login, app_id).
    """
    assert entity_type is not None
    if entity_type == EntityType.USER:
        jwt_token = generate_jwt(GHAPP_PERSONAL_ID, init_ghapp_private_key_personal())
    else:
        jwt_token = generate_jwt(GHAPP_ORG_ID, init_ghapp_private_key_org())

    headers = {
        "Authorization": f"Bearer {jwt_token}",
        "Accept": "application/vnd.github.v3+json",
    }
    url = f"https://api.github.com/app/installations/{installation_id}"
    response = requests.get(url, headers=headers)

    if response.status_code == 200:
        return response.json()
    else:
        error = response.json().get("message")
        logger.error("Failed to get installation %s: %s", installation_id, error)
        raise GitHubAPIError(response.status_code, f"Failed to get installation: {error}")


def ensure_runner_group(entity_name, token, group_name):
    """Ensure the runner group exists and return its ID."""
    headers = {
        "Authorization": f"Bearer {token}",
        "Accept": "application/vnd.github.v3+json",
    }

    list_url = f"https://api.github.com/orgs/{entity_name}/actions/runner-groups"
    response = requests.get(list_url, headers=headers)
    if response.status_code != 200:
        error = response.json()
        logger.error("Failed to list runner groups for org %s: %s", entity_name, error)
        raise GitHubAPIError(response.status_code, f"Failed to list runner groups: {error}")

    for group in response.json().get("runner_groups", []):
        if group.get("name") == group_name:
            logger.debug("Found existing runner group '%s' (id=%s) for org %s",
                        group_name, group["id"], entity_name)
            return group["id"]

    create_body = {
        "name": group_name,
        "visibility": "all",
        "allows_public_repositories": True,
    }
    response = requests.post(list_url, headers=headers, json=create_body)
    if response.status_code == 201:
        runner_group_id = response.json().get("id")
        logger.debug("Created runner group '%s' (id=%s) for org %s",
                     group_name, runner_group_id, entity_name)
        return runner_group_id
    else:
        error = response.json()
        logger.error("Failed to create runner group '%s' for org %s: %s",
                     group_name, entity_name, error)
        raise GitHubAPIError(response.status_code, f"Failed to create runner group: {error}")


def create_jit_runner_config_org(token, group_id, labels, entity_name, runner_name):
    """Create a JIT runner configuration for a new ephemeral runner (org-scoped)."""
    headers = {
        "Authorization": f"Bearer {token}",
        "Accept": "application/vnd.github.v3+json",
    }
    url = f"https://api.github.com/orgs/{entity_name}/actions/runners/generate-jitconfig"
    body = {
        "name": runner_name,
        "runner_group_id": group_id,
        "labels": labels,
    }
    response = requests.post(url, headers=headers, json=body)

    if response.status_code == 201:
        jit_config = response.json().get("encoded_jit_config")
        logger.debug("Created JIT runner config for org %s, runner name=%s", entity_name, runner_name)
        return jit_config
    else:
        error = response.json()
        logger.error("Failed to create JIT runner config for org %s: %s", entity_name, error)
        raise GitHubAPIError(response.status_code, f"Failed to create JIT runner config: {error}")


def create_jit_runner_config_repo(token, labels, repo_full_name, runner_name):
    """Create a JIT runner configuration for a new ephemeral runner (repo-scoped, for personal accounts)."""
    headers = {
        "Authorization": f"Bearer {token}",
        "Accept": "application/vnd.github.v3+json",
    }
    url = f"https://api.github.com/repos/{repo_full_name}/actions/runners/generate-jitconfig"
    body = {
        "name": runner_name,
        "runner_group_id": 1,  # default runner group for repos
        "labels": labels,
    }
    response = requests.post(url, headers=headers, json=body)

    if response.status_code == 201:
        jit_config = response.json().get("encoded_jit_config")
        logger.debug("Created JIT runner config for repo %s, runner name=%s", repo_full_name, runner_name)
        return jit_config
    else:
        error = response.json()
        logger.error("Failed to create JIT runner config for repo %s: %s", repo_full_name, error)
        raise GitHubAPIError(response.status_code, f"Failed to create JIT runner config: {error}")


def get_job_status(repo_full_name, job_id, token):
    """Get the effective status of a workflow job from GitHub API.

    GitHub can return status="in_progress" with conclusion="cancelled" (or other
    terminal conclusions). When a conclusion is present, the job is effectively
    completed regardless of the status field.
    """
    headers = {
        "Authorization": f"Bearer {token}",
        "Accept": "application/vnd.github.v3+json",
    }
    url = f"https://api.github.com/repos/{repo_full_name}/actions/jobs/{job_id}"
    response = requests.get(url, headers=headers)

    if response.status_code == 200:
        data = response.json()
        status = data.get("status")  # queued, in_progress, completed
        conclusion = data.get("conclusion")  # null, success, failure, cancelled, ...
        # A non-null conclusion means the job is done, even if status says in_progress
        if conclusion is not None:
            return "completed"
        return status
    else:
        logger.error("Failed to get job status for %s job %s: %s", repo_full_name, job_id, response.status_code)
        raise GitHubAPIError(response.status_code, f"Failed to get job status: {response.text}")
