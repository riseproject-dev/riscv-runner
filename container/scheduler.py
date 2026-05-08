import datetime
import itertools
import json
import logging
import random
import string
import threading
import traceback
from collections.abc import Callable, Iterable
from typing import Any

import db
from db import DuplicateRunnerNameException
import k8s
from k8s import FailureReason
import github as gh
from constants import *

from flask import Flask, request, make_response
from flask.json import dumps as json_dumps

# Used for /health for now
app = Flask(__name__)

logger = logging.getLogger(__name__)

POLL_INTERVAL = 15


def _gh_authenticate_app(installation_id, entity_type, *, job_id=None,
                         repo_full_name=None, repo_id=None,
                         entity_id=None, entity_name=None):
    """Wrap gh.authenticate_app and log every failure to installation_events.

    Successful auths are not logged (gh.authenticate_app's @ttl_cache makes
    success a hot path). Only GitHubAPIError failures are recorded — the
    cache itself doesn't store exceptions, so transient errors won't poison
    subsequent calls.
    """
    app_id = GHAPP_PERSONAL_ID if entity_type == EntityType.USER else GHAPP_ORG_ID
    try:
        return gh.authenticate_app(int(installation_id), app_id)
    except gh.GitHubAPIError as e:
        outcome = WebhookOutcome.AUTH_404 if e.status_code == 404 else WebhookOutcome.AUTH_OTHER_ERROR
        event_str = (f"auth_attempt.{e.status_code}"
                     if e.status_code == 404
                     else "auth_attempt.other_error")
        synthetic_payload = {
            "installation_id": int(installation_id),
            "app_id": app_id,
            "entity_type": entity_type.value,
            "entity_id": entity_id,
            "entity_name": entity_name,
            "repository": ({"id": repo_id, "full_name": repo_full_name}
                           if repo_full_name else None),
            "workflow_job": {"id": job_id} if job_id else None,
            "http_status": e.status_code,
            "error_message": str(e),
        }
        try:
            db.add_installation_event(
                source="scheduler",
                event=event_str,
                outcome=outcome,
                installation_id=int(installation_id),
                app_id=app_id,
                entity_type=entity_type.value,
                entity_id=entity_id,
                entity_name=entity_name,
                payload=synthetic_payload,
            )
        except Exception:
            logger.exception("Failed to record auth_attempt event_str=%s installation_id=%s",
                             event_str, installation_id)
        raise


def sync_jobs_state():
    """
    Sync job status between GitHub and the jobs table.

    For each active job, check GitHub for its actual status. If GitHub says
    completed but database disagrees, mark it completed. If GitHub says in_progress
    but database says pending, update to running.

    `gh.authenticate_app` is cached, so we iterate jobs flatly without batching.
    """
    jobs = db.get_active_jobs()
    if not jobs:
        return

    for job in jobs:
        assert job['status'] in ['pending', 'running'], f"Job job_id={job['job_id']} is not running or pending, status={job['status']}"
        job_id = job["job_id"]
        installation_id = job["installation_id"]
        entity_type = EntityType(job["entity_type"])
        repo = job.get("repo_full_name")
        if not repo:
            continue

        try:
            token = _gh_authenticate_app(
                int(installation_id), entity_type=entity_type,
                job_id=job_id,
                repo_full_name=repo,
                entity_id=job["entity_id"],
                entity_name=job["entity_name"],
            )
        except gh.GitHubAPIError as e:
            if e.status_code == 404:
                logger.warning("Installation not found installation_id=%s entity_type=%s, marking job %s failed",
                               installation_id, entity_type, job_id)
                db.mark_job_failed(job_id, {
                    "version": 1,
                    "message": f"installation not found for installation_id={installation_id} entity_type={entity_type}",
                })
                continue
            logger.error("Failed to authenticate for installation installation_id=%s entity_type=%s: %s",
                         installation_id, entity_type, e)
            continue

        try:
            gh_job = gh.get_job_info(repo, job_id, token)
        except gh.GitHubAPIError as e:
            if e.status_code == 404:
                logger.warning("Job not found job_id=%s entity=%s entity_id=%s entity_type=%s: marking as failed",
                               job_id, entity_type, job['entity_id'], job['entity_name'])
                db.mark_job_failed(job_id, {
                    "version": 1,
                    "message": f"job not found for job_id={job_id} entity={job['entity_name']} entity_id={job['entity_id']} entity_type={entity_type}",
                })
                continue
            logger.error("Failed to get status for job job_id=%s entity=%s entity_id=%s entity_type=%s: %s",
                         job_id, entity_type, job['entity_id'], job['entity_name'], e)
            continue

        gh_job_status = gh_job.get("status")  # queued, in_progress, completed
        gh_job_conclusion = gh_job.get("conclusion")  # null, success, failure, cancelled, ...
        # A non-null conclusion means the job is done, even if status says in_progress
        if gh_job_conclusion is not None:
            gh_job_status = "completed"

        if gh_job_status == "completed":
            logger.info("GH reconcile: job %s is completed on GitHub (was %s in DB)", job_id, job["status"])
            db.mark_job_completed(job_id, gh_job.get("runner_name"))
        elif gh_job_status == "in_progress" and job["status"] == "pending":
            logger.info("GH reconcile: job %s is in_progress on GitHub (was pending in DB)", job_id)
            db.mark_job_running(job_id, gh_job.get("runner_name"))


