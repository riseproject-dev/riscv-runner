from unittest.mock import patch, MagicMock, PropertyMock
import json
import threading

import pytest

from db import (
    add_job,
    mark_job_running,
    mark_job_completed,
    mark_job_failed,
    job_exists_for_pod,
    get_pool_demand,
    get_pending_jobs,
    add_worker,
    mark_worker_failed,
    hold_connection,
    _get_conn,
    DuplicateRunnerNameException,
)
from constants import EntityType


def make_mock_pool():
    """Create a mock connection pool, connection, and cursor.

    The _PoolConnection context manager calls _init_pool() to get the pool,
    acquires _pool_semaphore, then calls pool.getconn(). On exit it calls
    conn.commit() (clean) or conn.rollback() (exception), then pool.putconn().
    We mock at the pool level so the context manager drives commit/rollback.
    """
    pool = MagicMock()
    conn = MagicMock()
    cur = MagicMock()
    conn.cursor.return_value.__enter__ = MagicMock(return_value=cur)
    conn.cursor.return_value.__exit__ = MagicMock(return_value=False)
    pool.getconn.return_value = conn
    return pool, conn, cur


@pytest.fixture(autouse=True)
def mock_pool_and_semaphore():
    """Patch _pool_semaphore so _PoolConnection.__enter__/__exit__ don't block."""
    semaphore = threading.Semaphore(10)
    with patch("db._pool_semaphore", semaphore):
        yield


# --- add_job ---

@patch("db._init_pool")
def test_add_job_new(mock_pool_fn):
    pool, conn, cur = make_mock_pool()
    mock_pool_fn.return_value = pool
    cur.rowcount = 1  # inserted

    result = add_job(111, provider="github", entity_id=1000, entity_name="test-org",
                       entity_type=EntityType.ORGANIZATION,
                       repo_full_name="test-org/repo", installation_id=999,
                       labels=["rise"], k8s_pool="scw-em-rv1", k8s_image="img:latest",
                       html_url="https://example.com")

    assert result is True
    # INSERT + NOTIFY called (plus SET search_path)
    assert cur.execute.call_count >= 2


@patch("db._init_pool")
def test_add_job_duplicate(mock_pool_fn):
    pool, conn, cur = make_mock_pool()
    mock_pool_fn.return_value = pool
    cur.rowcount = 0  # not inserted (duplicate)

    result = add_job(111, provider="github", entity_id=1000, entity_name="test-org",
                       entity_type=EntityType.ORGANIZATION,
                       repo_full_name="test-org/repo", installation_id=999,
                       labels=["rise"], k8s_pool="scw-em-rv1", k8s_image="img:latest",
                       html_url="https://example.com")

    assert result is False


@patch("db._init_pool")
def test_add_job_sorts_labels(mock_pool_fn):
    pool, conn, cur = make_mock_pool()
    mock_pool_fn.return_value = pool
    cur.rowcount = 1

    add_job(111, provider="github", entity_id=1000, entity_name="test-org",
              entity_type=EntityType.ORGANIZATION,
              repo_full_name="test-org/repo", installation_id=999,
              labels=["z-label", "a-label"], k8s_pool="scw-em-rv1",
              k8s_image="img:latest", html_url="https://example.com")

    # Check that sorted labels were passed to the INSERT
    insert_call = cur.execute.call_args_list[1]  # second call is the INSERT
    args = insert_call[0][1]
    # job_labels is the 8th parameter (index 7) — provider was inserted before entity_id
    assert args[7] == '["a-label", "z-label"]'


# --- mark_job_running ---

@patch("db._init_pool")
def test_mark_job_running(mock_pool_fn):
    """Successful pending -> running transition returns old status via RETURNING old.status."""
    pool, conn, cur = make_mock_pool()
    mock_pool_fn.return_value = pool
    cur.fetchone.return_value = ("pending",)  # RETURNING old.status

    prev = mark_job_running(111, "my-runner")

    assert prev == "pending"


@patch("db._init_pool")
def test_mark_job_running_already_running(mock_pool_fn):
    """Job already running: UPDATE matches nothing, SELECT returns 'running'."""
    pool, conn, cur = make_mock_pool()
    mock_pool_fn.return_value = pool
    cur.fetchone.side_effect = [None, ("running",)]  # UPDATE no match, SELECT finds it

    prev = mark_job_running(111, "my-runner")

    assert prev == "running"


@patch("db._init_pool")
def test_mark_job_running_not_found(mock_pool_fn):
    pool, conn, cur = make_mock_pool()
    mock_pool_fn.return_value = pool
    cur.fetchone.side_effect = [None, None]  # UPDATE no match, SELECT no match

    prev = mark_job_running(111, "my-runner")

    assert prev is None


