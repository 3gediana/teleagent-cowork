"""Tests for tasq.models module."""
from __future__ import annotations

import pytest
from datetime import date, datetime

from tasq.models import Task, Project, Tag, TaskDeps, get_tasq_home, get_db_path


class TestTask:
    def test_task_creation(self) -> None:
        task = Task(
            title="Test Task",
            description="Test description",
            priority="high",
            status="todo",
        )
        assert task.title == "Test Task"
        assert task.description == "Test description"
        assert task.priority == "high"
        assert task.status == "todo"

    def test_task_empty_title_raises(self) -> None:
        with pytest.raises(ValueError, match="title cannot be empty"):
            Task(title="")

    def test_task_invalid_priority_raises(self) -> None:
        with pytest.raises(ValueError, match="Invalid priority"):
            Task(title="Test", priority="invalid")

    def test_task_invalid_status_raises(self) -> None:
        with pytest.raises(ValueError, match="Invalid status"):
            Task(title="Test", status="invalid")

    def test_task_default_values(self) -> None:
        task = Task(title="Default Test")
        assert task.priority == "medium"
        assert task.status == "todo"
        assert task.project_id is None
        assert task.due_date is None
        assert task.tags == []
        assert task.completed_at is None

    def test_task_mark_done(self) -> None:
        task = Task(title="Mark Done Test")
        assert task.status == "todo"
        assert task.completed_at is None

        task.mark_done()

        assert task.status == "done"
        assert task.completed_at is not None

    def test_task_block(self) -> None:
        task = Task(title="Block Test")
        task.block()
        assert task.status == "blocked"

    def test_task_unblock(self) -> None:
        task = Task(title="Unblock Test", status="blocked")
        task.unblock()
        assert task.status == "todo"

    def test_task_unblock_non_blocked(self) -> None:
        task = Task(title="Test", status="todo")
        task.unblock()
        assert task.status == "todo"

    def test_task_is_overdue_no_due(self) -> None:
        task = Task(title="No Due")
        assert not task.is_overdue()

    def test_task_is_overdue_future(self) -> None:
        task = Task(title="Future", due_date=date(2030, 1, 1))
        assert not task.is_overdue()

    def test_task_is_overdue_past(self) -> None:
        task = Task(title="Past", due_date=date(2020, 1, 1), status="todo")
        assert task.is_overdue()

    def test_task_is_overdue_done(self) -> None:
        task = Task(title="Done", due_date=date(2020, 1, 1), status="done")
        assert not task.is_overdue()

    def test_task_to_dict(self) -> None:
        task = Task(
            id=1,
            title="Dict Test",
            description="Description",
            priority="high",
            status="todo",
            project_id=5,
            due_date=date(2025, 6, 15),
            tags=["bug", "urgent"],
        )

        d = task.to_dict()

        assert d["id"] == 1
        assert d["title"] == "Dict Test"
        assert d["description"] == "Description"
        assert d["priority"] == "high"
        assert d["status"] == "todo"
        assert d["project_id"] == 5
        assert d["due_date"] == "2025-06-15"
        assert d["tags"] == ["bug", "urgent"]

    def test_task_from_dict(self) -> None:
        data = {
            "id": 10,
            "title": "From Dict",
            "description": "Test description",
            "priority": "urgent",
            "status": "in_progress",
            "project_id": 3,
            "due_date": "2025-12-25",
            "created_at": "2025-01-01T10:00:00",
            "updated_at": "2025-01-02T11:00:00",
            "completed_at": None,
            "tags": ["feature"],
        }

        task = Task.from_dict(data)

        assert task.id == 10
        assert task.title == "From Dict"
        assert task.priority == "urgent"
        assert task.status == "in_progress"
        assert task.project_id == 3
        assert task.due_date == date(2025, 12, 25)
        assert task.tags == ["feature"]

    def test_task_from_dict_minimal(self) -> None:
        data = {"title": "Minimal Task"}
        task = Task.from_dict(data)

        assert task.title == "Minimal Task"
        assert task.priority == "medium"
        assert task.status == "todo"
        assert task.tags == []

    def test_task_with_tags(self) -> None:
        task = Task(title="Tagged", tags=["a", "b", "c"])
        assert len(task.tags) == 3
        assert "a" in task.tags


class TestProject:
    def test_project_creation(self) -> None:
        proj = Project(name="Test Project", description="A test")
        assert proj.name == "Test Project"
        assert proj.description == "A test"

    def test_project_empty_name_raises(self) -> None:
        with pytest.raises(ValueError, match="name cannot be empty"):
            Project(name="")

    def test_project_to_dict(self) -> None:
        proj = Project(id=1, name="Proj", description="Desc")
        d = proj.to_dict()
        assert d["name"] == "Proj"
        assert d["description"] == "Desc"

    def test_project_from_dict(self) -> None:
        data = {"id": 5, "name": "From Data", "description": "Loaded"}
        proj = Project.from_dict(data)
        assert proj.name == "From Data"


class TestTag:
    def test_tag_creation(self) -> None:
        tag = Tag(name="TestTag")
        assert tag.name == "testtag"

    def test_tag_empty_name_raises(self) -> None:
        with pytest.raises(ValueError, match="name cannot be empty"):
            Tag(name="")

    def test_tag_name_normalized(self) -> None:
        tag = Tag(name="  UPPERCASE  ")
        assert tag.name == "uppercase"

    def test_tag_to_dict(self) -> None:
        tag = Tag(id=1, name="bug")
        d = tag.to_dict()
        assert d["name"] == "bug"

    def test_tag_from_dict(self) -> None:
        data = {"id": 2, "name": "feature"}
        tag = Tag.from_dict(data)
        assert tag.name == "feature"


class TestTaskDeps:
    def test_task_deps_creation(self) -> None:
        deps = TaskDeps(task_id=1, depends_on_id=2)
        assert deps.task_id == 1
        assert deps.depends_on_id == 2


class TestHelperFunctions:
    def test_get_tasq_home_default(self) -> None:
        home = get_tasq_home()
        assert home.name == ".tasq"

    def test_get_db_path(self) -> None:
        db_path = get_db_path()
        assert db_path.name == "tasq.db"
        assert ".tasq" in str(db_path)

    def test_get_config_path(self) -> None:
        from tasq.models import get_config_path
        config_path = get_config_path()
        assert config_path.name == "config.toml"