"""Tests for tasq.config module."""
from __future__ import annotations

import pytest
import tempfile
from pathlib import Path

from tasq.config import Config, load_config, DEFAULT_CONFIG


class TestConfig:
    def test_default_config(self) -> None:
        cfg = Config()
        assert cfg.get("display.date_format") == "%Y-%m-%d"
        assert cfg.get("defaults.priority") == "medium"

    def test_load_nonexistent(self, tmp_path: Path) -> None:
        config_path = tmp_path / "nonexistent.toml"
        cfg = Config(config_path)
        assert cfg.get("display.date_format") == "%Y-%m-%d"

    def test_load_existing(self, tmp_path: Path) -> None:
        config_path = tmp_path / "test.toml"
        config_path.write_text('[display]\ndate_format = "%d/%m/%Y"\n')

        cfg = Config(config_path)
        assert cfg.get("display.date_format") == "%d/%m/%Y"

    def test_get_nested_key(self) -> None:
        cfg = Config()
        val = cfg.get("display.date_format")
        assert val == "%Y-%m-%d"

    def test_get_missing_key_default(self) -> None:
        cfg = Config()
        val = cfg.get("nonexistent.key", "default")
        assert val == "default"

    def test_set_and_save(self, tmp_path: Path) -> None:
        config_path = tmp_path / "save_test.toml"
        cfg = Config(config_path)

        cfg.set("display.date_format", "%Y-%m-%d")
        cfg.set("new_section.key", "value")

        cfg2 = Config(config_path)
        assert cfg2.get("display.date_format") == "%Y-%m-%d"
        assert cfg2.get("new_section.key") == "value"

    def test_get_db_path(self) -> None:
        cfg = Config()
        db_path = cfg.get_db_path()
        assert isinstance(db_path, Path)

    def test_is_compact(self) -> None:
        cfg = Config()
        assert cfg.is_compact() == False

    def test_show_tags(self) -> None:
        cfg = Config()
        assert cfg.show_tags() == True

    def test_get_date_format(self) -> None:
        cfg = Config()
        assert cfg.get_date_format() == "%Y-%m-%d"

    def test_get_datetime_format(self) -> None:
        cfg = Config()
        assert cfg.get_datetime_format() == "%Y-%m-%d %H:%M"

    def test_get_default_priority(self) -> None:
        cfg = Config()
        assert cfg.get_default_priority() == "medium"

    def test_get_default_status(self) -> None:
        cfg = Config()
        assert cfg.get_default_status() == "todo"


class TestLoadConfig:
    def test_load_config_returns_config(self) -> None:
        cfg = load_config()
        assert isinstance(cfg, Config)