# --- mark_job_completed ---

@patch("db._init_pool")
def test_mark_job_completed_from_running(mock_pool_fn):
    """Successful running -> completed returns 'running' via RETURNING old.status."""
    pool, conn, cur = make_mock_pool()
    mock_pool_fn.return_value = pool
    cur.fetchone.return_value = ("running",)

    prev = mark_job_completed(111, "my-runner")

    assert prev == "running"


@patch("db._init_pool")
def test_mark_job_completed_from_pending(mock_pool_fn):
    """Successful pending -> completed returns 'pending' via RETURNING old.status."""
    pool, conn, cur = make_mock_pool()
    mock_pool_fn.return_value = pool
    cur.fetchone.return_value = ("pending",)

    prev = mark_job_completed(111, "my-runner")

    assert prev == "pending"


@patch("db._init_pool")
def test_mark_job_completed_already(mock_pool_fn):
    """Job already completed: UPDATE matches nothing, SELECT returns 'completed'."""
    pool, conn, cur = make_mock_pool()
    mock_pool_fn.return_value = pool
    cur.fetchone.side_effect = [None, ("completed",)]

    prev = mark_job_completed(111, "my-runner")

    assert prev == "completed"


@patch("db._init_pool")
def test_mark_job_completed_not_found(mock_pool_fn):
    pool, conn, cur = make_mock_pool()
    mock_pool_fn.return_value = pool
    cur.fetchone.side_effect = [None, None]

    prev = mark_job_completed(111, "my-runner")

    assert prev is None


@patch("db._init_pool")
def test_mark_job_completed_sets_k8s_pod(mock_pool_fn):
    """mark_job_completed should pass runner_name as the k8s_pod COALESCE arg."""
    pool, conn, cur = make_mock_pool()
    mock_pool_fn.return_value = pool
    cur.fetchone.return_value = ("running",)

    mark_job_completed(111, "rise-riscv-runner-abc")

    update_call = cur.execute.call_args_list[1]  # first call is SET search_path
    sql_params = update_call[0][1]
    assert sql_params == (111, "rise-riscv-runner-abc", 111)


# --- job_exists_for_pod ---

@patch("db._init_pool")
def test_job_exists_for_pod_true(mock_pool_fn):
    pool, conn, cur = make_mock_pool()
    mock_pool_fn.return_value = pool
    cur.fetchone.return_value = (1,)

    assert job_exists_for_pod("rise-riscv-runner-abc") is True


@patch("db._init_pool")
def test_job_exists_for_pod_false(mock_pool_fn):
    pool, conn, cur = make_mock_pool()
    mock_pool_fn.return_value = pool
    cur.fetchone.return_value = None

    assert job_exists_for_pod("rise-riscv-runner-abc") is False


# --- mark_job_failed ---

@patch("db._init_pool")
def test_mark_job_failed_serializes_failure_info_as_json(mock_pool_fn):
    """Successful pending -> failed passes failure_info as JSON string to SQL."""
    pool, conn, cur = make_mock_pool()
    mock_pool_fn.return_value = pool
    cur.fetchone.return_value = ("pending",)

    failure_info = {"version": 1, "message": "job not found"}
    prev = mark_job_failed(111, failure_info)

    assert prev == "pending"
    # Verify the second parameter to the SQL query is a JSON string, not a dict
    insert_call = cur.execute.call_args_list[1]  # second call is the UPDATE (first is SET search_path)
    sql_params = insert_call[0][1]
    assert isinstance(sql_params[1], str), "failure_info should be serialized as JSON string"
    assert json.loads(sql_params[1]) == failure_info


@patch("db._init_pool")
def test_mark_job_failed_requires_version_key(mock_pool_fn):
    """failure_info must contain a 'version' key with an int value."""
    pool, conn, cur = make_mock_pool()
    mock_pool_fn.return_value = pool

    with pytest.raises(AssertionError, match="failure_info must have a failure_info"):
        mark_job_failed(111, {"message": "missing version"})


# --- get_pool_demand ---

@patch("db._init_pool")
def test_get_pool_demand(mock_pool_fn):
    pool, conn, cur = make_mock_pool()
    mock_pool_fn.return_value = pool
    cur.fetchone.return_value = (3, 1)

    jobs, workers = get_pool_demand(1000, ["ubuntu-24.04-riscv"])

    assert jobs == 3
    assert workers == 1


# --- get_pending_jobs ---

