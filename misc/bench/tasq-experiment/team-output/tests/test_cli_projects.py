import pytest
from click.testing import CliRunner


@pytest.fixture
def runner():
    return CliRunner()


def test_projects_group():
    """Test that projects command group exists"""
    from tasq.cli.projects import projects
    assert projects is not None


def test_project_add_help(runner):
    """Test that 'tasq project add --help' works"""
    from tasq.cli.projects import add
    result = runner.invoke(add, ['--help'])
    assert result.exit_code == 0
    assert 'Add a new project' in result.output


def test_project_list_help(runner):
    """Test that 'tasq project list --help' works"""
    from tasq.cli.projects import list
    result = runner.invoke(list, ['--help'])
    assert result.exit_code == 0
    assert 'List all projects' in result.output


def test_project_rename_help(runner):
    """Test that 'tasq project rename --help' works"""
    from tasq.cli.projects import rename
    result = runner.invoke(rename, ['--help'])
    assert result.exit_code == 0
    assert 'Rename a project' in result.output


def test_project_archive_help(runner):
    """Test that 'tasq project archive --help' works"""
    from tasq.cli.projects import archive
    result = runner.invoke(archive, ['--help'])
    assert result.exit_code == 0
    assert 'Archive a project' in result.output
