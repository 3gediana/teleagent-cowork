from __future__ import annotations

import sys
import tempfile
from datetime import date, datetime
from pathlib import Path

import pytest

sys.path.insert(0, str(Path(__file__).parent.parent))

from tasq.db import SCHEMA, Store
from tasq.models import Project, Task, Tag


@pytest.fixture
def db_path(tmp_path: Path) -> Path:
    return tmp_path / "test.db"


@pytest.fixture
def store(db_path: Path) -> Store:
    s = Store(db_path)
    s.connect()
    s.migrate()
    return s


class TestStoreInit:
    def test_init_stores_db_path(self, db_path: Path) -> None:
        store = Store(db_path)
        assert store.db_path == db_path

    def test_connect_creates_connection(self, store: Store) -> None:
        assert store.conn is not None

    def test_context_manager(self, db_path: Path) -> None:
        with Store(db_path) as s:
            s.migrate()
            assert s.conn is not None


class TestMigrate:
    def test_migrate_creates_tables(self, store: Store, db_path: Path) -> None:
        conn = store.conn
        assert conn is not None
        cursor = conn.execute("SELECT name FROM sqlite_master WHERE type='table'")
        tables = {row["name"] for row in cursor.fetchall()}
        assert "tasks" in tables
        assert "projects" in tables
        assert "tags" in tables
        assert "task_tags" in tables
        assert "task_deps" in tables


class TestTaskCrud:
    def test_add_and_get_task(self, store: Store) -> None:
        task = Task(title="Test task", priority="high", status="todo")
        task_id = store.add_task(task)
        assert task_id > 0
        fetched = store.get_task(task_id)
        assert fetched is not None
        assert fetched.title == "Test task"
        assert fetched.priority == "high"
        assert fetched.status == "todo"

    def test_add_task_with_tags(self, store: Store) -> None:
        task = Task(title="Tagged task", priority="medium", status="todo", tags=["urgent", "frontend"])
        task_id = store.add_task(task)
        fetched = store.get_task(task_id)
        assert fetched is not None
        assert "urgent" in fetched.tags
        assert "frontend" in fetched.tags

    def test_get_nonexistent_task(self, store: Store) -> None:
        result = store.get_task(9999)
        assert result is None

    def test_update_task(self, store: Store) -> None:
        task = Task(title="Original", priority="low", status="todo")
        task_id = store.add_task(task)
        store.update_task(task_id, title="Updated", priority="high")
        updated = store.get_task(task_id)
        assert updated is not None
        assert updated.title == "Updated"
        assert updated.priority == "high"

    def test_update_task_status_to_done_sets_completed_at(self, store: Store) -> None:
        task = Task(title="To complete", priority="medium", status="todo")
        task_id = store.add_task(task)
        store.update_task(task_id, status="done")
        updated = store.get_task(task_id)
        assert updated is not None
        assert updated.status == "done"
        assert updated.completed_at is not None

    def test_delete_task(self, store: Store) -> None:
        task = Task(title="To delete", priority="low", status="todo")
        task_id = store.add_task(task)
        store.delete_task(task_id)
        result = store.get_task(task_id)
        assert result is None


class TestListTasks:
    def test_list_all_tasks(self, store: Store) -> None:
        for i in range(3):
            store.add_task(Task(title=f"Task {i}", priority="medium", status="todo"))
        tasks = store.list_tasks()
        assert len(tasks) == 3

    def test_filter_by_status(self, store: Store) -> None:
        store.add_task(Task(title="Todo task", priority="medium", status="todo"))
        store.add_task(Task(title="Done task", priority="medium", status="done"))
        todos = store.list_tasks(status="todo")
        assert len(todos) == 1
        assert todos[0].title == "Todo task"

    def test_filter_by_project(self, store: Store) -> None:
        proj = Project(name="test_proj")
        proj_id = store.add_project(proj)
        store.add_task(Task(title="Unlinked", priority="medium", status="todo"))
        store.add_task(Task(title="Linked", priority="medium", status="todo", project_id=proj_id))
        tasks = store.list_tasks(project_id=proj_id)
        assert len(tasks) == 1
        assert tasks[0].title == "Linked"

    def test_filter_by_tag(self, store: Store) -> None:
        store.add_task(Task(title="Has tag", priority="medium", status="todo", tags=["python"]))
        store.add_task(Task(title="No tag", priority="medium", status="todo"))
        tasks = store.list_tasks(tag="python")
        assert len(tasks) == 1
        assert tasks[0].title == "Has tag"

    def test_filter_overdue(self, store: Store) -> None:
        past = date.today().isoformat()
        store.add_task(Task(title="Overdue", priority="medium", status="todo", due_date=date(2020, 1, 1)))
        store.add_task(Task(title="Future", priority="medium", status="todo", due_date=date(2099, 1, 1)))
        overdue = store.list_tasks(overdue=True)
        assert len(overdue) == 1
        assert overdue[0].title == "Overdue"


