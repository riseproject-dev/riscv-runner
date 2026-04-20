from __future__ import annotations

import contextlib
import json
import logging
import select
import time
import threading
from typing import Any, Iterator

import psycopg2
import psycopg2.extras
from psycopg2.pool import ThreadedConnectionPool

from constants import POSTGRES_URL, POSTGRES_SCHEMA, POSTGRES_MAXCONN

logger = logging.getLogger(__name__)


class DuplicateRunnerNameException(Exception):
    """Raised when add_worker() detects a pod_name collision."""
    pass


# --- Connection management ---
# PostgreSQL connections are 1-query-at-a-time and NOT thread-safe.
# Waitress serves webhooks with 4+ threads, so each needs its own connection.
# ThreadedConnectionPool: minconn=1, maxconn=POSTGRES_MAXCONN. Threads borrow/return connections.
#
# A semaphore gates access so threads block (instead of crashing with PoolError)
# when all connections are in use.
#
# Thread-local caching: `hold_connection()` pins one connection to the current
# thread so nested `_get_conn()` calls share the same transaction — lets
# `sync_workers_state` take `LOCK TABLE` once and have all subsequent
# `mark_worker_*` calls respect it on the same connection.

_pool: ThreadedConnectionPool | None = None
_pool_semaphore: threading.Semaphore | None = None
_pool_lock = threading.Lock()
_thread_local = threading.local()


def _init_pool() -> ThreadedConnectionPool:
    global _pool, _pool_semaphore
    if _pool is not None:
        return _pool
    with _pool_lock:
        if _pool is not None:
            return _pool
        _pool = ThreadedConnectionPool(
            minconn=1,
            maxconn=POSTGRES_MAXCONN,
            dsn=POSTGRES_URL,
        )
        _pool_semaphore = threading.Semaphore(POSTGRES_MAXCONN)
        return _pool


class _PoolConnection:
    """Context manager that borrows a connection from the pool and returns it.

    - Acquires a semaphore slot before borrowing (blocks if pool is full).
    - Sets search_path on every borrowed connection.
    - Auto-commits on clean exit, auto-rollbacks on exception.
    - Releases the semaphore slot after returning the connection.

    If the current thread has pinned a connection via `hold_connection()`, this
    short-circuits: no new connection is borrowed, no commit/rollback happens on
    exit (the outer `hold_connection` owns the lifecycle), and exceptions still
    propagate so the outer block rolls back.
    """
    def __init__(self) -> None:
        self.conn = None
        self._held = False

    def __enter__(self):
        held = getattr(_thread_local, "conn", None)
        if held is not None:
            self.conn = held
            self._held = True
            return self.conn
        pool = _init_pool()
        _pool_semaphore.acquire()
        try:
            self.conn = pool.getconn()
            with self.conn.cursor() as cur:
                cur.execute(f"SET search_path TO {POSTGRES_SCHEMA}")
        except Exception:
            if self.conn is not None:
                pool.putconn(self.conn)
                self.conn = None
            _pool_semaphore.release()
            raise
        return self.conn

    def __exit__(self, exc_type, exc_val, exc_tb):
        if self._held:
            return False
        if self.conn is not None:
            if exc_type is not None:
                self.conn.rollback()
            else:
                self.conn.commit()
            _init_pool().putconn(self.conn)
            self.conn = None
        _pool_semaphore.release()
        return False


def _get_conn() -> _PoolConnection:
    return _PoolConnection()


@contextlib.contextmanager
def hold_connection():
    """Pin one pool connection to the current thread for the duration of the block.

    Nested `_get_conn()` calls in the same thread reuse this connection so all
    operations share a single transaction. COMMIT on clean exit, ROLLBACK on
    exception. Used by `sync_workers_state` to hold `LOCK TABLE workers IN
    EXCLUSIVE MODE` across all the `mark_worker_*` calls it makes.
    """
    assert getattr(_thread_local, "conn", None) is None, "held connection already active"
    pool = _init_pool()
    _pool_semaphore.acquire()
    conn = None
    try:
        conn = pool.getconn()
        with conn.cursor() as cur:
            cur.execute(f"SET search_path TO {POSTGRES_SCHEMA}")
        _thread_local.conn = conn
        try:
            yield conn
            conn.commit()
        except Exception:
            conn.rollback()
            raise
    finally:
        _thread_local.conn = None
        if conn is not None:
            pool.putconn(conn)
        _pool_semaphore.release()


