"""Tests for tasq.cli module."""
from __future__ import annotations

import pytest
from click.testing import CliRunner

from tasq.cli import cli


@pytest.fixture
def runner() -> CliRunner:
    return CliRunner()


@pytest.fixture
def tasq_env(monkeypatch, tmp_path) -> None:
    import os
    monkeypatch.setenv("TASQ_HOME", str(tmp_path))


class TestCliHelp:
    def test_root_help(self, runner: CliRunner, tasq_env) -> None:
        result = runner.invoke(cli, ["--help"])
        assert result.exit_code == 0
        assert "task" in result.output
        assert "project" in result.output
        assert "tag" in result.output
        assert "report" in result.output

    def test_task_help(self, runner: CliRunner, tasq_env) -> None:
        result = runner.invoke(cli, ["task", "--help"])
        assert result.exit_code == 0
        assert "add" in result.output
        assert "list" in result.output
        assert "show" in result.output

    def test_project_help(self, runner: CliRunner, tasq_env) -> None:
        result = runner.invoke(cli, ["project", "--help"])
        assert result.exit_code == 0
        assert "add" in result.output
        assert "list" in result.output

    def test_tag_help(self, runner: CliRunner, tasq_env) -> None:
        result = runner.invoke(cli, ["tag", "--help"])
        assert result.exit_code == 0
        assert "list" in result.output
        assert "rename" in result.output

    def test_report_help(self, runner: CliRunner, tasq_env) -> None:
        result = runner.invoke(cli, ["report", "--help"])
        assert result.exit_code == 0
        assert "burndown" in result.output
        assert "stats" in result.output

    def test_import_help(self, runner: CliRunner, tasq_env) -> None:
        result = runner.invoke(cli, ["import", "--help"])
        assert result.exit_code == 0

    def test_export_help(self, runner: CliRunner, tasq_env) -> None:
        result = runner.invoke(cli, ["export", "--help"])
        assert result.exit_code == 0

    def test_config_help(self, runner: CliRunner, tasq_env) -> None:
        result = runner.invoke(cli, ["config", "--help"])
        assert result.exit_code == 0

    def test_shell_help(self, runner: CliRunner, tasq_env) -> None:
        result = runner.invoke(cli, ["shell", "--help"])
        assert result.exit_code == 0


class TestTaskCommands:
    def test_task_add(self, runner: CliRunner, tasq_env) -> None:
        result = runner.invoke(cli, ["task", "add", "Test task"])
        assert result.exit_code == 0
        assert "Created task" in result.output

    def test_task_add_with_priority(self, runner: CliRunner, tasq_env) -> None:
        result = runner.invoke(cli, ["task", "add", "High priority task", "-P", "high"])
        assert result.exit_code == 0
        assert "Created task" in result.output

    def test_task_add_with_tags(self, runner: CliRunner, tasq_env) -> None:
        result = runner.invoke(cli, ["task", "add", "Tagged task", "-t", "bug", "-t", "urgent"])
        assert result.exit_code == 0

    def test_task_add_with_due_date(self, runner: CliRunner, tasq_env) -> None:
        result = runner.invoke(cli, ["task", "add", "Due task", "-d", "2025-12-31"])
        assert result.exit_code == 0

    def test_task_add_invalid_date(self, runner: CliRunner, tasq_env) -> None:
        result = runner.invoke(cli, ["task", "add", "Bad date task", "-d", "invalid"])
        assert result.exit_code != 0

    def test_task_list_empty(self, runner: CliRunner, tasq_env) -> None:
        result = runner.invoke(cli, ["task", "list"])
        assert result.exit_code == 0

    def test_task_list_with_tasks(self, runner: CliRunner, tasq_env) -> None:
        runner.invoke(cli, ["task", "add", "Task 1"])
        runner.invoke(cli, ["task", "add", "Task 2"])

        result = runner.invoke(cli, ["task", "list"])
        assert result.exit_code == 0
        assert "Task 1" in result.output
        assert "Task 2" in result.output

    def test_task_list_filter_status(self, runner: CliRunner, tasq_env) -> None:
        runner.invoke(cli, ["task", "add", "Todo Task", "-s", "todo"])
        runner.invoke(cli, ["task", "add", "Done Task", "-s", "done"])

        result = runner.invoke(cli, ["task", "list", "-s", "todo"])
        assert result.exit_code == 0

    def test_task_show(self, runner: CliRunner, tasq_env) -> None:
        runner.invoke(cli, ["task", "add", "Show Test"])

        result = runner.invoke(cli, ["task", "show", "1"])
        assert result.exit_code == 0
        assert "Show Test" in result.output

    def test_task_show_not_found(self, runner: CliRunner, tasq_env) -> None:
        result = runner.invoke(cli, ["task", "show", "999"])
        assert result.exit_code != 0

    def test_task_done(self, runner: CliRunner, tasq_env) -> None:
        runner.invoke(cli, ["task", "add", "Done Test"])

        result = runner.invoke(cli, ["task", "done", "1"])
        assert result.exit_code == 0
        assert "done" in result.output.lower()

    def test_task_rm(self, runner: CliRunner, tasq_env) -> None:
        runner.invoke(cli, ["task", "add", "Delete Test"])

        result = runner.invoke(cli, ["task", "rm", "1", "--force"])
        assert result.exit_code == 0
        assert "Deleted" in result.output

    def test_task_edit(self, runner: CliRunner, tasq_env) -> None:
        runner.invoke(cli, ["task", "add", "Edit Test"])

        result = runner.invoke(cli, ["task", "edit", "1", "--title", "Updated Title"])
        assert result.exit_code == 0

    def test_task_block(self, runner: CliRunner, tasq_env) -> None:
        runner.invoke(cli, ["task", "add", "Block Test"])
        runner.invoke(cli, ["task", "add", "Blocker"])

        result = runner.invoke(cli, ["task", "block", "1", "--by", "2"])
        assert result.exit_code == 0

    def test_task_deps(self, runner: CliRunner, tasq_env) -> None:
        runner.invoke(cli, ["task", "add", "Dep Test"])
        runner.invoke(cli, ["task", "add", "Blocker"])
        runner.invoke(cli, ["task", "block", "1", "--by", "2"])

        result = runner.invoke(cli, ["task", "deps", "1"])
        assert result.exit_code == 0