@patch("db._init_pool")
def test_get_pending_jobs(mock_pool_fn):
    pool, conn, cur = make_mock_pool()
    mock_pool_fn.return_value = pool
    cur.fetchall.return_value = [{"job_id": 333}, {"job_id": 111}]

    result = get_pending_jobs()

    assert result == [{"job_id": 333}, {"job_id": 111}]


# --- add worker / mark worker failed ---

def _add_worker_default(**overrides):
    defaults = dict(
        provider="github",
        entity_id=1000,
        entity_name="test-org",
        entity_type="Organization",
        installation_id=42,
        repo_full_name="test-org/test-repo",
        k8s_pool="scw-em-rv1",
        pod_name="pod-1",
        job_labels=["rise"],
        k8s_image="img:latest",
    )
    defaults.update(overrides)
    return add_worker(**defaults)


@patch("db._init_pool")
def test_add_worker(mock_pool_fn):
    pool, conn, cur = make_mock_pool()
    mock_pool_fn.return_value = pool
    cur.rowcount = 1  # inserted

    _add_worker_default()

    # No explicit commit needed — _PoolConnection.__exit__ handles it


@patch("db._init_pool")
def test_add_worker_duplicate_raises(mock_pool_fn):
    """DuplicateRunnerNameException propagates; context manager handles rollback."""
    pool, conn, cur = make_mock_pool()
    mock_pool_fn.return_value = pool
    cur.rowcount = 0  # collision

    with pytest.raises(DuplicateRunnerNameException):
        _add_worker_default()


@patch("db._init_pool")
def test_mark_worker_failed(mock_pool_fn):
    pool, conn, cur = make_mock_pool()
    mock_pool_fn.return_value = pool

    mark_worker_failed("pod-1", None, {"version": 2, "reason": "runner_never_registered"}, None)

    assert cur.execute.call_count >= 1


# --- hold_connection ---

@patch("db._init_pool")
def test_hold_connection_reuses_connection_for_nested_get_conn(mock_pool_fn):
    """Inside hold_connection, nested _get_conn() yields the held connection
    without calling pool.getconn again."""
    pool, conn, cur = make_mock_pool()
    mock_pool_fn.return_value = pool

    with hold_connection() as held:
        # mark_worker_failed borrows via _get_conn; should reuse `held`.
        with _get_conn() as inner1:
            assert inner1 is held
        with _get_conn() as inner2:
            assert inner2 is held

    # Only one pool.getconn call even though we "borrowed" three times.
    assert pool.getconn.call_count == 1


@patch("db._init_pool")
def test_hold_connection_does_not_do_transation(mock_pool_fn):
    """Clean exit => commit the transaction and return the connection to the pool."""
    pool, conn, cur = make_mock_pool()
    mock_pool_fn.return_value = pool

    with hold_connection():
        pass

    conn.commit.assert_not_called()
    conn.rollback.assert_not_called()
    pool.putconn.assert_called_once_with(conn)


@patch("db._init_pool")
def test_hold_connection_does_not_do_transation_even_on_exception(mock_pool_fn):
    """Exception inside the block => rollback and still return the connection."""
    pool, conn, cur = make_mock_pool()
    mock_pool_fn.return_value = pool

    class MyError(Exception):
        pass

    with pytest.raises(MyError):
        with hold_connection():
            raise MyError("boom")

    conn.rollback.assert_not_called()
    conn.commit.assert_not_called()
    pool.putconn.assert_called_once_with(conn)

@patch("db._init_pool")
def test_block_inside_hold_connection_does_transation(mock_pool_fn):
    """Clean exit => commit the transaction and return the connection to the pool."""
    pool, conn, cur = make_mock_pool()
    mock_pool_fn.return_value = pool

    with hold_connection():
        mark_job_running(1, "my-runner")

    conn.commit.assert_called_once()
    conn.rollback.assert_not_called()
    pool.putconn.assert_called_once_with(conn)

@patch("db._init_pool")
def test_block_with_error_inside_hold_connection_does_rollback(mock_pool_fn):
    """Clean exit => commit the transaction and return the connection to the pool."""
    pool, conn, cur = make_mock_pool()
    mock_pool_fn.return_value = pool

    class MyError(Exception):
        pass

    with pytest.raises(MyError):
        with hold_connection():
            # Do it inside the block because `hold_connection` does call `cur.execute`
            cur.execute.side_effect = MyError("anything")
            mark_job_running(1, "my-runner")

    conn.commit.assert_not_called()
    conn.rollback.assert_called_once()
    pool.putconn.assert_called_once_with(conn)