# --- Schema bootstrap ---

def ensure_schema() -> None:
    """Create schema, enum type, tables, and indexes if they don't exist. Idempotent.

    Uses a direct connection (not the pool context manager) because DDL
    requires autocommit=True, which must be set before any statement runs.
    The pool context manager runs SET search_path on enter, which starts a
    transaction and prevents setting autocommit afterwards.
    """
    pool = _init_pool()
    _pool_semaphore.acquire()
    conn = pool.getconn()
    try:
        conn.autocommit = True
        with conn.cursor() as cur:
            cur.execute(f"CREATE SCHEMA IF NOT EXISTS {POSTGRES_SCHEMA}")
            cur.execute(f"SET search_path TO {POSTGRES_SCHEMA}")

            # Create enum types (idempotent via DO blocks)
            cur.execute("""
                DO $$ BEGIN
                    CREATE TYPE status_enum AS ENUM ('pending', 'running', 'completed', 'failed');
                EXCEPTION
                    WHEN duplicate_object THEN null;
                END $$
            """)
            cur.execute("""
                DO $$ BEGIN
                    CREATE TYPE provider_enum AS ENUM ('github', 'gitlab', 'azdo');
                EXCEPTION
                    WHEN duplicate_object THEN null;
                END $$
            """)

            # Jobs table
            cur.execute("""
                CREATE TABLE IF NOT EXISTS jobs (
                    job_id          BIGINT PRIMARY KEY,
                    status          status_enum NOT NULL DEFAULT 'pending',
                    failure_info    JSONB,
                    provider        provider_enum NOT NULL,
                    entity_id       BIGINT NOT NULL,
                    entity_name     TEXT NOT NULL,
                    entity_type     TEXT NOT NULL,
                    repo_full_name  TEXT NOT NULL,
                    installation_id BIGINT NOT NULL,
                    job_labels      JSONB NOT NULL DEFAULT '[]',
                    k8s_pool        TEXT NOT NULL,
                    k8s_image       TEXT NOT NULL,
                    html_url        TEXT,
                    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
                    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
                )
            """)

            # Workers table
            cur.execute("""
                CREATE TABLE IF NOT EXISTS workers (
                    pod_name        TEXT PRIMARY KEY,
                    provider        provider_enum NOT NULL,
                    entity_id       BIGINT NOT NULL,
                    entity_name     TEXT NOT NULL,
                    entity_type     TEXT NOT NULL,
                    installation_id BIGINT NOT NULL,
                    repo_full_name  TEXT,
                    job_labels      JSONB NOT NULL DEFAULT '[]',
                    k8s_pool        TEXT NOT NULL,
                    k8s_image       TEXT NOT NULL,
                    k8s_node        TEXT,
                    status          status_enum NOT NULL DEFAULT 'pending',
                    failure_info    JSONB,
                    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
                    running_at      TIMESTAMPTZ,
                    completed_at    TIMESTAMPTZ,
                    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
                )
            """)

            # Indexes (IF NOT EXISTS for idempotency)
            cur.execute("""
                CREATE INDEX IF NOT EXISTS idx_jobs_active
                ON jobs (entity_id, job_labels, created_at)
                WHERE status != 'completed'
            """)
            cur.execute("""
                CREATE INDEX IF NOT EXISTS idx_jobs_reconcile
                ON jobs (installation_id)
                WHERE status != 'completed'
            """)
            cur.execute("""
                CREATE INDEX IF NOT EXISTS idx_jobs_created
                ON jobs (created_at DESC)
            """)
            cur.execute("""
                CREATE INDEX IF NOT EXISTS idx_workers_active
                ON workers (entity_id, job_labels, k8s_pool)
                WHERE status != 'completed'
            """)

        conn.autocommit = False
    finally:
        pool.putconn(conn)
        _pool_semaphore.release()
    logger.info("Schema '%s' ensured (tables + indexes)", POSTGRES_SCHEMA)


# --- Job operations ---

