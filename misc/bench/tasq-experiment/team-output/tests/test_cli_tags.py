import pytest
from click.testing import CliRunner


@pytest.fixture
def runner():
    return CliRunner()


def test_tags_group():
    """Test that tags command group exists"""
    from tasq.cli.tags import tags
    assert tags is not None


def test_tag_list_help(runner):
    """Test that 'tasq tag list --help' works"""
    from tasq.cli.tags import list
    result = runner.invoke(list, ['--help'])
    assert result.exit_code == 0
    assert 'List all tags' in result.output


def test_tag_rename_help(runner):
    """Test that 'tasq tag rename --help' works"""
    from tasq.cli.tags import rename
    result = runner.invoke(rename, ['--help'])
    assert result.exit_code == 0
    assert 'Rename a tag' in result.output


def test_tag_rm_help(runner):
    """Test that 'tasq tag rm --help' works"""
    from tasq.cli.tags import rm
    result = runner.invoke(rm, ['--help'])
    assert result.exit_code == 0
    assert 'Remove a tag' in result.output
