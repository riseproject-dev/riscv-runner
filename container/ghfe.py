import hashlib
import hmac
import json
import logging
import requests
import time

from flask import Flask, g, request, make_response

import db
import github as gh
from constants import *


ORG_APP_INSTALL_URL = "https://github.com/apps/rise-risc-v-runners/installations/new"
PERSONAL_APP_INSTALL_URL = "https://github.com/apps/rise-risc-v-runners-personal/installations/new"

app = Flask(__name__)

logger = logging.getLogger(__name__)


class WebhookError(Exception):
    """Exception raised during webhook processing."""
    def __init__(self, status_code, message):
        self.status_code = status_code
        self.message = message
        super().__init__(message)


@app.errorhandler(WebhookError)
def handle_webhook_error(e):
    if e.status_code == 200:
        logger.debug(e.message)
    else:
        logger.warning(e.message)
    return make_response(e.message, e.status_code)


@app.errorhandler(AssertionError)
def handle_assertion_error(e):
    logger.info(e)
    return make_response(str(e), 400)


@app.before_request
def _start_timer():
    g.print_perf_log = False
    g.request_start = time.perf_counter()


@app.after_request
def _log_duration(response):
    if request.method == "GET" and request.path == "/health":
        pass
    elif not g.print_perf_log:
        pass
    else:
        elapsed_ms = (time.perf_counter() - g.request_start) * 1000
        logger.info(
            "%s %s -> %d in %.1fms",
            request.method, request.path, response.status_code, elapsed_ms,
        )
    return response


# --- Webhook validation ---

def compute_signature(body, secret):
    return hmac.new(secret.encode('utf-8'), msg=body.encode('utf-8'), digestmod=hashlib.sha256)


def verify_signature(body, signature, secret):
    """Verify that the body was sent from GitHub by validating the signature."""
    if not signature:
        return False, "X-Hub-Signature-256 header is missing!"

    hash = compute_signature(body, secret)
    expected_signature = "sha256=" + hash.hexdigest()

    if not hmac.compare_digest(expected_signature, signature):
        return False, f"Request signatures didn't match! Expected: {expected_signature}, Got: {signature}"

    return True, "Signatures match"


def check_webhook_signature(headers, body):
    """Verify the webhook signature."""
    if not "X-Github-Event" in request.headers:
        raise WebhookError(400, "Missing X-Github-Event header")
    event = headers["X-Github-Event"]

    if not "X-Hub-Signature-256" in request.headers:
        raise WebhookError(400, "Missing X-Hub-Signature-256 header")
    signature = headers["X-Hub-Signature-256"]

    is_valid, message = verify_signature(body, signature, GHAPP_WEBHOOK_SECRET)
    if not is_valid:
        logger.warning("Webhook signature verification failed: %s", message)
        raise WebhookError(401, message)

    return event, body


def check_webhook_event(body):
    """Check if the event is a workflow_job with a handled action."""
    try:
        payload = json.loads(body)
    except json.JSONDecodeError:
        logger.debug("Invalid JSON payload")
        raise WebhookError(400, "Invalid JSON payload")

    action = payload["action"]
    if action not in ("queued", "in_progress", "completed"):
        logger.debug("Ignoring action: %s", action)
        raise WebhookError(200, f"Ignoring action: {action}")

    return payload, action


def authorize_entity(payload):
    """Authorize the repository owner (organization or personal account)."""
    owner = payload["repository"]["owner"]
    owner_id = owner["id"]
    if not owner_id:
        raise WebhookError(400, "Owner ID is missing in payload")

    owner_type = owner["type"]
    if not owner_type:
        raise WebhookError(400, "Owner Type is missing in payload")
    if owner_type not in (EntityType.ORGANIZATION, EntityType.USER):
        raise WebhookError(400, f"Unsupported owner type: {owner_type}")

    entity_type = EntityType(owner_type)

    return owner_id, entity_type


