import pytest
from unittest.mock import MagicMock, patch
from click.testing import CliRunner
from tasq.cli.projects import projects, add, list, rename, archive


@pytest.fixture
def mock_store():
    store = MagicMock()
    return store


@pytest.fixture
def runner():
    return CliRunner()


def test_projects_group():
    """Test that projects command group exists"""
    @projects.command(name="test")
    def test_cmd():
        pass
    assert "test" in projects.commands


def test_add_project(runner, mock_store):
    """Test project add command"""
    with patch("tasq.cli.projects._resolve_store", return_value=mock_store):
        mock_store.add_project.return_value = 1

        result = runner.invoke(add, ["web"])
        assert result.exit_code == 0
        assert "Created project 1: web" in result.output
        mock_store.add_project.assert_called_once()


def test_add_project_with_description(runner, mock_store):
    """Test project add with description"""
    with patch("tasq.cli.projects._resolve_store", return_value=mock_store):
        mock_store.add_project.return_value = 1

        result = runner.invoke(add, ["web", "-d", "Web frontend"])
        assert result.exit_code == 0


def test_list_projects_empty(runner, mock_store):
    """Test list command with no projects"""
    with patch("tasq.cli.projects._resolve_store", return_value=mock_store):
        mock_store.list_projects.return_value = []

        result = runner.invoke(list, [])
        assert result.exit_code == 0
        assert "No projects found" in result.output


def test_list_projects(runner, mock_store):
    """Test list command with projects"""
    with patch("tasq.cli.projects._resolve_store", return_value=mock_store):
        mock_project = MagicMock()
        mock_project.id = 1
        mock_project.name = "web"
        mock_project.description = "Web frontend"
        mock_project.archived = False
        mock_store.list_projects.return_value = [mock_project]

        result = runner.invoke(list, [])
        assert result.exit_code == 0
        assert "1: web" in result.output
        assert "Web frontend" in result.output


def test_list_projects_archived(runner, mock_store):
    """Test list command with archived project"""
    with patch("tasq.cli.projects._resolve_store", return_value=mock_store):
        mock_project = MagicMock()
        mock_project.id = 1
        mock_project.name = "old"
        mock_project.description = None
        mock_project.archived = True
        mock_store.list_projects.return_value = [mock_project]

        result = runner.invoke(list, [])
        assert result.exit_code == 0
        assert "(archived)" in result.output


def test_rename_project(runner, mock_store):
    """Test rename command"""
    with patch("tasq.cli.projects._resolve_store", return_value=mock_store):
        mock_project = MagicMock()
        mock_project.id = 1
        mock_project.name = "old"
        mock_store.get_project_by_name.return_value = mock_project

        result = runner.invoke(rename, ["old", "new"])
        assert result.exit_code == 0
        assert "Renamed" in result.output
        mock_store.update_project.assert_called_with(1, name="new")


def test_rename_project_not_found(runner, mock_store):
    """Test rename with invalid project"""
    with patch("tasq.cli.projects._resolve_store", return_value=mock_store):
        mock_store.get_project_by_name.return_value = None

        result = runner.invoke(rename, ["nonexistent", "new"])
        assert result.exit_code == 0
        assert "not found" in result.output


def test_archive_project(runner, mock_store):
    """Test archive command"""
    with patch("tasq.cli.projects._resolve_store", return_value=mock_store):
        mock_project = MagicMock()
        mock_project.id = 1
        mock_project.name = "old"
        mock_store.get_project_by_name.return_value = mock_project

        result = runner.invoke(archive, ["old"])
        assert result.exit_code == 0
        assert "archived" in result.output
        mock_store.update_project.assert_called_with(1, archived=True)


def test_archive_project_not_found(runner, mock_store):
    """Test archive with invalid project"""
    with patch("tasq.cli.projects._resolve_store", return_value=mock_store):
        mock_store.get_project_by_name.return_value = None

        result = runner.invoke(archive, ["nonexistent"])
        assert result.exit_code == 0
        assert "not found" in result.output