def _gh_runner_key_for_worker(worker):
    """Return (installation_id, entity_type, entity_id, gh_runner_target).

    For organizations: gh_runner_target = entity_name.
    For users:         gh_runner_target = repo_full_name.
    """
    entity_type = EntityType(worker["entity_type"])
    target = worker["entity_name"] if entity_type == EntityType.ORGANIZATION else worker["repo_full_name"]
    return (int(worker["installation_id"]), entity_type, int(worker["entity_id"]), target)


def _get_gh_runners(gh_runner_key, token, gh_runners_by_target):
    if gh_runner_key not in gh_runners_by_target:
        _, entity_type, _, target = gh_runner_key
        try:
            if entity_type == EntityType.ORGANIZATION:
                group_id = gh.ensure_runner_group(target, token, RUNNER_GROUP_NAME)
                raw = gh.list_runners_org_group(token, target, group_id)
            else:
                raw = [r for r in gh.list_runners_repo(token, target)
                       if r.get("name", "").startswith(RUNNER_NAME_PREFIX)]
        except gh.GitHubAPIError as e:
            logger.error("Failed to list GH runners for %s/%s: %s", entity_type, target, e)
            gh_runners_by_target[gh_runner_key] = {}
            return gh_runners_by_target[gh_runner_key]
        gh_runners_by_target[gh_runner_key] = {r["name"]: r for r in raw}
    return gh_runners_by_target[gh_runner_key]


def _delete_gh_runner(worker_name, token, entity_type, gh_runner_target, runner_id):
    """Delete a GH runner by id. Logs on failure, swallows exceptions."""
    try:
        if entity_type == EntityType.ORGANIZATION:
            gh.delete_runner_org(token, gh_runner_target, runner_id)
        else:
            gh.delete_runner_repo(token, gh_runner_target, runner_id)
        logger.info("Deleted GH runner name=%s id=%s from entity=%s", worker_name, runner_id, gh_runner_target)
        return True
    except Exception as e:
        logger.error("Failed to delete GH runner name=%s id=%s from entity=%s: %s", worker_name, runner_id, gh_runner_target, e)
        return False


def _fail_and_cleanup(worker, pod, token, entity_type, gh_runner_target, gh_runner: dict | None, reason: FailureReason):
    """Mark a worker failed, kill its pod, and remove any stale GH registration.

    If GitHub has a runner for this worker (gh_runner is not None), try to delete
    it first. A non-2xx from GitHub (e.g. 422 "runner is busy") is our signal
    that GH thinks a job is actually executing — abort cleanup so we don't kill
    a worker that is doing useful work we missed signal for. Otherwise proceed:
    collect diagnostics, mark the worker failed, and kill the pod so its slot
    frees up. Phase 5's grace window later removes the Failed pod.
    """
    logger.warning("Health check failed for pod=%s reason=%s", worker["pod_name"], reason.value)
    if gh_runner:
        if not _delete_gh_runner(worker["pod_name"], token, entity_type, gh_runner_target, gh_runner["id"]):
            logger.warning("Aborting cleanup for worker=%s: GitHub refused to delete the runner (may be running a job)",
                           worker["pod_name"])
            return
    try:
        failure_info = k8s.collect_pod_failure_info(pod, reason=reason)
    except Exception as e:
        logger.error("collect_pod_failure_info failed for %s: %s", worker["pod_name"], e)
        failure_info = {"version": 2, "reason": reason.value, "collect_error": str(e)}
    db.mark_worker_failed(worker["pod_name"],
                          pod.spec.node_name or worker["k8s_node"],
                          failure_info,
                          datetime.datetime.now(datetime.timezone.utc))
    try:
        k8s.kill_pod(pod)
    except Exception as e:
        logger.error("kill_pod failed for %s: %s", pod.metadata.name, e)


def _age_seconds(ts):
    """Seconds elapsed since ts (a datetime, possibly naive). Returns +inf if ts is None."""
    if ts is None:
        return float("inf")
    if ts.tzinfo is None:
        ts = ts.replace(tzinfo=datetime.timezone.utc)
    return (datetime.datetime.now(datetime.timezone.utc) - ts).total_seconds()


def _group_by(elements: Iterable, key: Callable[[Any], Any]):
    # itertools.groupby requires sorted elements by key before grouping by key
    return itertools.groupby(sorted(elements, key=key), key=key)


def _sync_workers_state_phase_1_orphan_sweep(pods_by_name, workers_by_name):
    """Phase 1 of sync_workers_state: mark workers without any corresponding pods as orphaned."""
    for pod_name, w in workers_by_name.items():
        if pod_name not in pods_by_name and w["status"] in ["pending", "running"]:
            # There are no pods for that worker
            db.mark_worker_orphaned(pod_name)


