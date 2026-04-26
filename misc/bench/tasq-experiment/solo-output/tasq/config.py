"""Configuration management for tasq."""
from __future__ import annotations

import sys
from pathlib import Path
from typing import Any, Optional

if sys.version_info < (3, 11):
    import tomli as tomllib
else:
    import tomllib


DEFAULT_CONFIG = {
    "database": {"path": "~/.tasq/tasq.db"},
    "display": {
        "date_format": "%Y-%m-%d",
        "datetime_format": "%Y-%m-%d %H:%M",
        "show_tags": True,
        "compact": False,
    },
    "defaults": {
        "priority": "medium",
        "status": "todo",
    },
    "editor": {},
}


def _dict_to_toml(d: dict[str, Any], indent: int = 0) -> str:
    lines = []
    prefix = "  " * indent
    for key, value in d.items():
        if isinstance(value, dict):
            lines.append(f"{prefix}{key} = {{")
            lines.append(_dict_to_toml(value, indent + 1))
            lines.append(f"{prefix}}}")  # type: ignore
        elif isinstance(value, bool):
            lines.append(f"{prefix}{key} = {'true' if value else 'false'}")
        elif isinstance(value, int):
            lines.append(f"{prefix}{key} = {value}")
        elif isinstance(value, str):
            lines.append(f"{prefix}{key} = {repr(value)}")
        else:
            lines.append(f"{prefix}{key} = {repr(value)}")
    return "\n".join(lines)


class Config:
    def __init__(self, config_path: Optional[Path] = None) -> None:
        if config_path is None:
            config_path = Path.home() / ".tasq" / "config.toml"
        self.config_path = config_path
        self._data: dict[str, Any] = {}
        self.load()

    def load(self) -> None:
        if self.config_path.exists():
            try:
                with open(self.config_path, "rb") as f:
                    self._data = tomllib.load(f)
            except Exception:
                self._data = dict(DEFAULT_CONFIG)
        else:
            self._data = dict(DEFAULT_CONFIG)

    def save(self) -> None:
        self.config_path.parent.mkdir(parents=True, exist_ok=True)
        content = "# tasq configuration\n\n" + _dict_to_toml(self._data) + "\n"
        with open(self.config_path, "w", encoding="utf-8") as f:
            f.write(content)

    def get(self, key: str, default: Any = None) -> Any:
        keys = key.split(".")
        value = self._data
        for k in keys:
            if isinstance(value, dict):
                value = value.get(k)
            else:
                return default
            if value is None:
                return default
        return value

    def set(self, key: str, value: Any) -> None:
        keys = key.split(".")
        d = self._data
        for k in keys[:-1]:
            if k not in d:
                d[k] = {}
            d = d[k]
        d[keys[-1]] = value
        self.save()

    def get_db_path(self) -> Path:
        path = self.get("database.path", "~/.tasq/tasq.db")
        p = Path(path).expanduser()
        if not p.is_absolute():
            p = Path.home() / ".tasq" / str(p)
        return p

    def is_compact(self) -> bool:
        return bool(self.get("display.compact", False))

    def show_tags(self) -> bool:
        return bool(self.get("display.show_tags", True))

    def get_date_format(self) -> str:
        return str(self.get("display.date_format", "%Y-%m-%d"))

    def get_datetime_format(self) -> str:
        return str(self.get("display.datetime_format", "%Y-%m-%d %H:%M"))

    def get_default_priority(self) -> str:
        return str(self.get("defaults.priority", "medium"))

    def get_default_status(self) -> str:
        return str(self.get("defaults.status", "todo"))


def load_config() -> Config:
    return Config()