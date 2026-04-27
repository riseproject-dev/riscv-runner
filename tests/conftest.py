import sys
import types
from enum import Enum

class EntityType(str, Enum):
    ORGANIZATION = "Organization"
    USER = "User"

# Mock the constants module before any container module is imported.
# This avoids requiring real env vars (PROD, PROD_URL, etc.) during tests.
mock_constants = types.ModuleType("constants")
mock_constants.EntityType = EntityType
mock_constants.PROD = False
mock_constants.PROD_URL = "https://prod.example.com"
mock_constants.STAGING_URL = "https://staging.example.com"
mock_constants.K8S_NAMESPACE = "default"
mock_constants.GHAPP_ORG_ID = 2167633
mock_constants.GHAPP_ORG_PRIVATE_KEY = "test-key"
mock_constants.GHAPP_PERSONAL_ID = 3131217
mock_constants.GHAPP_PERSONAL_PRIVATE_KEY = "test-key-personal"
mock_constants.GHAPP_WEBHOOK_SECRET = "test-webhook-secret"
mock_constants.POSTGRES_URL = "postgresql://test:test@localhost:5432/testdb"
mock_constants.POSTGRES_SCHEMA = "staging"
mock_constants.POSTGRES_MAXCONN = 10
mock_constants.RUNNER_GROUP_NAME = "RISE RISC-V Runners"
mock_constants.RUNNER_NAME_PREFIX = "rise-riscv-runner-staging-"
mock_constants.RUNNER_REGISTRATION_TIMEOUT_SECONDS = 120
mock_constants.RUNNER_PENDING_TIMEOUT_SECONDS = 600
mock_constants.POD_PENDING_TIMEOUT_SECONDS = 600
mock_constants.POD_DELETE_GRACE_SECONDS = 6 * 60 * 60
mock_constants.K8S_KUBECONFIG = None
mock_constants.RISEPROJECT_DEV_ORG_ID = 152654596
mock_constants.PYTORCH_ORG_ID = 21003710
mock_constants.GGML_ORG_ORG_ID = 134263123
mock_constants.RUNNER_IMAGE_UBUNTU_24_04 = "riscv-runner:ubuntu-24.04-latest"
mock_constants.RUNNER_IMAGE_UBUNTU_26_04 = "riscv-runner:ubuntu-26.04-latest"
mock_constants.ENTITY_CONFIG = {
    152654596: {"max_workers": None, "pre_allocated": 0, "staging": True},
    21003710: {"max_workers": 20, "pre_allocated": 0, "staging": False},
    134263123: {"max_workers": 20, "pre_allocated": 0, "staging": False},
}
mock_constants.STAGING_ENTITIES = {oid for oid, c in mock_constants.ENTITY_CONFIG.items() if c.get("staging")}

sys.modules["constants"] = mock_constants


import pytest


@pytest.fixture(autouse=True)
def _clear_gh_authenticate_app_cache():
    """authenticate_app is TTL-cached; clear between tests to avoid cross-test contamination."""
    try:
        import github
        github.authenticate_app.cache_clear()
    except ImportError:
        pass
    yield