def add_job(job_id: int, provider: str, entity_id: int, entity_name: str, entity_type: str | Any,
              repo_full_name: str, installation_id: int, labels: list[str],
              k8s_pool: str, k8s_image: str, html_url: str) -> bool:
    """Store a new job. Returns True if created, False if duplicate."""
    sorted_labels = json.dumps(sorted(labels))
    entity_type_val = entity_type.value if hasattr(entity_type, 'value') else str(entity_type)
    now = time.time()

    with _get_conn() as conn:
        with conn.cursor() as cur:
            cur.execute("""
                INSERT INTO jobs (job_id, status, provider, entity_id, entity_name, entity_type,
                                  repo_full_name, installation_id, job_labels, k8s_pool,
                                  k8s_image, html_url, created_at, updated_at)
                VALUES (%s, 'pending', %s, %s, %s, %s, %s, %s, %s, %s, %s, %s,
                        to_timestamp(%s), to_timestamp(%s))
                ON CONFLICT (job_id) DO NOTHING
            """, (int(job_id), provider, int(entity_id), entity_name, entity_type_val,
                  repo_full_name, int(installation_id), sorted_labels, k8s_pool,
                  k8s_image, html_url, now, now))
            created = cur.rowcount > 0

            if created:
                cur.execute(f"NOTIFY {POSTGRES_SCHEMA}_queue_event, %s", (str(job_id),))

    if created:
        logger.info("Stored job %s for entity %s pool %s", job_id, entity_name, k8s_pool)
    else:
        logger.debug("Job %s already exists, skipping", job_id)
    return created


def mark_job_running(job_id: int) -> str | None:
    """Update job status to running. Returns previous status string or None.

    Only allows the transition: pending -> running.
    """
    with _get_conn() as conn:
        with conn.cursor() as cur:
            cur.execute("""
                WITH prev AS (SELECT status FROM jobs WHERE job_id = %s)
                UPDATE jobs SET status = 'running', updated_at = now()
                WHERE job_id = %s AND status = 'pending'
                RETURNING (SELECT status::text FROM prev) as prev_status
            """, (int(job_id), int(job_id)))
            row = cur.fetchone()

            if row is not None:
                logger.info("Job %s status updated to running (was %s)", job_id, row[0])
                return row[0]

            # UPDATE didn't match — either job doesn't exist or is already running/completed
            cur.execute("SELECT status::text FROM jobs WHERE job_id = %s", (int(job_id),))
            existing = cur.fetchone()
            if existing is None:
                logger.debug("Job %s not found in PostgreSQL", job_id)
                return None
            logger.debug("Job %s not updated to running (current status: %s)", job_id, existing[0])
            return existing[0]


def mark_job_completed(job_id: int) -> str | None:
    """Update job status to completed. Returns previous status string or None.

    Allows transitions: pending|running -> completed.
    """
    with _get_conn() as conn:
        with conn.cursor() as cur:
            cur.execute("""
                WITH prev AS (SELECT status FROM jobs WHERE job_id = %s)
                UPDATE jobs SET status = 'completed', updated_at = now()
                WHERE job_id = %s AND (status = 'pending' OR status = 'running')
                RETURNING (SELECT status::text FROM prev) as prev_status
            """, (int(job_id), int(job_id)))
            row = cur.fetchone()

            if row is not None:
                logger.info("Job %s status updated to completed (was %s)", job_id, row[0])
                return row[0]

            # UPDATE didn't match — either job doesn't exist or is already completed
            cur.execute("SELECT status::text FROM jobs WHERE job_id = %s", (int(job_id),))
            existing = cur.fetchone()
            if existing is None:
                logger.debug("Job %s not found in PostgreSQL", job_id)
                return None
            return existing[0]


def mark_job_failed(job_id: int, failure_info: dict) -> str | None:
    """Update job status to failed. Returns previous status string or None.

    Allows transitions: pending|running -> failed.
    """
    assert "version" in failure_info and isinstance(failure_info['version'], int), f"failure_info must have a failure_info['version'] parameter and it must be an int"
    with _get_conn() as conn:
        with conn.cursor() as cur:
            cur.execute("""
                WITH prev AS (SELECT status FROM jobs WHERE job_id = %s)
                UPDATE jobs SET status = 'failed', failure_info = %s, updated_at = now()
                WHERE job_id = %s AND (status = 'pending' OR status = 'running')
                RETURNING (SELECT status::text FROM prev) as prev_status
            """, (int(job_id), json.dumps(failure_info), int(job_id)))
            row = cur.fetchone()

            if row is not None:
                logger.info("Job %s status updated to completed (was %s)", job_id, row[0])
                return row[0]

            # UPDATE didn't match — either job doesn't exist or is already completed
            cur.execute("SELECT status::text FROM jobs WHERE job_id = %s", (int(job_id),))
            existing = cur.fetchone()
            if existing is None:
                logger.debug("Job %s not found in PostgreSQL", job_id)
                return None
            return existing[0]


