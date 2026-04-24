from __future__ import annotations

import sys
from datetime import date, datetime
from pathlib import Path

import pytest

sys.path.insert(0, str(Path(__file__).parent.parent))

from tasq.models import Priority, Project, Status, Tag, Task, validate_priority, validate_status


class TestTag:
    def test_to_row(self) -> None:
        tag = Tag(id=1, name="urgent")
        row = tag.to_row()
        assert row["id"] == 1
        assert row["name"] == "urgent"

    def test_from_row(self) -> None:
        row = {"id": 5, "name": "frontend"}
        tag = Tag.from_row(row)
        assert tag.id == 5
        assert tag.name == "frontend"

    def test_from_row_missing_id(self) -> None:
        row = {"name": "test"}
        tag = Tag.from_row(row)
        assert tag.id is None
        assert tag.name == "test"


class TestProject:
    def test_to_row(self) -> None:
        proj = Project(id=1, name="web", description="Web project", created_at=datetime(2024, 1, 1, 12, 0))
        row = proj.to_row()
        assert row["id"] == 1
        assert row["name"] == "web"
        assert row["description"] == "Web project"
        assert row["created_at"] == "2024-01-01T12:00:00"

    def test_from_row(self) -> None:
        row = {"id": 2, "name": "api", "description": "REST API", "created_at": "2024-01-02T10:00:00"}
        proj = Project.from_row(row)
        assert proj.id == 2
        assert proj.name == "api"
        assert proj.description == "REST API"
        assert proj.created_at == datetime(2024, 1, 2, 10, 0, 0)

    def test_default_created_at(self) -> None:
        proj = Project(name="test")
        assert proj.created_at is not None


class TestTask:
    def test_valid_priorities(self) -> None:
        for p in ["low", "medium", "high", "urgent"]:
            task = Task(title="test", priority=p)
            assert task.priority == p

    def test_invalid_priority(self) -> None:
        with pytest.raises(ValueError):
            Task(title="test", priority="invalid")

    def test_valid_statuses(self) -> None:
        for s in ["todo", "in_progress", "blocked", "done", "cancelled"]:
            task = Task(title="test", status=s)
            assert task.status == s

    def test_invalid_status(self) -> None:
        with pytest.raises(ValueError):
            Task(title="test", status="invalid")

    def test_to_row(self) -> None:
        task = Task(
            id=1,
            title="Fix bug",
            description="Fix the login bug",
            priority="high",
            status="todo",
            project_id=2,
            due_date=date(2024, 6, 15),
            created_at=datetime(2024, 1, 1),
            updated_at=datetime(2024, 1, 2),
        )
        row = task.to_row()
        assert row["id"] == 1
        assert row["title"] == "Fix bug"
        assert row["description"] == "Fix the login bug"
        assert row["priority"] == "high"
        assert row["status"] == "todo"
        assert row["project_id"] == 2
        assert row["due_date"] == "2024-06-15"
        assert row["tags"] == []

    def test_to_row_no_due_date(self) -> None:
        task = Task(title="Test", due_date=None)
        row = task.to_row()
        assert row["due_date"] is None

    def test_from_row(self) -> None:
        row = {
            "id": 3,
            "title": "Deploy",
            "description": "Deploy to prod",
            "priority": "urgent",
            "status": "done",
            "project_id": 1,
            "due_date": "2024-07-01",
            "created_at": "2024-01-01T00:00:00",
            "updated_at": "2024-01-02T00:00:00",
            "completed_at": "2024-01-03T00:00:00",
            "tags": ["ops", "critical"],
        }
        task = Task.from_row(row)
        assert task.id == 3
        assert task.title == "Deploy"
        assert task.priority == "urgent"
        assert task.status == "done"
        assert task.due_date == date(2024, 7, 1)
        assert task.completed_at == datetime(2024, 1, 3, 0, 0, 0)
        assert task.tags == ["ops", "critical"]

    def test_from_row_no_completed_at(self) -> None:
        row = {"id": 1, "title": "Test", "priority": "medium", "status": "todo", "created_at": "", "updated_at": ""}
        task = Task.from_row(row)
        assert task.completed_at is None

    def test_tags_default_empty(self) -> None:
        task = Task(title="Test")
        assert task.tags == []

    def test_default_values(self) -> None:
        task = Task(title="My task")
        assert task.priority == "medium"
        assert task.status == "todo"
        assert task.project_id is None
        assert task.due_date is None
        assert task.completed_at is None
        assert task.tags == []


class TestValidateFunctions:
    def test_validate_priority_valid(self) -> None:
        assert validate_priority("high") == "high"

    def test_validate_priority_invalid(self) -> None:
        with pytest.raises(ValueError):
            validate_priority("extreme")

    def test_validate_status_valid(self) -> None:
        assert validate_status("in_progress") == "in_progress"

    def test_validate_status_invalid(self) -> None:
        with pytest.raises(ValueError):
            validate_status("maybe")


class TestEnums:
    def test_priority_values(self) -> None:
        assert Priority.LOW.value == "low"
        assert Priority.MEDIUM.value == "medium"
        assert Priority.HIGH.value == "high"
        assert Priority.URGENT.value == "urgent"

    def test_status_values(self) -> None:
        assert Status.TODO.value == "todo"
        assert Status.IN_PROGRESS.value == "in_progress"
        assert Status.BLOCKED.value == "blocked"
        assert Status.DONE.value == "done"
        assert Status.CANCELLED.value == "cancelled"