def _sync_workers_state_phase_2_pod_phase_sync(pods_by_name, workers_by_name):
    """Phase 2 of sync_workers_state: synchronize pod phases with worker status in the database."""
    for pod_name, pod in pods_by_name.items():
        if pod.status.phase == "Running" and pod_name in workers_by_name and workers_by_name[pod_name]["status"] in ["pending"]:
            db.mark_worker_running(pod_name, pod.spec.node_name, k8s.get_runner_running_at(pod))
        elif pod.status.phase == "Succeeded" and pod_name in workers_by_name and workers_by_name[pod_name]["status"] in ["pending", "running"]:
            db.mark_worker_completed(pod_name, pod.spec.node_name, k8s.get_pod_finished_at(pod))
        elif pod.status.phase == "Failed" and pod_name in workers_by_name and workers_by_name[pod_name]["status"] in ["pending", "running"]:
            try:
                failure_info = k8s.collect_pod_failure_info(pod, reason=FailureReason.POD_FAILED)
            except Exception as e:
                logger.error("Failed to collect failure info for pod %s: %s", pod.metadata.name, e)
                failure_info = {
                    "version": 2,
                    "reason": FailureReason.POD_FAILED.value,
                    "collect_error": str(e),
                }
            assert failure_info and "version" in failure_info and isinstance(failure_info["version"], int), \
                f"Failed pod {pod_name} requires failure_info with int 'version' field"
            db.mark_worker_failed(pod_name, pod.spec.node_name, failure_info, k8s.get_pod_finished_at(pod))


def _sync_workers_state_phase_3_health_checks(pods_by_name, workers_by_name, gh_runners_by_target):
    """Phase 3 of sync_workers_state: GitHub runner health checks.

    For pending/running workers grouped by GitHub runner scope: if a Running pod older
    than RUNNER_REGISTRATION_TIMEOUT_SECONDS is still missing from GH, or a Pending pod
    is older than POD_PENDING_TIMEOUT_SECONDS, kill the pod and mark the worker failed.
    Populates ``gh_runners_by_target`` as a side effect for Phase 4 to consume.
    """
    # Sort before groupby so workers with the same scope are grouped together
    # (groupby only groups adjacent equal keys).
    workers_by_gh_runner_key = _group_by((w for w in workers_by_name.values() if w["status"] in ["pending", "running"]), key=_gh_runner_key_for_worker)
    for gh_runner_key, workers in workers_by_gh_runner_key:
        installation_id, entity_type, entity_id, gh_runner_target = gh_runner_key
        try:
            token = _gh_authenticate_app(
                installation_id, entity_type=entity_type,
                entity_id=entity_id,
                entity_name=(gh_runner_target if entity_type == EntityType.ORGANIZATION else None),
                repo_full_name=(gh_runner_target if entity_type == EntityType.USER else None),
            )
        except gh.GitHubAPIError as e:
            logger.error("Failed to authenticate for installation_id=%s entity_type=%s gh_runner_target=%s: %s", installation_id, entity_type, gh_runner_target, e)
            continue

        # Consume the groupby elements into a list that we can iterate multiple times
        workers = list(workers)

        gh_runners = _get_gh_runners(gh_runner_key, token, gh_runners_by_target)

        logger.debug(f"Checking for workers={RUNNER_NAME_PREFIX}%s in runners={RUNNER_NAME_PREFIX}%s for target=%s entity_type=%s",
                    sorted([w["pod_name"].removeprefix(RUNNER_NAME_PREFIX) for w in workers]),
                    sorted([r.removeprefix(RUNNER_NAME_PREFIX) for r in gh_runners.keys()]),
                    gh_runner_target,
                    entity_type)

        for w in workers:
            worker_name = w["pod_name"]
            assert worker_name in pods_by_name
            pod = pods_by_name[worker_name]
            gh_runner = gh_runners.get(worker_name)

            worker_status = w["status"]
            runner_status, runner_busy = (gh_runner["status"], gh_runner["busy"]) if gh_runner else (None, None)

            # If the worker is still pending
            if (worker_status) == ("pending"):
                if _age_seconds(pod.metadata.creation_timestamp) < POD_PENDING_TIMEOUT_SECONDS:
                    logger.debug("Worker worker=%s worker_status=%s runner_status=%s is still pending", worker_name, worker_status, runner_status)
                    continue
                logger.warning("Worker worker=%s worker_status=%s runner_status=%s is still pending after more than %d seconds, marking as failed", worker_name, worker_status, runner_status, POD_PENDING_TIMEOUT_SECONDS)
                _fail_and_cleanup(w, pod, token, entity_type, gh_runner_target, gh_runner,
                                    reason=FailureReason.POD_STUCK_PENDING)
                continue

            # If the worker is running but the runner is unknown, it may be that the runner has already self-unregistered after executing a job
            elif (worker_status, runner_status) == ("running", None):
                if db.job_exists_for_pod(worker_name):
                    logger.debug("Worker worker=%s worker_status=%s runner_status=%s runner has already run a job and self-unregistered, skipping", worker_name, worker_status, runner_status)
                    continue
                if _age_seconds(w["running_at"]) < RUNNER_REGISTRATION_TIMEOUT_SECONDS:
                    logger.info("Worker worker=%s worker_status=%s runner_status=%s is not known github runner and may still register", worker_name, worker_status, runner_status)
                    continue
                logger.warning("Worker worker=%s worker_status=%s runner_status=%s is not known github runner and failed to register in %d seconds, marking as failed", worker_name, worker_status, runner_status, RUNNER_REGISTRATION_TIMEOUT_SECONDS)
                _fail_and_cleanup(w, pod, token, entity_type, gh_runner_target, gh_runner,
                                    reason=FailureReason.RUNNER_NEVER_REGISTERED)
                continue

            # If the worker is running but the runner isn't running yet
            elif (worker_status, runner_status) == ("running", "offline"):
                if _age_seconds(w["running_at"]) < RUNNER_REGISTRATION_TIMEOUT_SECONDS:
                    logger.info("Worker worker=%s worker_status=%s runner_status=%s is known github runner and may still register", worker_name, worker_status, runner_status)
                    continue
                logger.warning("Worker worker=%s worker_status=%s runner_status=%s is known github runner and failed to register in %d seconds, marking as failed", worker_name, worker_status, runner_status, RUNNER_REGISTRATION_TIMEOUT_SECONDS)
                _fail_and_cleanup(w, pod, token, entity_type, gh_runner_target, gh_runner,
                                    reason=FailureReason.RUNNER_NEVER_REGISTERED)

            # If the worker and runner are running but the runner hasn't picked up a job yet
            elif (worker_status, runner_status, runner_busy) == ("running", "online", False):
                if _age_seconds(w["running_at"]) < RUNNER_PENDING_TIMEOUT_SECONDS:
                    logger.info("Worker worker=%s worker_status=%s runner_status=%s is known github runner and may still pick up a job", worker_name, worker_status, runner_status)
                    continue
                logger.warning("Worker worker=%s worker_status=%s runner_status=%s is known github runner and failed to pick up a job in %d seconds, marking as failed", worker_name, worker_status, runner_status, RUNNER_PENDING_TIMEOUT_SECONDS)
                _fail_and_cleanup(w, pod, token, entity_type, gh_runner_target, gh_runner,
                                    reason=FailureReason.RUNNER_IDLE)
                continue

            # If the worker and runner are running and the runner has picked up a job
            elif (worker_status, runner_status, runner_busy) == ("running", "online", True):
                # Nothing to do, everything is working!
                pass

            # If the worker is running, but the status of the runner is unknown
            elif (worker_status) == ("running"):
                assert runner_status not in [None, "offline", "online"]
                logger.info("Worker worker=%s worker_status=%s runner_status=%s has unkown github status", worker_name, worker_status, runner_status)
                if _age_seconds(w["running_at"]) < RUNNER_REGISTRATION_TIMEOUT_SECONDS:
                    logger.info("Worker worker=%s worker_status=%s runner_status=%s is known github runner and may still register", worker_name, worker_status, runner_status, )
                    continue
                logger.warning("Worker worker=%s worker_status=%s runner_status=%s is known github runner and in unknown state for after %d seconds, marking as failed", worker_name, worker_status, runner_status, RUNNER_REGISTRATION_TIMEOUT_SECONDS)
                _fail_and_cleanup(w, pod, token, entity_type, gh_runner_target, gh_runner,
                                    reason=FailureReason.RUNNER_NEVER_REGISTERED)
                continue

            else: # pragma: no cover
                assert False, f"unexpected worker status (worker_status={worker_status!r}, runner_status={runner_status!r}, runner_busy={runner_busy!r}) for worker={worker_name}"


