#!/usr/bin/env python3
"""Reconstruct GitHub App installation history for an entity.

Pulls events from /trace/* on the production GHFE and prints a chronological
table — used to debug "installation not found" cases (jobs that never get
picked up). The PROD_URL is hard-coded; TRACE_API_SECRET comes from the
environment.

For --entity-name <login>, this script shells out to `gh api` to resolve the
login to a numeric account.id (User then Org). Requires `gh auth login`.
"""

import argparse
import json
import os
import subprocess
import sys
from typing import Any

import requests


PROD_URL = "https://riseriscvrunnerappqdvknz9s-ghfe.functions.fnc.fr-par.scw.cloud"
TRACE_API_SECRET = os.environ["TRACE_API_SECRET"]

PAYLOAD_DUMP_LIMIT = 4 * 1024  # bytes — truncate verbose payload dumps for terminal


def _api_get(path: str) -> dict:
    """GET PROD_URL + path with the bearer token. Raises on non-2xx."""
    url = PROD_URL.rstrip("/") + path
    resp = requests.get(url, headers={"Authorization": f"Bearer {TRACE_API_SECRET}"}, timeout=30)
    if resp.status_code != 200:
        sys.exit(f"GET {url} -> {resp.status_code}: {resp.text}")
    return resp.json()


def resolve_entity_name(login: str) -> int:
    """`gh api /users/<login>` then fall back to `/orgs/<login>`. Returns account.id."""
    for path in (f"/users/{login}", f"/orgs/{login}"):
        result = subprocess.run(
            ["gh", "api", path, "--jq", ".id"],
            capture_output=True, text=True,
        )
        if result.returncode == 0 and result.stdout.strip():
            return int(result.stdout.strip())
    sys.exit(f"could not resolve entity name {login!r} via `gh api`. Have you run `gh auth login`?")


def fetch_events(args: argparse.Namespace) -> list[dict]:
    if args.installation_id is not None:
        return _api_get(f"/trace/installation/{args.installation_id}")["events"]
    if args.entity_id is not None:
        return _api_get(f"/trace/entity/{args.entity_id}")["events"]
    if args.entity_name is not None:
        return _api_get(f"/trace/entity/{resolve_entity_name(args.entity_name)}")["events"]
    if args.job_id is not None:
        return _api_get(f"/trace/job/{args.job_id}")["events"]
    sys.exit("internal error: no selector provided")


def fetch_payload(event_id: int) -> Any:
    return _api_get(f"/trace/payload/{event_id}").get("payload")


def _short(s: str | None, n: int) -> str:
    if s is None:
        return ""
    s = str(s)
    return s if len(s) <= n else s[: n - 1] + "…"


def render_table(events: list[dict]) -> None:
    """Print a chronological table — one row per event."""
    cols = ("received_at", "event", "outcome", "entity_name", "repo_full_name", "job_id")
    widths = {
        "received_at": 26,
        "event": 36,
        "outcome": 22,
        "entity_name": 24,
        "repo_full_name": 36,
        "job_id": 14,
    }
    header = "  ".join(f"{c:<{widths[c]}}" for c in cols)
    print(header)
    print("-" * len(header))
    for e in events:
        row = {
            "received_at": _short(e.get("received_at"), widths["received_at"]),
            "event": _short(e.get("event"), widths["event"]),
            "outcome": _short(e.get("outcome"), widths["outcome"]),
            "entity_name": _short(e.get("entity_name"), widths["entity_name"]),
            "repo_full_name": _short(e.get("repo_full_name"), widths["repo_full_name"]),
            "job_id": _short(e.get("job_id"), widths["job_id"]),
        }
        print("  ".join(f"{row[c]:<{widths[c]}}" for c in cols))


def render_verbose_auth_payloads(events: list[dict]) -> None:
    """For each auth_attempt.* row, fetch and dump its payload (truncated)."""
    for e in events:
        if not (e.get("event") or "").startswith("auth_attempt."):
            continue
        payload = fetch_payload(int(e["id"]))
        text = json.dumps(payload, indent=2, default=str)
        if len(text) > PAYLOAD_DUMP_LIMIT:
            text = text[:PAYLOAD_DUMP_LIMIT] + "\n  …(truncated)"
        print(f"\n=== payload for event id={e['id']} ({e.get('event')}) ===")
        print(text)


def diagnosis(events: list[dict]) -> list[str]:
    """Heuristics over the event stream. Returns human-readable hint lines."""
    hints: list[str] = []

    # 1. Suspended without subsequent unsuspend
    last_suspend = None
    last_unsuspend = None
    last_deleted = None
    last_renamed = None
    has_selected = False
    for e in events:
        ev = e.get("event")
        ts = e.get("received_at")
        if ev == "installation.suspend":
            last_suspend = ts
        elif ev == "installation.unsuspend":
            last_unsuspend = ts
        elif ev == "installation.deleted":
            last_deleted = ts
        elif ev == "installation_target.renamed":
            last_renamed = ts

    if last_suspend and (last_unsuspend is None or last_unsuspend < last_suspend):
        hints.append(f"Installation is currently suspended (last suspend at {last_suspend}).")
    if last_deleted:
        hints.append(f"App was uninstalled at {last_deleted} — installation_id no longer valid.")
    if last_renamed:
        hints.append(f"Account was renamed at {last_renamed} — cached entity_name on jobs may be stale.")

    # 2. auth_attempt.404 anywhere is a strong signal
    auth_404 = [e for e in events if e.get("event") == "auth_attempt.404"]
    if auth_404:
        hints.append(f"{len(auth_404)} auth_attempt.404 row(s) — installation lookup failed during scheduler reconcile.")

    return hints


def main() -> int:
    p = argparse.ArgumentParser(description=__doc__)
    g = p.add_mutually_exclusive_group(required=True)
    g.add_argument("--installation-id", type=int)
    g.add_argument("--entity-id", type=int)
    g.add_argument("--entity-name", type=str, help="GitHub login; resolved via `gh api`")
    g.add_argument("--job-id", type=int)
    p.add_argument("--verbose", action="store_true",
                   help="dump full payload for auth_attempt.* rows")
    p.add_argument("--json", action="store_true", help="dump raw events as JSON")
    args = p.parse_args()

    events = fetch_events(args)

    if args.json:
        print(json.dumps(events, indent=2, default=str))
        return 0

    if not events:
        print("No events found.")
        return 0

    render_table(events)

    if args.verbose:
        render_verbose_auth_payloads(events)

    hints = diagnosis(events)
    if hints:
        print("\nDiagnosis hints:")
        for h in hints:
            print(f"  - {h}")

    return 0


if __name__ == "__main__":
    sys.exit(main())