class TestProjectCrud:
    def test_add_and_get_project(self, store: Store) -> None:
        proj = Project(name="web", description="Web project")
        proj_id = store.add_project(proj)
        assert proj_id > 0
        fetched = store.get_project(proj_id)
        assert fetched is not None
        assert fetched.name == "web"

    def test_get_project_by_name(self, store: Store) -> None:
        proj = Project(name="unique_proj")
        store.add_project(proj)
        fetched = store.get_project_by_name("unique_proj")
        assert fetched is not None
        assert fetched.name == "unique_proj"

    def test_list_projects(self, store: Store) -> None:
        store.add_project(Project(name="a"))
        store.add_project(Project(name="b"))
        projects = store.list_projects()
        assert len(projects) == 2

    def test_update_project(self, store: Store) -> None:
        proj = Project(name="old_name", description="old desc")
        proj_id = store.add_project(proj)
        store.update_project(proj_id, name="new_name", description="new desc")
        updated = store.get_project(proj_id)
        assert updated is not None
        assert updated.name == "new_name"
        assert updated.description == "new desc"

    def test_delete_project(self, store: Store) -> None:
        proj = Project(name="to_delete")
        proj_id = store.add_project(proj)
        store.delete_project(proj_id)
        result = store.get_project(proj_id)
        assert result is None


class TestTagCrud:
    def test_add_and_get_tag(self, store: Store) -> None:
        tag = Tag(name="bug")
        tag_id = store.add_tag(tag)
        assert tag_id > 0
        fetched = store.get_tag(tag_id)
        assert fetched is not None
        assert fetched.name == "bug"

    def test_get_tag_by_name(self, store: Store) -> None:
        tag = Tag(name="feature")
        store.add_tag(tag)
        fetched = store.get_tag_by_name("feature")
        assert fetched is not None
        assert fetched.name == "feature"

    def test_list_tags(self, store: Store) -> None:
        store.add_tag(Tag(name="a"))
        store.add_tag(Tag(name="b"))
        tags = store.list_tags()
        assert len(tags) == 2

    def test_delete_tag(self, store: Store) -> None:
        tag = Tag(name="obsolete")
        store.add_tag(tag)
        store.delete_tag("obsolete")
        fetched = store.get_tag_by_name("obsolete")
        assert fetched is None


class TestDependencies:
    def test_add_dependency(self, store: Store) -> None:
        t1 = store.add_task(Task(title="Task 1", priority="medium", status="todo"))
        t2 = store.add_task(Task(title="Task 2", priority="medium", status="todo"))
        store.add_dependency(t2, t1)
        blockers = store.get_blockers(t2)
        assert len(blockers) == 1
        assert blockers[0].id == t1

    def test_get_dependants(self, store: Store) -> None:
        t1 = store.add_task(Task(title="Task 1", priority="medium", status="todo"))
        t2 = store.add_task(Task(title="Task 2", priority="medium", status="todo"))
        store.add_dependency(t2, t1)
        deps = store.get_dependants(t1)
        assert len(deps) == 1
        assert deps[0].id == t2

    def test_remove_dependency(self, store: Store) -> None:
        t1 = store.add_task(Task(title="Task 1", priority="medium", status="todo"))
        t2 = store.add_task(Task(title="Task 2", priority="medium", status="todo"))
        store.add_dependency(t2, t1)
        store.remove_dependency(t2, t1)
        blockers = store.get_blockers(t2)
        assert len(blockers) == 0


class TestImportExport:
    def test_get_all_tasks_for_export(self, store: Store) -> None:
        store.add_task(Task(title="Export 1", priority="high", status="done"))
        store.add_task(Task(title="Export 2", priority="low", status="todo"))
        tasks = store.get_all_tasks_for_export()
        assert len(tasks) == 2

    def test_import_tasks(self, store: Store) -> None:
        tasks = [
            Task(title="Imported 1", priority="high", status="todo"),
            Task(title="Imported 2", priority="low", status="done"),
        ]
        store.import_tasks(tasks)
        all_tasks = store.get_all_tasks_for_export()
        assert len(all_tasks) == 2
        titles = {t.title for t in all_tasks}
        assert "Imported 1" in titles
        assert "Imported 2" in titles

    def test_clear_all(self, store: Store) -> None:
        store.add_task(Task(title="Test", priority="medium", status="todo"))
        store.add_project(Project(name="Test"))
        store.clear_all()
        assert len(store.list_tasks()) == 0
        assert len(store.list_projects()) == 0