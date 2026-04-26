import pytest
from unittest.mock import patch, MagicMock
from datetime import datetime

from tasq.cli.shell import _status_icon, _cmd_list, _cmd_add, _cmd_show, _cmd_done, _cmd_rm


@pytest.fixture
def mock_store():
    store = MagicMock()
    store.migrate.return_value = None
    return store


@pytest.fixture
def sample_tasks():
    from tasq.models import Task
    now = datetime.now()
    return [
        Task(id=1, title="Task One", description=None, priority="high",
             status="todo", project_id=None, due_date=None,
             created_at=now, updated_at=now, completed_at=None, tags=[]),
        Task(id=2, title="Task Two", description="Desc", priority="medium",
             status="done", project_id=None, due_date=None,
             created_at=now, updated_at=now, completed_at=now, tags=["review"]),
    ]


class TestStatusIcon:
    def test_todo_icon(self):
        assert _status_icon("todo") == "[ ]"

    def test_done_icon(self):
        assert _status_icon("done") == "[x]"

    def test_in_progress_icon(self):
        assert _status_icon("in_progress") == "[*]"

    def test_blocked_icon(self):
        assert _status_icon("blocked") == "[!]"

    def test_cancelled_icon(self):
        assert _status_icon("cancelled") == "[-]"

    def test_unknown_icon(self):
        assert _status_icon("unknown") == "[?]"


class TestCmdList:
    def test_list_empty(self, mock_store):
        mock_store.list_tasks.return_value = []
        _cmd_list(mock_store, [])
        mock_store.list_tasks.assert_called_once()

    def test_list_with_tasks(self, mock_store, sample_tasks):
        mock_store.list_tasks.return_value = sample_tasks
        _cmd_list(mock_store, [])
        mock_store.list_tasks.assert_called_once()


class TestCmdAdd:
    def test_add_requires_title(self, mock_store):
        _cmd_add(mock_store, [])
        mock_store.add_task.assert_not_called()

    def test_add_creates_task(self, mock_store):
        mock_store.add_task.return_value = 1
        _cmd_add(mock_store, ["Test Task"])
        mock_store.add_task.assert_called_once()
        call_args = mock_store.add_task.call_args[0][0]
        assert call_args.title == "Test Task"
        assert call_args.priority == "medium"


class TestCmdShow:
    def test_show_nonexistent(self, mock_store):
        mock_store.get_task.return_value = None
        _cmd_show(mock_store, ["999"])
        mock_store.get_task.assert_called_once_with(999)

    def test_show_invalid_id(self, mock_store):
        _cmd_show(mock_store, ["abc"])
        mock_store.get_task.assert_not_called()


class TestCmdDone:
    def test_done_nonexistent(self, mock_store):
        mock_store.get_task.return_value = None
        _cmd_done(mock_store, ["999"])
        mock_store.update_task.assert_not_called()

    def test_done_updates_status(self, mock_store, sample_tasks):
        mock_store.get_task.return_value = sample_tasks[0]
        _cmd_done(mock_store, ["1"])
        mock_store.update_task.assert_called_once_with(1, status="done")


class TestCmdRm:
    def test_rm_deletes_task(self, mock_store):
        _cmd_rm(mock_store, ["1"])
        mock_store.delete_task.assert_called_once_with(1)