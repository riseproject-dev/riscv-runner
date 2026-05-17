#!/usr/bin/env python3
"""
Fetch the latest Ubuntu 24.04 runner-images release from GitHub,
extract package versions from its internal.ubuntu24.json manifest,
and update the corresponding ARG lines in the Dockerfiles.

Mapping between JSON fields and Dockerfile ARGs is defined in
versions-map.json at the repository root.
"""

import json
import re
import sys
import urllib.request
from pathlib import Path

REPO = "actions/runner-images"
GITHUB_API = "https://api.github.com"
REPO_ROOT = Path(__file__).resolve().parent.parent


# ---------------------------------------------------------------------------
# GitHub API helpers
# ---------------------------------------------------------------------------

def github_get(path: str):
    """GET a GitHub API endpoint, returns parsed JSON."""
    url = f"{GITHUB_API}{path}"
    req = urllib.request.Request(url, headers={"Accept": "application/vnd.github+json"})
    with urllib.request.urlopen(req) as resp:
        return json.loads(resp.read())


def find_latest_ubuntu24_release() -> dict:
    """Find the latest non-prerelease release whose tag starts with 'ubuntu24/'."""
    page = 1
    while page <= 10:
        releases = github_get(f"/repos/{REPO}/releases?per_page=30&page={page}")
        if not releases:
            break
        for rel in releases:
            if rel["prerelease"] or rel["draft"]:
                continue
            if rel["tag_name"].startswith("ubuntu24/"):
                return rel
        page += 1
    raise RuntimeError("No non-prerelease ubuntu24/* release found")


def find_json_asset_url(release: dict) -> str:
    """Return the browser_download_url for internal.ubuntu24.json from a release."""
    for asset in release.get("assets", []):
        if asset["name"] == "internal.ubuntu24.json":
            return asset["browser_download_url"]
    # Fallback: construct URL from tag
    tag = release["tag_name"]
    encoded_tag = tag.replace("/", "%2F")
    return f"https://github.com/{REPO}/releases/download/{encoded_tag}/internal.ubuntu24.json"


def download_json(url: str) -> dict:
    """Download and parse a JSON file."""
    req = urllib.request.Request(url)
    with urllib.request.urlopen(req) as resp:
        return json.loads(resp.read())


# ---------------------------------------------------------------------------
# JSON manifest traversal
# ---------------------------------------------------------------------------

def extract_versions(node) -> dict:
    """
    Walk the runner-images JSON tree and build a flat lookup dict.

    Returns e.g.:
      "Cached Tools/Python"         -> ["3.10.20", "3.11.15", ...]
      "Project Management/Gradle"   -> "9.4.1"
      "Java"                        -> ["17.0.18+8", "21.0.10+7", ...]
    """

    def _walk(node, path: list, results: dict):
        if isinstance(node, list):
            for item in node:
                _walk(item, path, results)
            return
        if not isinstance(node, dict):
            return

        node_type = node.get("NodeType", "")

        if node_type == "ToolVersionNode":
            tool = node.get("ToolName", "")
            key = "/".join(path + [tool]) if path else tool
            results[key] = node.get("Version", "")

        elif node_type == "ToolVersionsListNode":
            tool = node.get("ToolName", "")
            key = "/".join(path + [tool]) if path else tool
            results[key] = node.get("Versions", [])

        elif node_type == "TableNode":
            # Java table: rows like "17.0.18+8 (default)|JAVA_HOME_17_X64"
            parent_key = "/".join(path) if path else "Table"
            parsed = []
            for row in node.get("Rows", []):
                ver = row.split("|")[0].strip()
                ver = re.sub(r"\s*\(.*?\)\s*$", "", ver)  # strip "(default)" etc.
                parsed.append(ver)
            results[parent_key] = parsed

        elif node_type == "HeaderNode":
            title = node.get("Title", "")
            _walk(node.get("Children", []), path + [title] if title else path, results)
            return  # don't recurse Children again below

        # Recurse Children for non-header nodes
        children = node.get("Children")
        if children is not None:
            _walk(children, path, results)

    results: dict = {}
    _walk(node, [], results)
    return results


# ---------------------------------------------------------------------------
# Dockerfile ARG updater
# ---------------------------------------------------------------------------

def update_dockerfile_arg(dockerfile_path: Path, arg_name: str, new_version: str) -> bool:
    """
    Update every occurrence of ARG <arg_name>=<value> in a Dockerfile.
    Returns True if any change was made.
    """
    pattern = re.compile(rf"^(ARG\s+{re.escape(arg_name)}=)([^\s#]+)(.*)")
    lines = dockerfile_path.read_text().splitlines(keepends=True)
    changed = False
    for i, line in enumerate(lines):
        m = pattern.match(line.rstrip("\n"))
        if m and m.group(2) != new_version:
            lines[i] = f"{m.group(1)}{new_version}{m.group(3)}\n"
            changed = True
    if changed:
        dockerfile_path.write_text("".join(lines))
    return changed


# ---------------------------------------------------------------------------
# Version resolution
# ---------------------------------------------------------------------------

def resolve_version(lookup: dict, entry: dict) -> str | None:
    """Resolve a version string from the extracted lookup dict for a mapping entry."""
    json_tool = entry["json_tool"]

    # Try with various prefixes the JSON tree might use
    value = None
    for prefix in ("", "Installed Software/", "Ubuntu 24.04/Installed Software/"):
        value = lookup.get(f"{prefix}{json_tool}")
        if value is not None:
            break

    if value is None:
        print(f"  WARNING: '{json_tool}' not found in manifest", file=sys.stderr)
        return None

    match_prefix = entry.get("match_prefix")

    if isinstance(value, list) and match_prefix:
        for v in value:
            if v.startswith(f"{match_prefix}.") or v.startswith(f"{match_prefix}+"):
                return v
            # Fallback: bare prefix match (e.g. major-only like "17")
            if v.startswith(match_prefix):
                return v
        print(f"  WARNING: no version matching '{match_prefix}' in {value}", file=sys.stderr)
        return None

    if isinstance(value, list):
        return value[0] if value else None

    if isinstance(value, str):
        return value

    print(f"  WARNING: unexpected type for '{json_tool}': {type(value)}", file=sys.stderr)
    return None


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

def main() -> int:
    print("Finding latest ubuntu24 release...")
    release = find_latest_ubuntu24_release()
    tag = release["tag_name"]
    print(f"  Found: {tag}")

    json_url = find_json_asset_url(release)
    print(f"  Downloading manifest...")
    manifest = download_json(json_url)

    print("Extracting versions from manifest...")
    lookup = extract_versions(manifest)

    map_path = REPO_ROOT / "versions-map.json"
    print(f"Loading mapping from {map_path.relative_to(REPO_ROOT)}")
    mapping = json.loads(map_path.read_text())

    updates = []
    for entry in mapping:
        arg_name = entry["arg"]
        dockerfile_rel = entry["dockerfile"]
        dockerfile_path = REPO_ROOT / dockerfile_rel

        version = resolve_version(lookup, entry)
        if version is None:
            continue

        if update_dockerfile_arg(dockerfile_path, arg_name, version):
            updates.append((arg_name, version, dockerfile_rel))
            print(f"  UPDATED {arg_name} -> {version} in {dockerfile_rel}")
        else:
            print(f"  (no change) {arg_name} = {version}")

    if updates:
        print(f"\n{len(updates)} ARG(s) updated.")
    else:
        print("\nNo updates needed.")

    return 0


if __name__ == "__main__":
    sys.exit(main())
