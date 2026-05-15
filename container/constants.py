import json
import os
from enum import Enum

class EntityType(str, Enum):
    ORGANIZATION = "Organization"
    USER = "User"


class WebhookOutcome(str, Enum):
    """Stored verbatim in installation_events.outcome (TEXT column)."""
    OK = "ok"
    JOB_STORED = "job_stored"
    JOB_ALREADY_EXISTS = "job_already_exists"
    JOB_MARKED_RUNNING = "job_marked_running"
    JOB_MARKED_COMPLETED = "job_marked_completed"
    JOB_NOT_FOUND = "job_not_found"
    IGNORED_ACTION = "ignored_action"
    IGNORED_NO_LABEL = "ignored_no_label"
    IGNORED_EVENT = "ignored_event"
    AUTH_404 = "auth_404"
    AUTH_OTHER_ERROR = "auth_other_error"

PROD = os.environ["PROD"].lower() == "true"
PROD_URL = os.environ["PROD_URL"]
STAGING_URL = os.environ["STAGING_URL"]

K8S_KUBECONFIG = os.environ["K8S_KUBECONFIG"]

GHAPP_ORG_ID = 2167633  # https://github.com/apps/rise-risc-v-runners
GHAPP_ORG_PRIVATE_KEY = os.environ["GHAPP_ORG_PRIVATE_KEY"]  # PEM-encoded private key for the org GitHub App
GHAPP_PERSONAL_ID = 3131217  # https://github.com/apps/rise-risc-v-runners-personal
GHAPP_PERSONAL_PRIVATE_KEY = os.environ["GHAPP_PERSONAL_PRIVATE_KEY"]  # PEM-encoded private key for the personal GitHub App
GHAPP_WEBHOOK_SECRET = os.environ["GHAPP_WEBHOOK_SECRET"]  # Secret for validating GitHub webhook signatures
TRACE_API_SECRET = os.environ["TRACE_API_SECRET"]  # Bearer token gating /trace/* endpoints

POSTGRES_URL = os.environ["POSTGRES_URL"]  # postgresql://user:pass@host:5432/db?sslmode=require
POSTGRES_SCHEMA = "prod" if PROD else "staging"
POSTGRES_MAXCONN = 10

RUNNER_GROUP_NAME = f"RISE RISC-V Runners{'' if PROD else " (staging)"}"
RUNNER_NAME_PREFIX = f"rise-riscv-runner{'' if PROD else '-staging'}-"

RUNNER_REGISTRATION_TIMEOUT_SECONDS = 120           # pod Running but GH never sees runner
RUNNER_PENDING_TIMEOUT_SECONDS      = 600        # pod Running but GH never picks up the runner
POD_PENDING_TIMEOUT_SECONDS         = 600           # pod stuck Pending (no capacity, image pull, etc.)
POD_DELETE_GRACE_SECONDS            = 6 * 60 * 60   # keep terminal pods around so logs remain inspectable

# gh api orgs/<orgname> --jq '.id'
RISEPROJECT_DEV_ORG_ID = 152654596 # github.com/riseproject-dev
PYTORCH_ORG_ID = 21003710 # github.com/pytorch
GGML_ORG_ORG_ID = 134263123 # github.com/ggml-org (for llama.cpp)
# gh api users/<username> --jq '.id'
LUHENRY_USER_ID = 660779 # github.com/luhenry

ENTITY_CONFIG = {
    RISEPROJECT_DEV_ORG_ID: {
        "max_workers": None,
        "pre_allocated": 0,
        "staging": [
            "riscv-runner-sample-staging",
        ],
    },
    PYTORCH_ORG_ID: {
        "max_workers": 20,
        "pre_allocated": 0,
    },
    GGML_ORG_ORG_ID: {
        "max_workers": 20,
        "pre_allocated": 0,
    },
    LUHENRY_USER_ID: {
        "max_workers": None,
        "pre_allocated": 0,
    },
}

STAGING_ENTITIES = {oid: c["staging"] for oid, c in ENTITY_CONFIG.items() if c.get("staging", False)}

GO_GHFE_URL = os.environ.get("GO_GHFE_URL", "")
GO_GHFE_ROUTING: frozenset[int] = frozenset(
    int(e) for e in (json.loads(os.environ["GO_GHFE_ROUTING"]).get("entities") or [])
) if os.environ.get("GO_GHFE_ROUTING") else frozenset()

RUNNER_REGISTRY = "rg.fr-par.scw.cloud/funcscwriseriscvrunnerappqdvknz9s"
RUNNER_IMAGE = "riscv-runner"
RUNNER_UBUNTU_24_04_TAG = "ubuntu-24.04-latest" if PROD else "ubuntu-24.04-staging"
RUNNER_UBUNTU_26_04_TAG = "ubuntu-26.04-latest" if PROD else "ubuntu-26.04-staging"

RUNNER_IMAGE_UBUNTU_24_04 = f"{RUNNER_REGISTRY}/{RUNNER_IMAGE}:{RUNNER_UBUNTU_24_04_TAG}"
RUNNER_IMAGE_UBUNTU_26_04 = f"{RUNNER_REGISTRY}/{RUNNER_IMAGE}:{RUNNER_UBUNTU_26_04_TAG}"