class TestProjectCommands:
    def test_project_add(self, runner: CliRunner, tasq_env) -> None:
        result = runner.invoke(cli, ["project", "add", "Web"])
        assert result.exit_code == 0
        assert "Created project" in result.output

    def test_project_list_empty(self, runner: CliRunner, tasq_env) -> None:
        result = runner.invoke(cli, ["project", "list"])
        assert result.exit_code == 0

    def test_project_list(self, runner: CliRunner, tasq_env) -> None:
        runner.invoke(cli, ["project", "add", "Project A"])
        runner.invoke(cli, ["project", "add", "Project B"])

        result = runner.invoke(cli, ["project", "list"])
        assert result.exit_code == 0
        assert "Project A" in result.output
        assert "Project B" in result.output

    def test_project_rename(self, runner: CliRunner, tasq_env) -> None:
        runner.invoke(cli, ["project", "add", "OldName"])

        result = runner.invoke(cli, ["project", "rename", "OldName", "NewName"])
        assert result.exit_code == 0

    def test_project_archive(self, runner: CliRunner, tasq_env) -> None:
        runner.invoke(cli, ["project", "add", "ArchiveMe"])

        result = runner.invoke(cli, ["project", "archive", "ArchiveMe"])
        assert result.exit_code == 0


class TestTagCommands:
    def test_tag_list_empty(self, runner: CliRunner, tasq_env) -> None:
        result = runner.invoke(cli, ["tag", "list"])
        assert result.exit_code == 0

    def test_tag_list_with_tags(self, runner: CliRunner, tasq_env) -> None:
        runner.invoke(cli, ["task", "add", "Test", "-t", "bug"])

        result = runner.invoke(cli, ["tag", "list"])
        assert result.exit_code == 0
        assert "bug" in result.output

    def test_tag_rename(self, runner: CliRunner, tasq_env) -> None:
        runner.invoke(cli, ["task", "add", "Test", "-t", "oldname"])

        result = runner.invoke(cli, ["tag", "rename", "oldname", "newname"])
        assert result.exit_code == 0

    def test_tag_rm(self, runner: CliRunner, tasq_env) -> None:
        runner.invoke(cli, ["task", "add", "Test", "-t", "todelete"])

        result = runner.invoke(cli, ["tag", "rm", "todelete", "--force"])
        assert result.exit_code == 0


class TestReportCommands:
    def test_report_stats(self, runner: CliRunner, tasq_env) -> None:
        result = runner.invoke(cli, ["report", "stats"])
        assert result.exit_code == 0
        assert "Total tasks" in result.output

    def test_report_by_project(self, runner: CliRunner, tasq_env) -> None:
        runner.invoke(cli, ["project", "add", "Web"])

        result = runner.invoke(cli, ["report", "by-project"])
        assert result.exit_code == 0
        assert "Web" in result.output

    def test_report_overdue_empty(self, runner: CliRunner, tasq_env) -> None:
        result = runner.invoke(cli, ["report", "overdue"])
        assert result.exit_code == 0

    def test_report_burndown(self, runner: CliRunner, tasq_env) -> None:
        result = runner.invoke(cli, ["report", "burndown"])
        assert result.exit_code == 0


class TestConfigCommands:
    def test_config_get(self, runner: CliRunner, tasq_env) -> None:
        result = runner.invoke(cli, ["config", "get", "display.date_format"])
        assert result.exit_code == 0

    def test_config_set(self, runner: CliRunner, tasq_env) -> None:
        result = runner.invoke(cli, ["config", "set", "test.key", "test_value"])
        assert result.exit_code == 0

    def test_config_path(self, runner: CliRunner, tasq_env) -> None:
        result = runner.invoke(cli, ["config", "path"])
        assert result.exit_code == 0

    def test_config_list(self, runner: CliRunner, tasq_env) -> None:
        result = runner.invoke(cli, ["config", "list"])
        assert result.exit_code == 0


class TestImportExportCommands:
    def test_export_json(self, runner: CliRunner, tasq_env, tmp_path) -> None:
        runner.invoke(cli, ["task", "add", "Export Test"])

        output = tmp_path / "export.json"
        result = runner.invoke(cli, ["export", "json", "-o", str(output)])
        assert result.exit_code == 0

        assert output.exists()

    def test_import_json(self, runner: CliRunner, tasq_env, tmp_path) -> None:
        json_file = tmp_path / "import.json"
        json_file.write_text('{"tasks": [], "projects": []}')

        result = runner.invoke(cli, ["import", "json", str(json_file)])
        assert result.exit_code == 0