from __future__ import annotations

from dataclasses import dataclass, field
from datetime import date, datetime
from enum import Enum
from typing import Literal


class Priority(str, Enum):
    LOW = "low"
    MEDIUM = "medium"
    HIGH = "high"
    URGENT = "urgent"


class Status(str, Enum):
    TODO = "todo"
    IN_PROGRESS = "in_progress"
    BLOCKED = "blocked"
    DONE = "done"
    CANCELLED = "cancelled"


@dataclass
class Tag:
    id: int | None = None
    name: str = ""

    def to_row(self) -> dict:
        return {"id": self.id, "name": self.name}

    @classmethod
    def from_row(cls, row: dict) -> Tag:
        return cls(id=row.get("id"), name=row.get("name", ""))


@dataclass
class Project:
    id: int | None = None
    name: str = ""
    description: str | None = None
    created_at: datetime = field(default_factory=datetime.now)

    def to_row(self) -> dict:
        return {
            "id": self.id,
            "name": self.name,
            "description": self.description,
            "created_at": self.created_at.isoformat(),
        }

    @classmethod
    def from_row(cls, row: dict) -> Project:
        created = row.get("created_at", "")
        if isinstance(created, str):
            created = datetime.fromisoformat(created)
        return cls(
            id=row.get("id"),
            name=row.get("name", ""),
            description=row.get("description"),
            created_at=created,
        )


@dataclass
class Task:
    id: int | None = None
    title: str = ""
    description: str | None = None
    priority: Literal["low", "medium", "high", "urgent"] = "medium"
    status: Literal["todo", "in_progress", "blocked", "done", "cancelled"] = "todo"
    project_id: int | None = None
    due_date: date | None = None
    created_at: datetime = field(default_factory=datetime.now)
    updated_at: datetime = field(default_factory=datetime.now)
    completed_at: datetime | None = None
    tags: list[str] = field(default_factory=list)

    VALID_PRIORITIES = {"low", "medium", "high", "urgent"}
    VALID_STATUSES = {"todo", "in_progress", "blocked", "done", "cancelled"}

    def __post_init__(self) -> None:
        if self.priority not in self.VALID_PRIORITIES:
            raise ValueError(f"Invalid priority: {self.priority}")
        if self.status not in self.VALID_STATUSES:
            raise ValueError(f"Invalid status: {self.status}")

    def to_row(self) -> dict:
        due = self.due_date.isoformat() if self.due_date else None
        completed = self.completed_at.isoformat() if self.completed_at else None
        return {
            "id": self.id,
            "title": self.title,
            "description": self.description,
            "priority": self.priority,
            "status": self.status,
            "project_id": self.project_id,
            "due_date": due,
            "created_at": self.created_at.isoformat(),
            "updated_at": self.updated_at.isoformat(),
            "completed_at": completed,
            "tags": self.tags,
        }

    @classmethod
    def from_row(cls, row: dict) -> Task:
        created = row.get("created_at", "")
        if isinstance(created, str) and created:
            created = datetime.fromisoformat(created)
        elif not created:
            created = datetime.now()
        updated = row.get("updated_at", "")
        if isinstance(updated, str) and updated:
            updated = datetime.fromisoformat(updated)
        elif not updated:
            updated = datetime.now()
        completed = row.get("completed_at")
        if isinstance(completed, str) and completed:
            completed = datetime.fromisoformat(completed)
        due = row.get("due_date")
        if isinstance(due, str) and due:
            due = date.fromisoformat(due)
        tags_raw = row.get("tags", [])
        if isinstance(tags_raw, str):
            tags_raw = [t.strip() for t in tags_raw.split(",") if t.strip()]
        return cls(
            id=row.get("id"),
            title=row.get("title", ""),
            description=row.get("description"),
            priority=row.get("priority", "medium"),
            status=row.get("status", "todo"),
            project_id=row.get("project_id"),
            due_date=due,
            created_at=created,
            updated_at=updated,
            completed_at=completed,
            tags=tags_raw if isinstance(tags_raw, list) else [],
        )


def validate_priority(value: str) -> Literal["low", "medium", "high", "urgent"]:
    if value not in {"low", "medium", "high", "urgent"}:
        raise ValueError(f"Invalid priority: {value}")
    return value


def validate_status(value: str) -> Literal["todo", "in_progress", "blocked", "done", "cancelled"]:
    if value not in {"todo", "in_progress", "blocked", "done", "cancelled"}:
        raise ValueError(f"Invalid status: {value}")
    return value