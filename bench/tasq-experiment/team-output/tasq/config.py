from __future__ import annotations

import os
import sys
from pathlib import Path

if sys.version_info >= (3, 11):
    import tomllib
else:
    try:
        import tomli as tomllib
    except ImportError:
        tomllib = None


DEFAULT_TASQ_HOME = Path.home() / ".tasq"
CONFIG_FILE_NAME = "config.toml"


class Config:
    def __init__(self, tasq_home: Path | None = None) -> None:
        self.tasq_home = tasq_home or get_default_tasq_home()
        self.config_path = self.tasq_home / CONFIG_FILE_NAME
        self._data: dict = {}
        self.load()

    @property
    def db_path(self) -> Path:
        return self.tasq_home / "tasq.db"

    def load(self) -> None:
        self._data = {}
        if self.config_path.exists():
            with open(self.config_path, "rb") as f:
                if tomllib is None:
                    raise ImportError("tomli is required for Python < 3.11")
                self._data = tomllib.load(f)

    def save(self) -> None:
        self.tasq_home.mkdir(parents=True, exist_ok=True)
        try:
            import tomli_w
        except ImportError:
            import tomli_w
        with open(self.config_path, "wb") as f:
            tomli_w.dump(self._data, f)

    def get(self, key: str, default: str | None = None) -> str | None:
        return self._data.get(key, default)

    def set(self, key: str, value: str) -> None:
        self._data[key] = value
        self.save()

    def unset(self, key: str) -> None:
        if key in self._data:
            del self._data[key]
            self.save()


def get_default_tasq_home() -> Path:
    env = os.environ.get("TASQ_HOME")
    if env:
        return Path(env)
    return DEFAULT_TASQ_HOME


def resolve_tasq_home() -> Path:
    return get_default_tasq_home()


def resolve_db_path() -> Path:
    return resolve_tasq_home() / "tasq.db"