# --- Worker operations ---

def get_pool_demand(entity_id: int, job_labels: list[str]) -> tuple[int, int]:
    """Return (job_count, worker_count) for an entity + label set.

    Matches demand and supply by (entity_id, job_labels) rather than (entity_id, k8s_pool).
    This fixes the bug where different label sets mapping to the same pool cause stuck workers.
    Labels are sorted internally for consistent JSONB equality.
    """
    sorted_labels = json.dumps(sorted(job_labels))
    with _get_conn() as conn:
        with conn.cursor() as cur:
            cur.execute("""
                SELECT
                    (SELECT COUNT(*) FROM jobs
                     WHERE entity_id = %s AND job_labels = %s
                       AND (status = 'pending' OR status = 'running')) as job_count,
                    (SELECT COUNT(*) FROM workers
                     WHERE entity_id = %s AND job_labels = %s
                       AND (status = 'pending' OR status = 'running')) as worker_count
            """, (int(entity_id), sorted_labels, int(entity_id), sorted_labels))
            row = cur.fetchone()
    return row[0], row[1]


def get_total_workers_for_entity(entity_id: int) -> int:
    """Return total worker count across all pools for an entity."""
    with _get_conn() as conn:
        with conn.cursor() as cur:
            cur.execute("""
                SELECT COUNT(*) FROM workers
                WHERE entity_id = %s AND (status = 'pending' OR status = 'running')
            """, (int(entity_id),))
            row = cur.fetchone()
    return row[0]


def get_pending_jobs() -> list[dict]:
    """Return all pending jobs in FIFO order as full row dicts.

    Consumers (demand_match) read fields via `job["job_id"]`, `job["entity_id"]`,
    etc., so we return RealDictCursor rows — not raw tuples.
    """
    with _get_conn() as conn:
        with conn.cursor(cursor_factory=psycopg2.extras.RealDictCursor) as cur:
            cur.execute("""
                SELECT *
                FROM jobs
                WHERE status = 'pending'
                ORDER BY created_at
            """)
            return cur.fetchall()


def add_worker(provider: str, entity_id: int, entity_name: str, entity_type: str,
               installation_id: int, repo_full_name: str | None, k8s_pool: str, pod_name: str,
               job_labels: list[str], k8s_image: str) -> None:
    """Add a worker. Raises DuplicateRunnerNameException on pod_name collision.

    `repo_full_name` is only meaningful for user-scoped runners (personal accounts,
    where runners are registered under a specific repo). Pass None for org-scoped runners.
    """
    sorted_labels = json.dumps(sorted(job_labels))

    with _get_conn() as conn:
        with conn.cursor() as cur:
            cur.execute("""
                INSERT INTO workers (pod_name, provider, entity_id, entity_name, entity_type,
                                     installation_id, repo_full_name, k8s_pool, job_labels,
                                     k8s_image, status, created_at, updated_at)
                VALUES (%s, %s, %s, %s, %s, %s, %s, %s, %s, %s, 'pending', now(), now())
                ON CONFLICT (pod_name) DO NOTHING
            """, (pod_name, provider, int(entity_id), entity_name, entity_type,
                  int(installation_id), repo_full_name, k8s_pool, sorted_labels, k8s_image))

            if cur.rowcount == 0:
                raise DuplicateRunnerNameException(
                    f"Worker pod_name '{pod_name}' already exists")

    logger.debug("Added worker %s to pool %s:%s", pod_name, entity_id, k8s_pool)


