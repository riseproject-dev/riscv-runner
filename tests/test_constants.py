"""Tests for the real `container/constants.py`. Bypasses the conftest mock
by loading the file directly with `importlib.util`, so the parsing logic
in constants.py is exercised end-to-end.
"""
import importlib.util
import os
import sys
from pathlib import Path

import pytest


_REQUIRED_ENV = {
    "PROD": "false",
    "PROD_URL": "https://p",
    "STAGING_URL": "https://s",
    "K8S_KUBECONFIG": "kc",
    "GHAPP_ORG_PRIVATE_KEY": "k",
    "GHAPP_PERSONAL_PRIVATE_KEY": "k",
    "GHAPP_WEBHOOK_SECRET": "s",
    "TRACE_API_SECRET": "t",
    "POSTGRES_URL": "p",
}

_CONSTANTS_PATH = Path(__file__).resolve().parents[1] / "container" / "constants.py"


def _load_real_constants(extra_env):
    saved = sys.modules.pop("constants", None)
    env = _REQUIRED_ENV | extra_env
    old = {k: os.environ.get(k) for k in env}
    try:
        for k, v in env.items():
            if v is None:
                os.environ.pop(k, None)
            else:
                os.environ[k] = v
        spec = importlib.util.spec_from_file_location("_real_constants", _CONSTANTS_PATH)
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        return module
    finally:
        for k, v in old.items():
            if v is None:
                os.environ.pop(k, None)
            else:
                os.environ[k] = v
        if saved is not None:
            sys.modules["constants"] = saved


def test_go_ghfe_routing_unset_is_empty(monkeypatch):
    monkeypatch.delenv("GO_GHFE_ROUTING", raising=False)
    monkeypatch.delenv("GO_GHFE_URL", raising=False)
    m = _load_real_constants({"GO_GHFE_ROUTING": None, "GO_GHFE_URL": None})
    assert m.GO_GHFE_URL == ""
    assert m.GO_GHFE_ROUTING == frozenset()


def test_go_ghfe_routing_parses_entity_ids():
    m = _load_real_constants({
        "GO_GHFE_URL": "http://go-ghfe.local",
        "GO_GHFE_ROUTING": '{"entities":[152654596, 21003710]}',
    })
    assert m.GO_GHFE_URL == "http://go-ghfe.local"
    assert m.GO_GHFE_ROUTING == frozenset({152654596, 21003710})


def test_go_ghfe_routing_empty_entities_list():
    m = _load_real_constants({"GO_GHFE_ROUTING": '{"entities":[]}'})
    assert m.GO_GHFE_ROUTING == frozenset()