def match_labels_to_k8s(org_id, repo_full_name, job_labels):
    """
    Map workflow job labels to a k8s pool name and container image.

    Returns (k8s_pool, k8s_image) where k8s_pool is the board name string
    used as k8s pool key and pod label.
    """
    # Special case(s) for PyTorch org
    if org_id == PYTORCH_ORG_ID or (org_id == RISEPROJECT_DEV_ORG_ID and repo_full_name in ["riseproject-dev/pytorch", "riseproject-dev/executorch"]):
        if any("linux.riscv64.xlarge" in job_label or "linux.riscv64.2xlarge" in job_label for job_label in job_labels):
            return "scw-em-rv1", RUNNER_IMAGE_UBUNTU_24_04
        elif "ubuntu-24.04-riscv" in job_labels:
            return "scw-em-rv1", RUNNER_IMAGE_UBUNTU_24_04
        else:
            raise WebhookError(200, f"Ignoring job: missing required platform label (got {job_labels}) for PyTorch org")

    # Special case(s) for GGML org
    elif org_id == GGML_ORG_ORG_ID or (org_id == RISEPROJECT_DEV_ORG_ID and repo_full_name in ["riseproject-dev/llama.cpp", "riseproject-dev/llama.cpp-validation"]):
        if job_labels == ["ubuntu-24.04-riscv"]:
            return "cloudv10x-jupiter", RUNNER_IMAGE_UBUNTU_24_04
        else:
            raise WebhookError(200, f"Ignoring job: missing required platform label (got {job_labels}) for GGML org")

    # General cases
    elif job_labels == ["ubuntu-24.04-riscv"]:
        return "scw-em-rv1", RUNNER_IMAGE_UBUNTU_24_04
    # FIXME: there is no hardware that supports 26.04 (RVA23) just yet
    # elif job_labels == ["ubuntu-26.04-riscv"]:
    #     return "scw-em-rv1", RUNNER_IMAGE_UBUNTU_26_04

    raise WebhookError(200, f"Ignoring job: missing required platform label (got {job_labels})")


# --- Routes ---

@app.route("/health", methods=['GET'])
def health():
    return "ok"


def _setup_page(title, body_html, status=200):
    html = f"""<!doctype html>
<html><head><meta charset="utf-8"><title>{title}</title>
<style>
body {{ font-family: -apple-system, system-ui, sans-serif; max-width: 640px; margin: 3rem auto; padding: 0 1rem; color: #222; }}
h1 {{ font-size: 1.5rem; }}
.ok {{ color: #0a7f2e; }}
.err {{ color: #b00020; }}
a.button {{ display: inline-block; background: #0969da; color: #fff; padding: 0.5rem 1rem; border-radius: 6px; text-decoration: none; margin-top: 0.5rem; }}
code {{ background: #f3f3f3; padding: 0.1rem 0.3rem; border-radius: 3px; }}
</style></head>
<body>{body_html}</body></html>"""
    return make_response(html, status)