def mark_worker_running(pod_name: str, k8s_node: str, running_at: Any):
    with _get_conn() as conn:
        with conn.cursor() as cur:
            cur.execute("""
                UPDATE workers
                SET status = 'running',
                    k8s_node = %s,
                    running_at = COALESCE(running_at, %s, now()),
                    updated_at = now()
                WHERE pod_name = %s AND status = 'pending'
            """, (k8s_node, running_at, pod_name))
    logger.debug("Marked worker %s running", pod_name)


def mark_worker_completed(pod_name: str, k8s_node: str, completed_at: Any):
    with _get_conn() as conn:
        with conn.cursor() as cur:
            cur.execute("""
                UPDATE workers
                SET status = 'completed',
                    k8s_node = COALESCE(k8s_node, %s),
                    completed_at = COALESCE(completed_at, %s, now()),
                    updated_at = now()
                WHERE pod_name = %s AND (status = 'pending' OR status = 'running')
            """, (k8s_node, completed_at, pod_name))
    logger.debug("Marked worker %s completed", pod_name)


def mark_worker_failed(pod_name: str, k8s_node: str, failure_info: dict, completed_at: Any) -> None:
    """Mark a worker as failed with failure_info and completed_at.

    Allows transitions: pending -> failed, running -> failed.
    `completed_at` may be a datetime or None (DB now() fallback).
    """
    assert failure_info and "version" in failure_info and isinstance(failure_info["version"], int), \
        "failure_info must be a dict with an int 'version' field"
    with _get_conn() as conn:
        with conn.cursor() as cur:
            cur.execute("""
                UPDATE workers
                SET status = 'failed',
                    k8s_node = COALESCE(k8s_node, %s),
                    failure_info = %s,
                    completed_at = COALESCE(%s, now()),
                    updated_at = now()
                WHERE pod_name = %s AND (status = 'pending' OR status = 'running')
            """, (k8s_node, json.dumps(failure_info), completed_at, pod_name))
    logger.debug("Marked worker %s failed", pod_name)


def mark_worker_orphaned(pod_name: str):
    with _get_conn() as conn:
        with conn.cursor() as cur:
            cur.execute("""
                UPDATE workers
                SET status = 'completed',
                    completed_at = COALESCE(completed_at, now()),
                    updated_at = now()
                WHERE pod_name = %s AND (status = 'pending' OR status = 'running')
            """, (pod_name,))
    logger.debug("Marked worker %s orphaned", pod_name)


def get_active_jobs_and_workers() -> tuple[list[dict], list[dict]]:
    """Return (active_jobs, active_workers) as raw rows from PostgreSQL."""
    with _get_conn() as conn:
        with conn.cursor(cursor_factory=psycopg2.extras.RealDictCursor) as cur:
            cur.execute("""
                SELECT entity_id, entity_name, job_labels, k8s_pool, job_id,
                       status, repo_full_name, html_url, created_at
                FROM jobs WHERE status = 'pending' OR status = 'running'
                ORDER BY created_at
            """)
            jobs = cur.fetchall()

            cur.execute("""
                SELECT entity_id, entity_name, job_labels, k8s_pool, k8s_node, pod_name,
                       status, created_at
                FROM workers WHERE status = 'pending' OR status = 'running'
                ORDER BY created_at
            """)
            workers = cur.fetchall()

    return jobs, workers


def get_active_jobs() -> list[dict]:
    """Return active_jobs as raw rows from PostgreSQL."""
    with _get_conn() as conn:
        with conn.cursor(cursor_factory=psycopg2.extras.RealDictCursor) as cur:
            cur.execute("""
                SELECT *
                FROM jobs WHERE status = 'pending' OR status = 'running'
                ORDER BY created_at
            """)
            return cur.fetchall()


def get_active_workers() -> list[dict]:
    """Return active_workers as raw rows from PostgreSQL."""
    with _get_conn() as conn:
        with conn.cursor(cursor_factory=psycopg2.extras.RealDictCursor) as cur:
            cur.execute("""
                SELECT *
                FROM workers WHERE status = 'pending' OR status = 'running'
                ORDER BY created_at
            """)
            return cur.fetchall()


