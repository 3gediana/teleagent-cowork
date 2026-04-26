from __future__ import annotations

import os
import sys
import tempfile
from pathlib import Path

import pytest

sys.path.insert(0, str(Path(__file__).parent.parent))

from tasq.config import (
    Config,
    DEFAULT_TASQ_HOME,
    get_default_tasq_home,
    resolve_db_path,
    resolve_tasq_home,
)


class TestGetDefaultTasqHome:
    def test_env_override(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            path = Path(td) / "custom_tasq"
            os.environ["TASQ_HOME"] = str(path)
            try:
                result = get_default_tasq_home()
                assert result == path
            finally:
                del os.environ["TASQ_HOME"]

    def test_default_home(self) -> None:
        if "TASQ_HOME" in os.environ:
            del os.environ["TASQ_HOME"]
        result = get_default_tasq_home()
        assert result == DEFAULT_TASQ_HOME


class TestResolveTasqHome:
    def test_returns_path(self) -> None:
        result = resolve_tasq_home()
        assert isinstance(result, Path)


class TestResolveDbPath:
    def test_db_path_under_tasq_home(self) -> None:
        result = resolve_db_path()
        assert result.name == "tasq.db"
        assert result.parent == resolve_tasq_home()


class TestConfig:
    def test_default_tasq_home(self) -> None:
        cfg = Config()
        assert isinstance(cfg.tasq_home, Path)

    def test_custom_tasq_home(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            custom = Path(td) / "my_tasq"
            cfg = Config(custom)
            assert cfg.tasq_home == custom

    def test_load_empty_config(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            custom = Path(td) / "my_tasq"
            custom.mkdir()
            cfg = Config(custom)
            assert cfg._data == {}

    def test_save_and_load_config(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            custom = Path(td) / "my_tasq"
            custom.mkdir()
            cfg = Config(custom)
            cfg.set("editor", "vim")
            cfg.load()
            assert cfg.get("editor") == "vim"

    def test_get_default(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            custom = Path(td) / "my_tasq"
            custom.mkdir()
            cfg = Config(custom)
            assert cfg.get("nonexistent") is None
            assert cfg.get("nonexistent", "default") == "default"

    def test_set_and_get(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            custom = Path(td) / "my_tasq"
            custom.mkdir()
            cfg = Config(custom)
            cfg.set("key", "value")
            assert cfg.get("key") == "value"

    def test_unset(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            custom = Path(td) / "my_tasq"
            custom.mkdir()
            cfg = Config(custom)
            cfg.set("delkey", "value")
            cfg.unset("delkey")
            assert cfg.get("delkey") is None

    def test_db_path_property(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            custom = Path(td) / "my_tasq"
            cfg = Config(custom)
            assert cfg.db_path == custom / "tasq.db"

    def test_config_path_property(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            custom = Path(td) / "my_tasq"
            cfg = Config(custom)
            assert cfg.config_path == custom / "config.toml"