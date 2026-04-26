"""Tests for tasq.db module."""
from __future__ import annotations

import pytest
import tempfile
from pathlib import Path
from datetime import date, datetime

from tasq.db import Store
from tasq.models import Task, Project, Tag


@pytest.fixture
def db_path(tmp_path: Path) -> Path:
    return tmp_path / "test.db"


@pytest.fixture
def store(db_path: Path) -> Store:
    s = Store(db_path)
    s.migrate()
    return s


class TestStoreProjects:
    def test_add_project(self, store: Store) -> None:
        proj = Project(name="Test Project", description="A test project")
        proj_id = store.add_project(proj)
        assert proj_id > 0

        retrieved = store.get_project(proj_id)
        assert retrieved is not None
        assert retrieved.name == "Test Project"
        assert retrieved.description == "A test project"

    def test_get_project_by_name(self, store: Store) -> None:
        proj = Project(name="MyProject", description="Desc")
        store.add_project(proj)

        found = store.get_project_by_name("MyProject")
        assert found is not None
        assert found.name == "MyProject"

        not_found = store.get_project_by_name("NonExistent")
        assert not_found is None

    def test_list_projects(self, store: Store) -> None:
        store.add_project(Project(name="Project A"))
        store.add_project(Project(name="Project B"))

        projects = store.list_projects()
        assert len(projects) >= 2
        names = [p.name for p in projects]
        assert "Project A" in names
        assert "Project B" in names

    def test_update_project(self, store: Store) -> None:
        proj = Project(name="OldName")
        proj_id = store.add_project(proj)

        store.update_project(proj_id, name="NewName", description="New desc")
        updated = store.get_project(proj_id)
        assert updated is not None
        assert updated.name == "NewName"
        assert updated.description == "New desc"

    def test_delete_project(self, store: Store) -> None:
        proj = Project(name="ToDelete")
        proj_id = store.add_project(proj)

        store.delete_project(proj_id)
        assert store.get_project(proj_id) is None


class TestStoreTags:
    def test_add_tag(self, store: Store) -> None:
        tag = Tag(name="bug")
        tag_id = store.add_tag(tag)
        assert tag_id > 0

        retrieved = store.get_tag(tag_id)
        assert retrieved is not None
        assert retrieved.name == "bug"

    def test_add_duplicate_tag_returns_existing(self, store: Store) -> None:
        tag1 = Tag(name="feature")
        id1 = store.add_tag(tag1)

        tag2 = Tag(name="feature")
        id2 = store.add_tag(tag2)

        assert id1 == id2

    def test_get_tag_by_name(self, store: Store) -> None:
        store.add_tag(Tag(name="urgent"))

        found = store.get_tag_by_name("urgent")
        assert found is not None
        assert found.name == "urgent"

        not_found = store.get_tag_by_name("nonexistent")
        assert not_found is None

    def test_list_tags(self, store: Store) -> None:
        store.add_tag(Tag(name="tag1"))
        store.add_tag(Tag(name="tag2"))

        tags = store.list_tags()
        assert len(tags) >= 2
        names = [t.name for t in tags]
        assert "tag1" in names
        assert "tag2" in names

    def test_update_tag(self, store: Store) -> None:
        tag = Tag(name="oldname")
        tag_id = store.add_tag(tag)

        store.update_tag(tag_id, "newname")
        updated = store.get_tag(tag_id)
        assert updated is not None
        assert updated.name == "newname"

    def test_delete_tag(self, store: Store) -> None:
        tag = Tag(name="todelete")
        tag_id = store.add_tag(tag)

        store.delete_tag(tag_id)
        assert store.get_tag(tag_id) is None


class TestStoreTasks:
    def test_add_task(self, store: Store) -> None:
        task = Task(
            title="Test Task",
            description="A test task",
            priority="high",
            status="todo",
        )
        task_id = store.add_task(task)
        assert task_id > 0

        retrieved = store.get_task(task_id)
        assert retrieved is not None
        assert retrieved.title == "Test Task"
        assert retrieved.priority == "high"
        assert retrieved.status == "todo"

    def test_add_task_with_tags(self, store: Store) -> None:
        task = Task(title="Task With Tags", tags=["bug", "urgent"])
        task_id = store.add_task(task)

        retrieved = store.get_task(task_id)
        assert retrieved is not None
        assert "bug" in retrieved.tags
        assert "urgent" in retrieved.tags

    def test_add_task_with_project(self, store: Store) -> None:
        proj = Project(name="TestProject")
        proj_id = store.add_project(proj)

        task = Task(title="Project Task", project_id=proj_id)
        task_id = store.add_task(task)

        retrieved = store.get_task(task_id)
        assert retrieved is not None
        assert retrieved.project_id == proj_id

    def test_add_task_with_due_date(self, store: Store) -> None:
        due = date(2025, 12, 31)
        task = Task(title="Due Task", due_date=due)
        task_id = store.add_task(task)

        retrieved = store.get_task(task_id)
        assert retrieved is not None
        assert retrieved.due_date == due

    def test_get_task_not_found(self, store: Store) -> None:
        result = store.get_task(9999)
        assert result is None

    def test_update_task(self, store: Store) -> None:
        task = Task(title="Original Title")
        task_id = store.add_task(task)

        store.update_task(task_id, title="Updated Title", priority="urgent")
        updated = store.get_task(task_id)
        assert updated is not None
        assert updated.title == "Updated Title"
        assert updated.priority == "urgent"

    def test_delete_task(self, store: Store) -> None:
        task = Task(title="To Delete")
        task_id = store.add_task(task)

        store.delete_task(task_id)
        assert store.get_task(task_id) is None

    def test_list_tasks_no_filters(self, store: Store) -> None:
        store.add_task(Task(title="Task 1"))
        store.add_task(Task(title="Task 2"))
        store.add_task(Task(title="Task 3"))

        tasks = store.list_tasks()
        assert len(tasks) >= 3

    def test_list_tasks_filter_by_status(self, store: Store) -> None:
        store.add_task(Task(title="Todo Task", status="todo"))
        store.add_task(Task(title="Done Task", status="done"))

        tasks = store.list_tasks(status="todo")
        assert all(t.status == "todo" for t in tasks)

    def test_list_tasks_filter_by_project(self, store: Store) -> None:
        proj = Project(name="FilterProject")
        proj_id = store.add_project(proj)

        store.add_task(Task(title="In Project", project_id=proj_id))
        store.add_task(Task(title="No Project"))

        tasks = store.list_tasks(project_id=proj_id)
        assert all(t.project_id == proj_id for t in tasks)

    def test_list_tasks_filter_by_tag(self, store: Store) -> None:
        task1 = Task(title="Task With Bug", tags=["bug"])
        task2 = Task(title="Task With Feature", tags=["feature"])

        store.add_task(task1)
        store.add_task(task2)

        tasks = store.list_tasks(tag="bug")
        assert all("bug" in t.tags for t in tasks)

    def test_list_tasks_overdue(self, store: Store) -> None:
        past_due = date(2020, 1, 1)
        future_due = date(2030, 12, 31)

        store.add_task(Task(title="Overdue Task", due_date=past_due, status="todo"))
        store.add_task(Task(title="Future Task", due_date=future_due, status="todo"))

        overdue = store.list_tasks(overdue=True)
        assert all(t.is_overdue() for t in overdue)


