import pytest
from click.testing import CliRunner


@pytest.fixture
def runner():
    return CliRunner()


def test_tasks_group():
    """Test that tasks command group exists"""
    from tasq.cli.tasks import tasks
    assert tasks is not None


def test_projects_group():
    """Test that projects command group exists"""
    from tasq.cli.projects import projects
    assert projects is not None


def test_tags_group():
    """Test that tags command group exists"""
    from tasq.cli.tags import tags
    assert tags is not None


def test_tasks_commands():
    """Test that task subcommands are registered"""
    from tasq.cli.tasks import tasks
    expected = ['add', 'list', 'show', 'done', 'rm', 'edit', 'block', 'deps']
    for cmd in expected:
        assert cmd in tasks.commands, f"Missing command: {cmd}"


def test_projects_commands():
    """Test that project subcommands are registered"""
    from tasq.cli.projects import projects
    expected = ['add', 'list', 'rename', 'archive']
    for cmd in expected:
        assert cmd in projects.commands, f"Missing command: {cmd}"


def test_tags_commands():
    """Test that tag subcommands are registered"""
    from tasq.cli.tags import tags
    expected = ['list', 'rename', 'rm']
    for cmd in expected:
        assert cmd in tags.commands, f"Missing command: {cmd}"


def test_add_command_help(runner):
    """Test that 'tasq task add --help' works"""
    from tasq.cli.tasks import add
    result = runner.invoke(add, ['--help'])
    assert result.exit_code == 0
    assert 'Add a new task' in result.output


def test_list_command_help(runner):
    """Test that 'tasq task list --help' works"""
    from tasq.cli.tasks import list
    result = runner.invoke(list, ['--help'])
    assert result.exit_code == 0
    assert 'List tasks' in result.output


def test_show_command_help(runner):
    """Test that 'tasq task show --help' works"""
    from tasq.cli.tasks import show
    result = runner.invoke(show, ['--help'])
    assert result.exit_code == 0
    assert 'Show task details' in result.output


def test_done_command_help(runner):
    """Test that 'tasq task done --help' works"""
    from tasq.cli.tasks import done
    result = runner.invoke(done, ['--help'])
    assert result.exit_code == 0
    assert 'Mark task as done' in result.output


def test_rm_command_help(runner):
    """Test that 'tasq task rm --help' works"""
    from tasq.cli.tasks import rm
    result = runner.invoke(rm, ['--help'])
    assert result.exit_code == 0
    assert 'Remove a task' in result.output


def test_edit_command_help(runner):
    """Test that 'tasq task edit --help' works"""
    from tasq.cli.tasks import edit
    result = runner.invoke(edit, ['--help'])
    assert result.exit_code == 0
    assert 'Edit a task' in result.output


def test_block_command_help(runner):
    """Test that 'tasq task block --help' works"""
    from tasq.cli.tasks import block
    result = runner.invoke(block, ['--help'])
    assert result.exit_code == 0
    assert 'Mark task as blocked' in result.output


def test_deps_command_help(runner):
    """Test that 'tasq task deps --help' works"""
    from tasq.cli.tasks import deps
    result = runner.invoke(deps, ['--help'])
    assert result.exit_code == 0
    assert 'Show blockers' in result.output


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


def test_tag_list_help(runner):
    """Test that 'tasq tag list --help' works"""
    from tasq.cli.tags import list
    result = runner.invoke(list, ['--help'])
    assert result.exit_code == 0
    assert 'List all tags' in result.output