def _sync_workers_state_phase_4_gh_cleanup(workers_by_name, gh_runners_by_target):
    """Phase 4 of sync_workers_state: delete orphan/completed/failed runners on GitHub.

    Any runner matching RUNNER_NAME_PREFIX whose worker row is terminal or missing
    gets deleted on GitHub. Reads the cache populated by Phase 3.
    """
    for gh_runner_key, gh_runners in gh_runners_by_target.items():
        installation_id, entity_type, entity_id, gh_runner_target = gh_runner_key
        try:
            token = _gh_authenticate_app(
                installation_id, entity_type=entity_type,
                entity_id=entity_id,
                entity_name=(gh_runner_target if entity_type == EntityType.ORGANIZATION else None),
                repo_full_name=(gh_runner_target if entity_type == EntityType.USER else None),
            )
        except gh.GitHubAPIError as e:
            logger.error("Failed to authenticate for installation_id=%s entity_type=%s gh_runner_target=%s: %s", installation_id, entity_type, gh_runner_target, e)
            continue

        for name, gh_runner in gh_runners.items():
            if not name.startswith(RUNNER_NAME_PREFIX):
                continue
            if name in workers_by_name and workers_by_name[name]["status"] in ("completed", "failed"):
                logging.info("Runner runner=%s has matching completed worker=%s", name, name)
                _delete_gh_runner(name, token, entity_type, gh_runner_target, gh_runner["id"])
            elif name not in workers_by_name:
                logging.info("Runner runner=%s is unknown", name)
                _delete_gh_runner(name, token, entity_type, gh_runner_target, gh_runner["id"])