def _render_setup(expected):
    installation_id = request.args.get("installation_id")
    if not installation_id:
        return _setup_page(
            "RISE RISC-V Runners — Setup",
            f"""<h1 class="err">Missing installation id</h1>
<p>This page is the post-install redirect target for the RISE RISC-V Runners GitHub Apps. It expects an <code>installation_id</code> query parameter, which GitHub normally appends after installation.</p>
<p>If you got here by mistake, you can (re-)install one of the apps:</p>
<p><a class="button" href="{ORG_APP_INSTALL_URL}">Install on an organization</a> <a class="button" href="{PERSONAL_APP_INSTALL_URL}">Install on a personal account</a></p>""",
            status=400,
        )

    try:
        installation = gh.get_installation(installation_id, entity_type=expected)
    except gh.GitHubAPIError as e:
        if e.status_code == 404:
            wrong_app_name = "personal" if expected == EntityType.ORGANIZATION else "organization"
            right_url = PERSONAL_APP_INSTALL_URL if expected == EntityType.ORGANIZATION else ORG_APP_INSTALL_URL
            return _setup_page(
                "RISE RISC-V Runners — Wrong app",
                f"""<h1 class="err">Installation not found for this app</h1>
<p>We couldn't find installation <code>{installation_id}</code> under the app you just installed. The most likely cause is that you installed the <strong>{"organization" if expected == EntityType.ORGANIZATION else "personal"} app</strong> on a <strong>{wrong_app_name} account</strong> — these two must match.</p>
<p>Please uninstall it from your GitHub settings and install the correct app:</p>
<p><a class="button" href="{right_url}">Install the {wrong_app_name} app</a></p>""",
                status=404,
            )
        logger.error("Unexpected error fetching installation %s: %s", installation_id, e)
        return _setup_page(
            "RISE RISC-V Runners — Setup error",
            f"""<h1 class="err">Something went wrong</h1>
<p>GitHub returned an error while validating your installation (<code>{e.status_code}</code>). Please try again in a minute, or contact the RISE team if the problem persists.</p>""",
            status=502,
        )

    account = installation.get("account") or {}
    account_type = account.get("type")
    account_login = account.get("login", "(unknown)")

    if account_type == expected.value:
        return _setup_page(
            "RISE RISC-V Runners — Installed",
            f"""<h1 class="ok">All set, {account_login}!</h1>
<p>The RISE RISC-V Runners {"organization" if expected == EntityType.ORGANIZATION else "personal"} app is correctly installed on <code>{account_login}</code>.</p>
<p>You can now trigger GitHub Actions jobs with the <code>ubuntu-24.04-riscv</code> label and they will be picked up automatically.</p>""",
        )

    # Mismatch: user installed this app on the wrong account type.
    if expected == EntityType.ORGANIZATION:
        logger.info("Entity %s installed Personal Account app on Organization, account_type=%s account_login=%s", account_login, account_type, account_login)
        return _setup_page(
            "RISE RISC-V Runners — Wrong account type",
            f"""<h1 class="err">You installed the organization app on a personal account</h1>
<p>The <strong>RISE RISC-V Runners</strong> (organization) app was installed on personal account <code>{account_login}</code>. It only works on GitHub <em>organizations</em>.</p>
<p>For personal accounts, install the dedicated personal app instead:</p>
<p><a class="button" href="{PERSONAL_APP_INSTALL_URL}">Install the personal app</a></p>
<p>You should also uninstall the organization app from <code>{account_login}</code>'s GitHub settings to avoid confusion.</p>""",
            status=400,
        )
    else:
        logger.info("Entity %s installed Organization app on Personal Account, account_type=%s account_login=%s", account_login, account_type, account_login)
        return _setup_page(
            "RISE RISC-V Runners — Wrong account type",
            f"""<h1 class="err">You installed the personal app on an organization</h1>
<p>The <strong>RISE RISC-V Runners (personal)</strong> app was installed on organization <code>{account_login}</code>. It only works on personal GitHub accounts.</p>
<p>For organizations, install the dedicated organization app instead:</p>
<p><a class="button" href="{ORG_APP_INSTALL_URL}">Install the organization app</a></p>
<p>You should also uninstall the personal app from <code>{account_login}</code>'s GitHub settings to avoid confusion.</p>""",
            status=400,
        )


@app.route("/setup/org", methods=["GET"])
def setup_org():
    return _render_setup(expected=EntityType.ORGANIZATION)


@app.route("/setup/personal", methods=["GET"])
def setup_personal():
    return _render_setup(expected=EntityType.USER)


