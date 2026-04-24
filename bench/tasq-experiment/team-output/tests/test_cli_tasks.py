import pytest
from unittest.mock import MagicMock, patch
from click.testing import CliRunner
from tasq.cli.tasks import tasks, add, list, show, done, rm, edit, block, deps


@pytest.fixture
def mock_store():
    store = MagicMock()
    return store


@pytest.fixture
def runner():
    return CliRunner()


def test_tasks_group():
    """Test that tasks command group exists"""
    @tasks.command(name="test")
    def test_cmd():
        pass
    assert "test" in tasks.commands


def test_add_task(runner, mock_store):
    """Test task add command"""
    with patch("tasq.cli.tasks._resolve_store", return_value=mock_store):
        mock_store.add_task.return_value = 1

        result = runner.invoke(add, ["Test Task", "-P", "high"])
        assert result.exit_code == 0
        assert "Created task 1: Test Task" in result.output
        mock_store.add_task.assert_called_once()


def test_add_task_with_project(runner, mock_store):
    """Test task add with project"""
    with patch("tasq.cli.tasks._resolve_store", return_value=mock_store):
        mock_project = MagicMock()
        mock_project.id = 2
        mock_store.get_project_by_name.return_value = mock_project
        mock_store.add_task.return_value = 3

        result = runner.invoke(add, ["My Task", "-p", "web", "-P", "urgent"])
        assert result.exit_code == 0
        mock_store.get_project_by_name.assert_called_with("web")


def test_add_task_with_tags(runner, mock_store):
    """Test task add with tags"""
    with patch("tasq.cli.tasks._resolve_store", return_value=mock_store):
        mock_store.add_task.return_value = 1

        result = runner.invoke(add, ["Task", "-t", "bug", "-t", "frontend"])
        assert result.exit_code == 0


def test_list_tasks_empty(runner, mock_store):
    """Test list command with no tasks"""
    with patch("tasq.cli.tasks._resolve_store", return_value=mock_store):
        mock_store.list_tasks.return_value = []

        result = runner.invoke(list, [])
        assert result.exit_code == 0
        assert "No tasks found" in result.output


def test_list_tasks(runner, mock_store):
    """Test list command with tasks"""
    with patch("tasq.cli.tasks._resolve_store", return_value=mock_store):
        mock_task = MagicMock()
        mock_task.id = 1
        mock_task.title = "Test"
        mock_task.status = "todo"
        mock_task.due_date = None
        mock_task.project_id = None
        mock_task.tags = []
        mock_store.list_tasks.return_value = [mock_task]

        result = runner.invoke(list, [])
        assert result.exit_code == 0
        assert "1: [ ] Test" in result.output


def test_list_tasks_with_project_filter(runner, mock_store):
    """Test list with project filter"""
    with patch("tasq.cli.tasks._resolve_store", return_value=mock_store):
        mock_project = MagicMock()
        mock_project.id = 2
        mock_project.name = "web"
        mock_store.get_project_by_name.return_value = mock_project
        mock_store.list_tasks.return_value = []

        result = runner.invoke(list, ["-p", "web"])
        assert result.exit_code == 0
        mock_store.list_tasks.assert_called_with(project_id=2)


def test_list_tasks_with_status_filter(runner, mock_store):
    """Test list with status filter"""
    with patch("tasq.cli.tasks._resolve_store", return_value=mock_store):
        mock_store.list_tasks.return_value = []

        result = runner.invoke(list, ["-s", "done"])
        assert result.exit_code == 0
        mock_store.list_tasks.assert_called_with(status="done")


def test_show_task(runner, mock_store):
    """Test show command"""
    with patch("tasq.cli.tasks._resolve_store", return_value=mock_store):
        from datetime import datetime
        mock_task = MagicMock()
        mock_task.id = 1
        mock_task.title = "Test Task"
        mock_task.description = "A test"
        mock_task.status = "todo"
        mock_task.priority = "high"
        mock_task.due_date = None
        mock_task.project_id = None
        mock_task.tags = ["bug"]
        mock_task.created_at = datetime(2024, 1, 1, 12, 0, 0)
        mock_task.updated_at = datetime(2024, 1, 1, 12, 0, 0)
        mock_task.completed_at = None
        mock_store.get_task.return_value = mock_task

        result = runner.invoke(show, [1])
        assert result.exit_code == 0
        assert "Title:   Test Task" in result.output