def _sync_workers_state_phase_5_delete_terminal_pods(pods_by_name):
    """Phase 5 of sync_workers_state: delete completed|failed pods after the grace period.

    Pods in Succeeded/Failed phase are kept for POD_DELETE_GRACE_SECONDS so operators
    can still inspect them via ``kubectl logs``; once the grace window elapses they
    are deleted from the cluster.
    """
    now = datetime.datetime.now(datetime.timezone.utc)
    for pod_name, pod in pods_by_name.items():
        if pod.status.phase not in ("Succeeded", "Failed"):
            continue
        finished = k8s.get_pod_finished_at(pod) or pod.metadata.creation_timestamp
        if finished and finished.tzinfo is None:
            finished = finished.replace(tzinfo=datetime.timezone.utc)
        elapsed = (now - finished).total_seconds() if finished else float("inf")
        if elapsed < POD_DELETE_GRACE_SECONDS:
            continue
        try:
            k8s.delete_pod(pod)
        except Exception as e:
            logger.error("Failed to delete pod %s: %s", pod.metadata.name, e)


def sync_workers_state():
    """Reconcile worker state across Kubernetes, GitHub, and the workers table."""
    pods_by_name = {p.metadata.name: p for p in k8s.list_pods()}

    # GitHub runners registered to a given (installation_id, entity_type, entity_id, target).
    # Phase 3 populates this lazily; Phase 4 reads from it.
    gh_runners_by_target: dict[tuple, dict] = {}

    workers_by_name = {w["pod_name"]: w for w in db.get_workers_for_reconcile()}
    # 1. Orphan sweep — workers in `pending`/`running` with no matching k8s pod
    #    are marked completed.
    _sync_workers_state_phase_1_orphan_sweep(pods_by_name, workers_by_name)
    # 2. Pod phase sync — k8s Running/Succeeded/Failed phases propagate to the
    #    workers table (setting running_at / completed_at / failure_info).
    _sync_workers_state_phase_2_pod_phase_sync(pods_by_name, workers_by_name)

    workers_by_name = {w["pod_name"]: w for w in db.get_workers_for_reconcile()} # refresh workers
    # 3. Health checks — for pending/running workers grouped by GitHub runner
    #    scope: if a Running pod older than RUNNER_REGISTRATION_TIMEOUT_SECONDS
    #    is still missing from GH, or a Pending pod is older than
    #    POD_PENDING_TIMEOUT_SECONDS, kill the pod (activeDeadlineSeconds=1)
    #    so it transitions to Failed; the worker is marked failed with
    #    diagnostics. If GitHub refuses to delete the runner (e.g. 422 "busy"),
    #    abort cleanup for that worker — it may genuinely be running a job.
    _sync_workers_state_phase_3_health_checks(pods_by_name, workers_by_name, gh_runners_by_target)

    workers_by_name = {w["pod_name"]: w for w in db.get_workers_for_reconcile()} # refresh workers
    # 4. GitHub-side cleanup — any runner matching RUNNER_NAME_PREFIX whose
    #    worker row is terminal or missing gets deleted on GitHub.
    _sync_workers_state_phase_4_gh_cleanup(workers_by_name, gh_runners_by_target)
    # 5. Delete k8s pods in Succeeded/Failed phase after POD_DELETE_GRACE_SECONDS
    #    have elapsed since container termination, so operators can still
    #    `kubectl logs` them during the grace window.
    _sync_workers_state_phase_5_delete_terminal_pods(pods_by_name)