class TestStoreTaskTags:
    def test_add_task_tag(self, store: Store) -> None:
        task_id = store.add_task(Task(title="Tag Test"))
        store.add_task_tag(task_id, "newtag")

        retrieved = store.get_task(task_id)
        assert "newtag" in retrieved.tags

    def test_remove_task_tag(self, store: Store) -> None:
        task = Task(title="Remove Tag Test", tags=["removeme"])
        task_id = store.add_task(task)

        store.remove_task_tag(task_id, "removeme")
        retrieved = store.get_task(task_id)
        assert "removeme" not in retrieved.tags

    def test_set_task_tags(self, store: Store) -> None:
        task_id = store.add_task(Task(title="Set Tags Test"))
        store.set_task_tags(task_id, ["tag1", "tag2", "tag3"])

        retrieved = store.get_task(task_id)
        assert len(retrieved.tags) == 3
        assert all(t in retrieved.tags for t in ["tag1", "tag2", "tag3"])


class TestStoreTaskDeps:
    def test_add_task_dep(self, store: Store) -> None:
        task1 = Task(title="Blocker")
        task2 = Task(title="Blocked")

        id1 = store.add_task(task1)
        id2 = store.add_task(task2)

        store.add_task_dep(id2, id1)

        blockers = store.get_blockers(id2)
        assert len(blockers) == 1
        assert blockers[0].id == id1

    def test_remove_task_dep(self, store: Store) -> None:
        task1 = Task(title="Dep1")
        task2 = Task(title="Dep2")

        id1 = store.add_task(task1)
        id2 = store.add_task(task2)

        store.add_task_dep(id2, id1)
        store.remove_task_dep(id2, id1)

        blockers = store.get_blockers(id2)
        assert len(blockers) == 0

    def test_get_blockers(self, store: Store) -> None:
        t1 = Task(title="Blocker Task")
        t2 = Task(title="Dependent Task")

        id1 = store.add_task(t1)
        id2 = store.add_task(t2)

        store.add_task_dep(id2, id1)

        blockers = store.get_blockers(id2)
        assert len(blockers) == 1
        assert blockers[0].title == "Blocker Task"

    def test_get_dependants(self, store: Store) -> None:
        t1 = Task(title="Main Task")
        t2 = Task(title="Dependent Task")

        id1 = store.add_task(t1)
        id2 = store.add_task(t2)

        store.add_task_dep(id2, id1)

        dependants = store.get_dependants(id1)
        assert len(dependants) == 1
        assert dependants[0].title == "Dependent Task"


class TestStoreMethods:
    def test_context_manager(self, db_path: Path) -> None:
        with Store(db_path) as store:
            store.migrate()
            proj_id = store.add_project(Project(name="Context Test"))
            assert proj_id > 0

        with Store(db_path) as store:
            proj = store.get_project(proj_id)
            assert proj is not None
            assert proj.name == "Context Test"

    def test_close(self, store: Store) -> None:
        store.close()
        store.close()

    def test_now_iso(self, store: Store) -> None:
        now = store._now_iso()
        assert isinstance(now, str)
        parsed = datetime.fromisoformat(now)
        assert parsed is not None

    def test_parse_iso(self, store: Store) -> None:
        assert store._parse_iso("2025-01-01T12:00:00") is not None
        assert store._parse_iso(None) is None

    def test_parse_date(self, store: Store) -> None:
        assert store._parse_date("2025-01-01") == date(2025, 1, 1)
        assert store._parse_date(None) is None

    def test_migrate_idempotent(self, store: Store) -> None:
        store.migrate()
        store.migrate()

        proj_id = store.add_project(Project(name="After Double Migrate"))
        assert proj_id > 0