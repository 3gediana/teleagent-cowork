import pytest
from unittest.mock import MagicMock, patch
from click.testing import CliRunner
from tasq.cli.tags import tags, list, rename, rm


@pytest.fixture
def mock_store():
    store = MagicMock()
    return store


@pytest.fixture
def runner():
    return CliRunner()


def test_tags_group():
    """Test that tags command group exists"""
    @tags.command(name="test")
    def test_cmd():
        pass
    assert "test" in tags.commands


def test_list_tags_empty(runner, mock_store):
    """Test list command with no tags"""
    with patch("tasq.cli.tags._resolve_store", return_value=mock_store):
        mock_store.list_tags.return_value = []

        result = runner.invoke(list, [])
        assert result.exit_code == 0
        assert "No tags found" in result.output


def test_list_tags(runner, mock_store):
    """Test list command with tags"""
    with patch("tasq.cli.tags._resolve_store", return_value=mock_store):
        mock_tag = MagicMock()
        mock_tag.id = 1
        mock_tag.name = "bug"
        mock_store.list_tags.return_value = [mock_tag]

        result = runner.invoke(list, [])
        assert result.exit_code == 0
        assert "1: bug" in result.output


def test_rename_tag(runner, mock_store):
    """Test rename command"""
    with patch("tasq.cli.tags._resolve_store", return_value=mock_store):
        mock_tag = MagicMock()
        mock_tag.id = 1
        mock_tag.name = "old"
        mock_store.get_tag_by_name.return_value = mock_tag

        result = runner.invoke(rename, ["old", "new"])
        assert result.exit_code == 0
        assert "Renamed" in result.output
        mock_store.update_tag.assert_called_with(1, name="new")


def test_rename_tag_not_found(runner, mock_store):
    """Test rename with invalid tag"""
    with patch("tasq.cli.tags._resolve_store", return_value=mock_store):
        mock_store.get_tag_by_name.return_value = None

        result = runner.invoke(rename, ["nonexistent", "new"])
        assert result.exit_code == 0
        assert "not found" in result.output


def test_rm_tag(runner, mock_store):
    """Test rm command"""
    with patch("tasq.cli.tags._resolve_store", return_value=mock_store):
        mock_tag = MagicMock()
        mock_tag.id = 1
        mock_tag.name = "bug"
        mock_store.get_tag_by_name.return_value = mock_tag

        result = runner.invoke(rm, ["bug"])
        assert result.exit_code == 0
        assert "deleted" in result.output
        mock_store.delete_tag.assert_called_with(1)


def test_rm_tag_not_found(runner, mock_store):
    """Test rm with invalid tag"""
    with patch("tasq.cli.tags._resolve_store", return_value=mock_store):
        mock_store.get_tag_by_name.return_value = None

        result = runner.invoke(rm, ["nonexistent"])
        assert result.exit_code == 0
        assert "not found" in result.output