def demand_match():
    """
    Match demand (pending jobs) with supply (k8s workers).

    Iterates pending jobs in FIFO order. For each job, checks:
    1. Pool demand vs supply — skip if demand already met
    2. Org max_workers cap — skip if org is at capacity
    3. K8s node capacity — skip if no available slot
    Then provisions a runner.
    """
    pending_jobs = db.get_pending_jobs()
    if not pending_jobs:
        logger.debug("No pending jobs to process")
        return

    logger.info("Processing %d pending jobs: [%s]", len(pending_jobs), ', '.join([str(j["job_id"]) for j in pending_jobs]))

    jobs_by_pool = _group_by(pending_jobs, key=lambda j: j["k8s_pool"])

    for k8s_pool, jobs in jobs_by_pool:
        available_slots = k8s.get_available_slots(label_selector=f"riseproject.dev/board={k8s_pool}")
        logger.info("Capacity for k8s_pool=%s available_slots=%s", k8s_pool, available_slots)
        if available_slots <= 0:
            continue

        for job in jobs:
            assert available_slots >= 1

            job_id = job["job_id"]
            if job.get("status") != "pending":
                logger.info("Job %s status is %s, not pending, skipping", job_id, job.get("status"))
                continue

            k8s_pool = job["k8s_pool"]
            k8s_image = job["k8s_image"]
            installation_id = job["installation_id"]
            entity_name = job["entity_name"]
            labels = job["job_labels"]
            entity_type = EntityType(job["entity_type"])
            entity_id = job["entity_id"]
            repo_full_name = job["repo_full_name"]
            provider = job["provider"]

            # Check demand vs supply, matched by entity_id + job_labels
            job_count, worker_count = db.get_pool_demand(entity_id, labels)
            if job_count <= worker_count:
                logger.info("Demand met for entity=%s entity_id=%s entity_type=%s labels=%s, jobs_count=%d workers_count=%d",
                            entity_name, entity_id, entity_type, labels, job_count, worker_count)
                continue

            # Check max_workers cap
            entity_config = ENTITY_CONFIG.get(int(entity_id), {"max_workers": 20})
            max_workers = entity_config.get("max_workers")
            if max_workers is not None:
                entity_worker_count = db.get_total_workers_for_entity(entity_id)
                if entity_worker_count >= max_workers:
                    logger.info("Max workers allocated for entity=%s entity_id=%s entity_type=%s labels=%s workers_count=%d max_workers=%d)",
                                entity_name, entity_id, entity_type, labels, entity_worker_count, max_workers)
                    continue

            # Reserve name in DB first — detects collision before creating k8s pod
            runner_name = None
            for _ in range(5):  # max retries for name collision
                suffix = ''.join(random.choices(string.ascii_lowercase + string.digits, k=9))
                candidate = f"{RUNNER_NAME_PREFIX}{suffix}"
                try:
                    db.add_worker(provider, entity_id, entity_name, entity_type.value,
                                  installation_id,
                                  repo_full_name if entity_type == EntityType.USER else None,
                                  k8s_pool, candidate,
                                  job_labels=labels, k8s_image=k8s_image)
                    runner_name = candidate
                    break
                except DuplicateRunnerNameException:
                    logger.warning("Runner name %s collision, regenerating", candidate)
                    continue

            if runner_name is None:
                logger.error("Failed to generate unique runner name for entity=%s entity_id=%s entity_type=%s pool=%s after retries", entity_name, entity_id, entity_type, k8s_pool)
                continue

            # Name reserved in DB, now safe to provision
            try:
                token = _gh_authenticate_app(
                    int(installation_id), entity_type=entity_type,
                    entity_id=entity_id,
                    entity_name=entity_name,
                    repo_full_name=repo_full_name,
                )

                if entity_type == EntityType.ORGANIZATION:
                    group_id = gh.ensure_runner_group(entity_name, token, RUNNER_GROUP_NAME)
                    jit_config = gh.create_jit_runner_config_org(token, group_id, labels, entity_name, runner_name)
                else:
                    jit_config = gh.create_jit_runner_config_repo(token, labels, repo_full_name, runner_name)

                k8s.provision_runner(jit_config, runner_name, k8s_image, k8s_pool, entity_id, entity_name)

                logger.info("Provisioned runner %s for entity=%s entity_id=%s entity_type=%s pool=%s", runner_name, entity_name, entity_id, entity_type, k8s_pool)

            except Exception as e:
                logger.error("Failed to provision runner %s for entity=%s entity_id=%s entity_type=%s pool=%s, error: %s", runner_name, entity_name, entity_id, entity_type, k8s_pool, str(e))
                failure_info = {
                    "version": 2, # bump when the structure changes
                    "reason": FailureReason.POD_ALLOCATION_FAILURE.value,
                    "containers": {},
                    "events": [],
                    "pod_message": None,
                    "pod_reason": None,
                }
                db.mark_worker_failed(runner_name, k8s_node=None, failure_info=failure_info, completed_at=None)

            available_slots -= 1
            if available_slots == 0:
                logger.debug("Capacity for k8s_pool=%s is now 0", k8s_pool)
                break


# --- HTTP Handlers ---

@app.route("/health", methods=['GET'])
def health():
    return "ok"


_STATUS_COLORS = {"pending": "#ccc504", "running": "#2563eb", "completed": "#16a34a", "failed": "#d90606"}

def _format_status(status):
    color = _STATUS_COLORS.get(status, "#666")
    return f'<span style="color:{color}">[{status:9s}]</span>'

def _format_labels(job_labels):
    """Format job_labels for display. Handles both list and JSON string."""
    if isinstance(job_labels, str):
        labels = json.loads(job_labels)
    else:
        labels = job_labels or []
    return ('[' + ", ".join(labels) + ']') if labels else "<none>"


def _format_timestamp(created_at):
    """Format a created_at value (datetime or unix float string) for display."""
    if not created_at:
        return "?"
    if isinstance(created_at, datetime.datetime):
        return created_at.strftime("%Y-%m-%d %H:%M:%S UTC")
    return datetime.datetime.fromtimestamp(float(created_at), tz=datetime.timezone.utc).strftime("%Y-%m-%d %H:%M:%S UTC")


def render_job(job) -> str:
    status = _format_status(job["status"])
    job_id = job["job_id"]
    repo = job["repo_full_name"]
    html_url = job["html_url"]
    labels = _format_labels(job["job_labels"])
    pod = job["k8s_pod"] or "<unknown pod>"
    created_str = _format_timestamp(job.get("created_at"))
    link = f'<a href="{html_url}">{repo}#{job_id}</a>' if html_url else f"{repo}#{job_id}"
    return f'{status}  {created_str}  {labels}  {pod}  {link}'


