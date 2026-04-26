"""Data models for tasq."""
from __future__ import annotations

from dataclasses import dataclass, field
from datetime import datetime, date
from typing import Optional, Literal
from pathlib import Path
import os


@dataclass
class Project:
    id: Optional[int] = None
    name: str = ""
    description: Optional[str] = None
    created_at: datetime = field(default_factory=datetime.utcnow)
    archived: bool = False

    def __post_init__(self) -> None:
        if not self.name:
            raise ValueError("Project name cannot be empty")

    def to_dict(self) -> dict:
        return {
            "id": self.id,
            "name": self.name,
            "description": self.description,
            "created_at": self.created_at.isoformat() if self.created_at else None,
            "archived": self.archived,
        }

    @classmethod
    def from_dict(cls, d: dict) -> Project:
        created_at = d.get("created_at")
        if isinstance(created_at, str):
            created_at = datetime.fromisoformat(created_at)
        elif created_at is None:
            created_at = datetime.utcnow()

        return cls(
            id=d.get("id"),
            name=d.get("name", ""),
            description=d.get("description"),
            created_at=created_at,
            archived=d.get("archived", False),
        )


@dataclass
class Tag:
    id: Optional[int] = None
    name: str = ""

    def __post_init__(self) -> None:
        if not self.name:
            raise ValueError("Tag name cannot be empty")
        self.name = self.name.strip().lower()

    def to_dict(self) -> dict:
        return {
            "id": self.id,
            "name": self.name,
        }

    @classmethod
    def from_dict(cls, d: dict) -> Tag:
        return cls(
            id=d.get("id"),
            name=d.get("name", ""),
        )


@dataclass
class Task:
    id: Optional[int] = None
    title: str = ""
    description: Optional[str] = None
    priority: Literal["low", "medium", "high", "urgent"] = "medium"
    status: Literal["todo", "in_progress", "blocked", "done", "cancelled"] = "todo"
    project_id: Optional[int] = None
    due_date: Optional[date] = None
    created_at: datetime = field(default_factory=datetime.utcnow)
    updated_at: datetime = field(default_factory=datetime.utcnow)
    completed_at: Optional[datetime] = None
    tags: list[str] = field(default_factory=list)

    def __post_init__(self) -> None:
        if not self.title:
            raise ValueError("Task title cannot be empty")
        if self.priority not in ("low", "medium", "high", "urgent"):
            raise ValueError(f"Invalid priority: {self.priority}")
        if self.status not in ("todo", "in_progress", "blocked", "done", "cancelled"):
            raise ValueError(f"Invalid status: {self.status}")

    def mark_done(self) -> None:
        self.status = "done"
        self.completed_at = datetime.utcnow()
        self.updated_at = datetime.utcnow()

    def block(self) -> None:
        self.status = "blocked"
        self.updated_at = datetime.utcnow()

    def unblock(self) -> None:
        if self.status == "blocked":
            self.status = "todo"
            self.updated_at = datetime.utcnow()

    def is_overdue(self) -> bool:
        if self.due_date is None:
            return False
        if self.status in ("done", "cancelled"):
            return False
        return self.due_date < date.today()

    def to_dict(self) -> dict:
        return {
            "id": self.id,
            "title": self.title,
            "description": self.description,
            "priority": self.priority,
            "status": self.status,
            "project_id": self.project_id,
            "due_date": self.due_date.isoformat() if self.due_date else None,
            "created_at": self.created_at.isoformat(),
            "updated_at": self.updated_at.isoformat(),
            "completed_at": self.completed_at.isoformat() if self.completed_at else None,
            "tags": self.tags,
        }

    @classmethod
    def from_dict(cls, d: dict) -> Task:
        created_at = d.get("created_at")
        if isinstance(created_at, str):
            created_at = datetime.fromisoformat(created_at)
        elif created_at is None:
            created_at = datetime.utcnow()

        updated_at = d.get("updated_at")
        if isinstance(updated_at, str):
            updated_at = datetime.fromisoformat(updated_at)
        elif updated_at is None:
            updated_at = datetime.utcnow()

        completed_at = d.get("completed_at")
        if isinstance(completed_at, str):
            completed_at = datetime.fromisoformat(completed_at)

        due_date = d.get("due_date")
        if isinstance(due_date, str):
            due_date = date.fromisoformat(due_date)

        return cls(
            id=d.get("id"),
            title=d.get("title", ""),
            description=d.get("description"),
            priority=d.get("priority", "medium"),
            status=d.get("status", "todo"),
            project_id=d.get("project_id"),
            due_date=due_date,
            created_at=created_at,
            updated_at=updated_at,
            completed_at=completed_at,
            tags=d.get("tags", []),
        )


@dataclass
class TaskDeps:
    task_id: int
    depends_on_id: int


def get_tasq_home() -> Path:
    home = os.environ.get("TASQ_HOME")
    if home:
        return Path(home).expanduser()
    return Path.home() / ".tasq"


def get_db_path() -> Path:
    return get_tasq_home() / "tasq.db"


def get_config_path() -> Path:
    return get_tasq_home() / "config.toml"