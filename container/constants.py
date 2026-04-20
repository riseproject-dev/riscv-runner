import os
from enum import Enum

class EntityType(str, Enum):
    ORGANIZATION = "Organization"
    USER = "User"

PROD = os.environ["PROD"].lower() == "true"
PROD_URL = os.environ["PROD_URL"]
STAGING_URL = os.environ["STAGING_URL"]

K8S_KUBECONFIG = os.environ["K8S_KUBECONFIG"]

GHAPP_ORG_ID = 2167633  # https://github.com/apps/rise-risc-v-runners
GHAPP_ORG_PRIVATE_KEY = os.environ["GHAPP_ORG_PRIVATE_KEY"]  # PEM-encoded private key for the org GitHub App
GHAPP_PERSONAL_ID = 3131217  # https://github.com/apps/rise-risc-v-runners-personal
GHAPP_PERSONAL_PRIVATE_KEY = os.environ["GHAPP_PERSONAL_PRIVATE_KEY"]  # PEM-encoded private key for the personal GitHub App
GHAPP_WEBHOOK_SECRET = os.environ["GHAPP_WEBHOOK_SECRET"]  # Secret for validating GitHub webhook signatures

POSTGRES_URL = os.environ["POSTGRES_URL"]  # postgresql://user:pass@host:5432/db?sslmode=require
POSTGRES_SCHEMA = "prod" if PROD else "staging"
POSTGRES_MAXCONN = 10

RUNNER_GROUP_NAME = "RISE RISC-V Runners"

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
            "riscv-runner",
            "riscv-runner-app",
            "riscv-runner-images",
            "riscv-runner-sample",
            "riscv-runner-device-plugin",
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
        "staging": [
            "riscv-runner-sample",
        ],
    },
}

STAGING_ENTITIES = {oid: c["staging"] for oid, c in ENTITY_CONFIG.items() if c.get("staging", False)}

RUNNER_REGISTRY = "rg.fr-par.scw.cloud/funcscwriseriscvrunnerappqdvknz9s"
RUNNER_IMAGE = "riscv-runner"
RUNNER_UBUNTU_24_04_TAG = "ubuntu-24.04-latest" if PROD else "ubuntu-24.04-staging"
RUNNER_UBUNTU_26_04_TAG = "ubuntu-26.04-latest" if PROD else "ubuntu-26.04-staging"
RUNNER_DIND_TAG = "dind-latest" if PROD else "dind-staging"

RUNNER_IMAGE_UBUNTU_24_04 = f"{RUNNER_REGISTRY}/{RUNNER_IMAGE}:{RUNNER_UBUNTU_24_04_TAG}"
RUNNER_IMAGE_UBUNTU_26_04 = f"{RUNNER_REGISTRY}/{RUNNER_IMAGE}:{RUNNER_UBUNTU_26_04_TAG}"
RUNNER_IMAGE_DIND = f"{RUNNER_REGISTRY}/{RUNNER_IMAGE}:{RUNNER_DIND_TAG}"