def render_worker(worker) -> list[str]:
    status = _format_status(worker["status"])
    created_str = _format_timestamp(worker['created_at'])
    labels = _format_labels(worker['job_labels'])
    pod = worker['pod_name']
    node = worker['k8s_node'] or '<unknown node>'
    lines = [f'{status}  {created_str}  {labels}  {pod}  (node: {node})']
    if worker["status"] == "failed" and worker["failure_info"]:
        failure_info = worker["failure_info"]
        version = failure_info.get("version", 1)
        if version == 1:
            pass
        else:
            if failure_info.get("reason"):
                lines.append(f"  Reason: {failure_info['reason']}")
        pod_reason = failure_info.get("pod_reason")
        pod_message = failure_info.get("pod_message")
        if pod_reason or pod_message:
            lines.append(f"  Pod: {pod_reason or '?'}  {pod_message or ''}".rstrip())
        for name, container in (failure_info.get("containers") or {}).items():
            exit_code = container.get("exit_code")
            c_reason = container.get("reason") or "?"
            c_message = container.get("message") or ""
            lines.append(f"  Container {name}: exit={exit_code}  {c_reason}  {c_message}".rstrip())
            logs = container.get("logs")
            if logs:
                for log_line in logs.splitlines():
                    lines.append(f"    | {log_line}")
        for ev in failure_info.get("events") or []:
            ts = ev.get("last_seen") or ev.get("first_seen") or "unknown"
            lines.append(f"  {ts}  [{ev['type']}]  {ev['reason']}: {ev['message']}")
    else:
        try:
            events = k8s.get_pod_events(worker["pod_name"])
            if events:
                for ev in events:
                    ts = ev.last_timestamp or ev.event_time or ev.metadata.creation_timestamp
                    ts_str = ts.strftime("%Y-%m-%d %H:%M:%S") if ts else "unknown"
                    lines.append(f"  {ts_str}  [{ev.type}]  {ev.reason}: {ev.message}")
            else:
                lines.append(f"  Events: (none)")
        except Exception:
            lines.append(f"  Events: (error fetching)")

    return lines

def _wants_json():
    return request.path.endswith('.json') or request.accept_mimetypes.best == 'application/json'


def _json_response(data):
    return make_response(json_dumps(data, default=str), 200, {"Content-Type": "application/json"})


@app.route("/usage", methods=['GET'])
@app.route("/usage.json", methods=['GET'])
def usage():
    active_jobs, active_workers = db.get_active_jobs_and_workers()

    if _wants_json():
        return _json_response({"jobs": active_jobs, "workers": active_workers})

    # HTML: group by (entity_name, job_labels) for display
    groups = {}
    for job in active_jobs:
        labels_key = json.dumps(job["job_labels"])
        key = (job["entity_id"], labels_key)
        if key not in groups:
            groups[key] = {"entity_name": job["entity_name"], "k8s_pool": job["k8s_pool"], "jobs": [], "workers": []}
        groups[key]["jobs"].append(job)

    for worker in active_workers:
        labels_key = json.dumps(worker["job_labels"])
        key = (worker["entity_id"], labels_key)
        if key not in groups:
            groups[key] = {"entity_name": worker["entity_name"], "k8s_pool": worker["k8s_pool"], "jobs": [], "workers": []}
        groups[key]["workers"].append(worker)

    lines = []
    for (_, labels_key), group in sorted(groups.items()):
        labels_display = _format_labels(labels_key)
        lines.append(f"=== {group['entity_name']} / {labels_display} ({group['k8s_pool']}) ===")
        if group["jobs"]:
            lines.append(f"  Jobs ({len(group['jobs'])}):")
            for job in sorted(group["jobs"], key=lambda j: j["created_at"]):
                lines.append(f'    - {render_job(job)}')
        else:
            lines.append("  Jobs: none")
        if group["workers"]:
            lines.append(f"  Workers ({len(group['workers'])}):")
            for worker in sorted(group["workers"], key=lambda w: w["created_at"]):
                lines.append(f'    - {'\n      '.join(render_worker(worker))}')
        else:
            lines.append("  Workers: none")
        lines.append("")
    if not lines:
        lines.append("No active pools.")
    return make_response(f"<title>{'Usage - Prod' if PROD else 'Usage - Staging'}</title><pre>{chr(10).join(lines)}</pre>", 200, {"Content-Type": "text/html"})


def _parse_date_param(value: str | None) -> str | None:
    """Parse a date parameter. Supports ISO dates (YYYY-MM-DD) and relative (-Xd for X days ago)."""
    if not value:
        return None
    import re
    m = re.match(r'^-(\d+)d$', value)
    if m:
        days_ago = int(m.group(1))
        return (datetime.date.today() - datetime.timedelta(days=days_ago)).isoformat()
    return value