def get_all_jobs(start: str | None = None, end: str | None = None,
                 page: int = 0, per_page: int = 100) -> tuple[list[dict[str, str]], int]:
    """Return (jobs, total_count) with optional date filtering and paging.

    Args:
        start: ISO date string (YYYY-MM-DD). Only jobs created on or after this date.
        end: ISO date string (YYYY-MM-DD). Only jobs created before this date.
        page: Page number (0-indexed).
        per_page: Number of jobs per page.

    Returns:
        Tuple of (list of job dicts, total matching count for pagination).
    """
    with _get_conn() as conn:
        with conn.cursor(cursor_factory=psycopg2.extras.RealDictCursor) as cur:
            conditions = []
            params: list = []
            if start:
                conditions.append("created_at >= %s::timestamptz")
                params.append(start)
            if end:
                conditions.append("created_at < %s::timestamptz")
                params.append(end)
            where = "WHERE " + " AND ".join(conditions) if conditions else ""

            cur.execute(f"SELECT COUNT(*) AS total FROM jobs {where}", params)
            total = cur.fetchone()["total"]

            page_params = params + [per_page, page * per_page]
            cur.execute(f"""
                SELECT * FROM jobs {where}
                ORDER BY created_at DESC
                LIMIT %s OFFSET %s
            """, page_params)
            rows = cur.fetchall()
    return [_job_row_to_dict(row) for row in rows], total


def get_workers_for_reconcile(terminal_lookback_seconds: int = 3600) -> list[dict]:
    """Return all active workers plus recently-terminal workers for reconciliation.

    Active = pending/running. Terminal = completed/failed within the lookback window.
    Terminal rows are included so sync_workers_state can delete their GitHub counterparts.
    """
    with _get_conn() as conn:
        with conn.cursor(cursor_factory=psycopg2.extras.RealDictCursor) as cur:
            cur.execute("""
                SELECT *
                FROM workers
                WHERE status IN ('pending', 'running')
                   OR (status IN ('completed', 'failed')
                       AND completed_at IS NOT NULL
                       AND completed_at > now() - (%s || ' seconds')::interval)
            """, (int(terminal_lookback_seconds),))
            return cur.fetchall()


# --- Pub/Sub ---

_listen_conn = None
_listen_lock = threading.Lock()


def _get_listen_conn():
    """Get or create a dedicated AUTOCOMMIT connection for LISTEN/NOTIFY."""
    global _listen_conn
    if _listen_conn is not None and _listen_conn.closed == 0:
        return _listen_conn
    with _listen_lock:
        if _listen_conn is not None and _listen_conn.closed == 0:
            return _listen_conn
        _listen_conn = psycopg2.connect(POSTGRES_URL)
        _listen_conn.set_isolation_level(psycopg2.extensions.ISOLATION_LEVEL_AUTOCOMMIT)
        with _listen_conn.cursor() as cur:
            cur.execute(f"SET search_path TO {POSTGRES_SCHEMA}")
            cur.execute(f"LISTEN {POSTGRES_SCHEMA}_queue_event")
        return _listen_conn


def wait_for_job(timeout: int) -> None:
    """Block until a new job is published or timeout expires.

    Drains all buffered notifications after waking so the scheduler isn't
    woken again for events that arrived while it was processing.
    """
    assert timeout
    conn = _get_listen_conn()
    ready = select.select([conn], [], [], timeout)
    if ready[0]:
        conn.poll()
        if conn.notifies:
            logger.debug("Woken by PG queue event: %d notifications", len(conn.notifies))
    # Drain all buffered notifications
    conn.notifies.clear()


# --- Internal helpers ---

def _job_row_to_dict(row: dict[str, Any]) -> dict[str, str]:
    """Convert a PostgreSQL job row to a string-valued dict."""
    d = {}
    d["job_id"] = str(row["job_id"])
    d["status"] = str(row["status"])
    d["entity_id"] = str(row["entity_id"])
    d["entity_name"] = row["entity_name"] or ""
    d["entity_type"] = row["entity_type"] or ""
    d["repo_full_name"] = row["repo_full_name"] or ""
    d["installation_id"] = str(row["installation_id"]) if row["installation_id"] else ""
    d["job_labels"] = json.dumps(row["job_labels"]) if row["job_labels"] is not None else "[]"
    d["k8s_pool"] = row["k8s_pool"] or ""
    d["k8s_image"] = row["k8s_image"] or ""
    d["html_url"] = row["html_url"] or ""
    if row["created_at"]:
        d["created_at"] = str(row["created_at"].timestamp())
    else:
        d["created_at"] = ""
    return d