@app.route("/", methods=['POST'])
def webhook():
    event, body = check_webhook_signature(request.headers, request.get_data(as_text=True))

    if event == "ping":
        return f"pong"

    elif event == "workflow_job":
        payload, action = check_webhook_event(body)

        owner_id, entity_type = authorize_entity(payload)

        # Check if we should redirect to staging
        if PROD:
            repo_name = payload["repository"].get("name")
            if owner_id in STAGING_ENTITIES and repo_name and repo_name in STAGING_ENTITIES[owner_id]:
                g.print_perf_log = True
                logger.debug("Proxying request for entity=%s repo=%s to staging (%s)", owner_id, repo_name, STAGING_URL)
                resp = requests.post(
                    STAGING_URL,
                    data=request.get_data(),
                    headers={k: v for k, v in request.headers if k.lower() != "host"},
                    timeout=30,
                )
                logger.info("Proxied request for entity=%s repo=%s to staging, status=%s", owner_id, repo_name, resp.status_code)
                return make_response(resp.content, resp.status_code)

        job_id = payload["workflow_job"]["id"]
        if not job_id:
            raise WebhookError(400, "Job ID is missing in payload")

        # labels may be missing when no labels are defined
        job_labels = payload["workflow_job"]["labels"] or []

        repo_full_name = payload["repository"]["full_name"]
        if not repo_full_name:
            raise WebhookError(400, "Repository full name is missing in payload")

        repo_id = payload["repository"]["id"]
        if not repo_id:
            raise WebhookError(400, "Repository ID is missing in payload")

        # entity_id: owner_id (org) for organizations, repo_id for personal accounts
        entity_id = owner_id if entity_type == EntityType.ORGANIZATION else repo_id

        # Make sure the required labels are present; Filters out unsupported jobs early
        k8s_pool, k8s_image = match_labels_to_k8s(owner_id, repo_full_name, job_labels)

        logger.info("Received %s workflow_job id=%s name=%s repo=%s labels=%s entity_type=%s",
                    action, job_id, payload["workflow_job"]["name"],
                    payload["repository"]["full_name"],
                    payload["workflow_job"]["labels"],
                    entity_type.value)

        # Only enable printing if we know we care for that webhook
        g.print_perf_log = True

        if action == "queued":
            installation_id = payload["installation"]["id"]
            if not installation_id:
                raise WebhookError(400, "Installation ID is missing in payload")

            entity_name = payload["repository"]["owner"]["login"]
            if not entity_name:
                raise WebhookError(400, "Entity name is missing in payload")

            html_url = payload["workflow_job"]["html_url"]
            if not html_url:
                raise WebhookError(400, "HTML URL is missing in payload")

            stored = db.add_job(
                job_id=job_id,
                provider="github",
                entity_id=entity_id,
                entity_name=entity_name,
                entity_type=entity_type,
                repo_full_name=repo_full_name,
                installation_id=installation_id,
                labels=job_labels,
                k8s_pool=k8s_pool,
                k8s_image=k8s_image,
                html_url=html_url,
            )

            if stored:
                return f"Job {job_id} stored."
            else:
                return f"Job {job_id} already exists."

        elif action == "in_progress":
            prev_status = db.mark_job_running(job_id, payload["workflow_job"].get("runner_name"))
            if prev_status is None:
                logger.warning("Job %s not found on in_progress event", job_id)
                return f"Job {job_id} not found."
            logger.info("Job %s marked running (was %s)", job_id, prev_status)
            return f"Job {job_id} marked running (was {prev_status})."

        elif action == "completed":
            prev_status = db.mark_job_completed(job_id, payload["workflow_job"].get("runner_name"))
            if prev_status is None:
                logger.warning("Job %s not found on completed event", job_id)
                return f"Job {job_id} not found."
            return f"Job {job_id} completed (was {prev_status})."

        else:
            return f"Ignoring {action} job"

    else:
        return f"Ignoring {event} event"

if __name__ == "__main__":
    # Set the logging level for all loggers to INFO
    logging.basicConfig(
        level=logging.getLevelNamesMapping()[os.environ.get("LOGLEVEL", "INFO")],
        format='%(pathname)s:%(lineno)d::%(funcName)s: [%(levelname)s] %(message)s',
        datefmt='%Y-%m-%d %H:%M:%S'
    )

    # Ensure PostgreSQL schema/tables exist
    db.ensure_schema()

    from waitress import serve

    HOST = "0.0.0.0"
    PORT = 8080

    print(f"Starting server on http://{HOST}:{PORT}")
    serve(app, host=HOST, port=PORT)