def _build_link_header(base_url: str, page: int, per_page: int, total: int,
                       extra_params: dict[str, str] | None = None) -> str:
    """Build a Link header for pagination, matching GitHub API format.

    See: https://docs.github.com/en/rest/using-the-rest-api/using-pagination-in-the-rest-api
    """
    last_page = max(0, (total - 1) // per_page)

    def _url(p: int) -> str:
        params = f"page={p}&per_page={per_page}"
        if extra_params:
            for k, v in extra_params.items():
                params += f"&{k}={v}"
        return f"{base_url}?{params}"

    links = []
    if page > 0:
        links.append(f'<{_url(0)}>; rel="first"')
        links.append(f'<{_url(page - 1)}>; rel="prev"')
    if page < last_page:
        links.append(f'<{_url(page + 1)}>; rel="next"')
        links.append(f'<{_url(last_page)}>; rel="last"')
    return ", ".join(links)


@app.route("/history", methods=['GET'])
@app.route("/history.json", methods=['GET'])
@app.route("/jobs", methods=['GET'])
@app.route("/jobs.json", methods=['GET'])
def jobs():
    start = _parse_date_param(request.args.get("start"))
    end = _parse_date_param(request.args.get("end"))
    page = request.args.get("page", 0, type=int)
    per_page = request.args.get("per_page", 100, type=int)

    if start is not None:
        try:
            datetime.date.fromisoformat(start)
        except:
            return make_response('invalid parameter start, must be YYYY-MM-DD', 400)
    if end is not None:
        try:
            datetime.date.fromisoformat(end)
        except:
            return make_response('invalid parameter end, must be YYYY-MM-DD', 400)
    if page < 0:
        return make_response('invalid parameter page, must be >= 0', 400)
    if per_page <= 0:
        return make_response('invalid parameter per_page, must be > 0', 400)

    jobs, total = db.get_all_jobs(start=start, end=end, page=page, per_page=per_page)

    if _wants_json():
        resp = _json_response(jobs)
        extra = {}
        if start:
            extra["start"] = start
        if end:
            extra["end"] = end
        link = _build_link_header(request.base_url.split('?')[0], page, per_page, total, extra)
        if link:
            resp.headers["link"] = link
        return resp

    # HTML
    lines = []
    for job in jobs:
        lines.append(render_job(job))
    if not lines:
        lines = ["No jobs found."]

    return make_response(f"<title>{'History - Prod' if PROD else 'History - Staging'}</title><pre>{chr(10).join(lines)}</pre>", 200, {"Content-Type": "text/html"})


@app.route("/workers", methods=['GET'])
@app.route("/workers.json", methods=['GET'])
def workers():
    start = _parse_date_param(request.args.get("start"))
    end = _parse_date_param(request.args.get("end"))
    page = request.args.get("page", 0, type=int)
    per_page = request.args.get("per_page", 100, type=int)

    if start is not None:
        try:
            datetime.date.fromisoformat(start)
        except:
            return make_response('invalid parameter start, must be YYYY-MM-DD', 400)
    if end is not None:
        try:
            datetime.date.fromisoformat(end)
        except:
            return make_response('invalid parameter end, must be YYYY-MM-DD', 400)
    if page < 0:
        return make_response('invalid parameter page, must be >= 0', 400)
    if per_page <= 0:
        return make_response('invalid parameter per_page, must be > 0', 400)

    workers, total = db.get_all_workers(start=start, end=end, page=page, per_page=per_page)

    if _wants_json():
        resp = _json_response(workers)
        extra = {}
        if start:
            extra["start"] = start
        if end:
            extra["end"] = end
        link = _build_link_header(request.base_url.split('?')[0], page, per_page, total, extra)
        if link:
            resp.headers["link"] = link
        return resp

    # HTML
    lines = []
    for worker in workers:
        lines.extend(render_worker(worker))
    if not lines:
        lines = ["No workers found."]

    return make_response(f"<title>{'Workers - Prod' if PROD else 'Workers - Staging'}</title><pre>{chr(10).join(lines)}</pre>", 200, {"Content-Type": "text/html"})


def _scheduler_iteration():
    # Serialize demand matching across scheduler containers: hold one DB connection
    # for the full schduler and lock the workers table exclusively. Thread-local
    # caching in db.py ensures all nested db calls reuse this connection
    # so they respect the lock without self-deadlocking on the pool.
    with db.hold_connection() as conn:
        with conn.cursor() as cur:
            cur.execute("LOCK TABLE workers IN EXCLUSIVE MODE")

        sync_jobs_state()
        sync_workers_state()
        demand_match()


if __name__ == "__main__":
    # Set the logging level for all loggers to INFO
    logging.basicConfig(
        level=logging.getLevelNamesMapping()[os.environ.get("LOGLEVEL", "INFO")],
        format='%(pathname)s:%(lineno)d::%(funcName)s: [%(levelname)s] %(message)s',
        datefmt='%Y-%m-%d %H:%M:%S'
    )

    # Ensure PostgreSQL schema/tables exist
    db.ensure_schema()

    def http_worker():
        from waitress import serve

        HOST = "0.0.0.0"
        PORT = 8080

        print(f"Starting server on http://{HOST}:{PORT}")
        serve(app, host=HOST, port=PORT)

    http_thread = threading.Thread(target=http_worker, daemon=True)
    http_thread.start()

    while True:
        try:
            _scheduler_iteration()
        except Exception as e:
            logger.error("Scheduler error: %s\n%s", e, traceback.format_exc())

        db.wait_for_job(POLL_INTERVAL)
