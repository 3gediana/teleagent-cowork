import pytest
from datetime import date, datetime, timedelta
from unittest.mock import patch, MagicMock

from tasq.cli.reports import burndown, by_project, overdue, stats


@pytest.fixture
def mock_store():
    with patch("tasq.cli.reports.Store") as mock:
        instance = MagicMock()
        instance.migrate.return_value = None
        mock.return_value = instance
        yield instance


@pytest.fixture
def sample_tasks():
    from tasq.models import Task
    now = datetime.now()
    today = date.today()
    return [
        Task(id=1, title="Task 1", description=None, priority="high", status="done",
             project_id=1, due_date=today - timedelta(days=1), created_at=now,
             updated_at=now, completed_at=now, tags=["bug"]),
        Task(id=2, title="Task 2", description=None, priority="medium", status="todo",
             project_id=1, due_date=today + timedelta(days=5), created_at=now,
             updated_at=now, completed_at=None, tags=[]),
        Task(id=3, title="Task 3", description=None, priority="low", status="in_progress",
             project_id=2, due_date=today - timedelta(days=2), created_at=now,
             updated_at=now, completed_at=None, tags=["feature"]),
        Task(id=4, title="Task 4", description=None, priority="urgent", status="blocked",
             project_id=None, due_date=today - timedelta(days=10), created_at=now,
             updated_at=now, completed_at=None, tags=[]),
        Task(id=5, title="Task 5", description=None, priority="high", status="done",
             project_id=2, due_date=today + timedelta(days=1), created_at=now,
             updated_at=now, completed_at=now, tags=["review"]),
    ]


def test_burndown_all_tasks(mock_store, sample_tasks):
    mock_store.list_tasks.return_value = sample_tasks
    mock_store.list_projects.return_value = []

    result = mock_store.list_tasks()
    assert len(result) == 5
    done = [t for t in result if t.status == "done"]
    assert len(done) == 2


def test_burndown_filtered_by_project(mock_store, sample_tasks):
    from tasq.models import Project
    mock_store.list_tasks.return_value = sample_tasks
    mock_store.list_projects.return_value = [
        Project(id=1, name="web", description=None, created_at=datetime.now()),
        Project(id=2, name="infra", description=None, created_at=datetime.now()),
    ]

    result = mock_store.list_tasks()
    web_tasks = [t for t in result if t.project_id == 1]
    assert len(web_tasks) == 2


def test_by_project_groups_correctly(mock_store, sample_tasks):
    from tasq.models import Project
    mock_store.list_tasks.return_value = sample_tasks
    mock_store.list_projects.return_value = [
        Project(id=1, name="web", description=None, created_at=datetime.now()),
        Project(id=2, name="infra", description=None, created_at=datetime.now()),
    ]

    tasks = mock_store.list_tasks()
    counts = {}
    for t in tasks:
        if t.project_id not in counts:
            counts[t.project_id] = 0
        counts[t.project_id] += 1
    assert counts.get(1) == 2
    assert counts.get(2) == 2


def test_overdue_filters_correctly(mock_store, sample_tasks):
    mock_store.list_tasks.return_value = sample_tasks
    today = date.today()
    overdue = [t for t in sample_tasks if t.due_date and t.due_date < today and t.status not in ("done", "cancelled")]
    assert len(overdue) == 2


def test_stats_counts_by_status(mock_store, sample_tasks):
    mock_store.list_tasks.return_value = sample_tasks
    tasks = sample_tasks
    counts = {s: sum(1 for t in tasks if t.status == s) for s in ["todo", "in_progress", "blocked", "done", "cancelled"]}
    assert counts["done"] == 2
    assert counts["todo"] == 1
    assert counts["in_progress"] == 1
    assert counts["blocked"] == 1