def test_show_task_not_found(runner, mock_store):
    """Test show command with invalid id"""
    with patch("tasq.cli.tasks._resolve_store", return_value=mock_store):
        mock_store.get_task.return_value = None

        result = runner.invoke(show, [999])
        assert result.exit_code == 0
        assert "not found" in result.output


def test_done_task(runner, mock_store):
    """Test done command"""
    with patch("tasq.cli.tasks._resolve_store", return_value=mock_store):
        mock_task = MagicMock()
        mock_task.id = 1
        mock_store.get_task.return_value = mock_task

        result = runner.invoke(done, [1])
        assert result.exit_code == 0
        assert "marked as done" in result.output
        mock_store.update_task.assert_called()


def test_rm_task(runner, mock_store):
    """Test rm command"""
    with patch("tasq.cli.tasks._resolve_store", return_value=mock_store):
        mock_task = MagicMock()
        mock_task.id = 1
        mock_store.get_task.return_value = mock_task

        result = runner.invoke(rm, [1])
        assert result.exit_code == 0
        assert "deleted" in result.output
        mock_store.delete_task.assert_called_with(1)


def test_edit_task(runner, mock_store):
    """Test edit command"""
    with patch("tasq.cli.tasks._resolve_store", return_value=mock_store):
        mock_task = MagicMock()
        mock_task.id = 1
        mock_store.get_task.return_value = mock_task

        result = runner.invoke(edit, [1, "--title", "New Title"])
        assert result.exit_code == 0
        assert "updated" in result.output
        mock_store.update_task.assert_called()


def test_edit_task_priority(runner, mock_store):
    """Test edit command with priority"""
    with patch("tasq.cli.tasks._resolve_store", return_value=mock_store):
        mock_task = MagicMock()
        mock_task.id = 1
        mock_store.get_task.return_value = mock_task

        result = runner.invoke(edit, [1, "--priority", "urgent"])
        assert result.exit_code == 0


def test_block_task(runner, mock_store):
    """Test block command"""
    with patch("tasq.cli.tasks._resolve_store", return_value=mock_store):
        mock_task = MagicMock()
        mock_task.id = 1
        mock_store.get_task.return_value = mock_task

        result = runner.invoke(block, [1])
        assert result.exit_code == 0
        assert "blocked" in result.output


def test_block_task_with_dep(runner, mock_store):
    """Test block with dependency"""
    with patch("tasq.cli.tasks._resolve_store", return_value=mock_store):
        mock_task = MagicMock()
        mock_task.id = 1
        mock_dep = MagicMock()
        mock_dep.id = 2
        mock_store.get_task.side_effect = [mock_task, mock_dep]

        result = runner.invoke(block, [1, "--by", "2"])
        assert result.exit_code == 0
        mock_store.add_dependency.assert_called_with(1, 2)


def test_deps_task(runner, mock_store):
    """Test deps command"""
    with patch("tasq.cli.tasks._resolve_store", return_value=mock_store):
        mock_task = MagicMock()
        mock_task.id = 1
        mock_store.get_task.return_value = mock_task
        mock_store.get_blockers.return_value = []
        mock_store.get_dependents.return_value = []

        result = runner.invoke(deps, [1])
        assert result.exit_code == 0
        assert "No blockers" in result.output


def test_deps_task_with_blockers(runner, mock_store):
    """Test deps with blockers"""
    with patch("tasq.cli.tasks._resolve_store", return_value=mock_store):
        mock_task = MagicMock()
        mock_task.id = 1
        mock_blocker = MagicMock()
        mock_blocker.id = 2
        mock_blocker.title = "Blocker Task"
        mock_store.get_task.return_value = mock_task
        mock_store.get_blockers.return_value = [mock_blocker]
        mock_store.get_dependents.return_value = []

        result = runner.invoke(deps, [1])
        assert result.exit_code == 0
        assert "Blocker Task" in result.output
