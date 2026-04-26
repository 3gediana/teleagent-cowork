import json
import csv
import pytest
from pathlib import Path
from unittest.mock import patch, MagicMock
from datetime import date, datetime

from tasq.cli.io import _export_json, _export_csv, _export_markdown, _import_json, _import_csv


@pytest.fixture
def sample_tasks():
    from tasq.models import Task
    now = datetime.now()
    return [
        Task(id=1, title="Task One", description="Description one", priority="high",
             status="todo", project_id=1, due_date=date(2025, 1, 15),
             created_at=now, updated_at=now, completed_at=None, tags=["bug", "urgent"]),
        Task(id=2, title="Task Two", description=None, priority="medium",
             status="done", project_id=1, due_date=date(2025, 1, 10),
             created_at=now, updated_at=now, completed_at=now, tags=["review"]),
    ]


class TestExportJson:
    def test_export_json_format(self, sample_tasks):
        result = _export_json(sample_tasks)
        data = json.loads(result)
        assert "tasks" in data
        assert len(data["tasks"]) == 2
        assert data["tasks"][0]["title"] == "Task One"
        assert data["tasks"][0]["tags"] == ["bug", "urgent"]

    def test_export_json_serializes_dates(self, sample_tasks):
        result = _export_json(sample_tasks)
        data = json.loads(result)
        assert data["tasks"][0]["due_date"] == "2025-01-15"
        assert data["tasks"][0]["completed_at"] is None

    def test_export_json_empty_list(self):
        result = _export_json([])
        data = json.loads(result)
        assert data["tasks"] == []


class TestExportCsv:
    def test_export_csv_headers(self, sample_tasks):
        result = _export_csv(sample_tasks)
        lines = result.strip().split("\n")
        reader = csv.DictReader(lines)
        headers = reader.fieldnames
        assert "title" in headers
        assert "priority" in headers
        assert "status" in headers
        assert "tags" in headers

    def test_export_csv_includes_tags(self, sample_tasks):
        result = _export_csv(sample_tasks)
        lines = result.strip().split("\n")
        reader = csv.DictReader(lines)
        rows = list(reader)
        assert rows[0]["tags"] == "bug|urgent"
        assert rows[1]["tags"] == "review"


class TestExportMarkdown:
    def test_export_markdown_format(self, sample_tasks):
        result = _export_markdown(sample_tasks)
        assert "# Tasks" in result
        assert "## Task One" in result
        assert "**Status**: todo" in result
        assert "**Priority**: high" in result

    def test_export_markdown_includes_tags(self, sample_tasks):
        result = _export_markdown(sample_tasks)
        assert "bug, urgent" in result


class TestImportJson:
    def test_import_json_parses_correctly(self, tmp_path):
        data = {
            "tasks": [
                {"title": "Imported Task", "priority": "high", "status": "todo",
                 "description": None, "project_id": None, "due_date": None,
                 "created_at": None, "updated_at": None, "completed_at": None, "tags": ["imported"]}
            ]
        }
        filepath = tmp_path / "test.json"
        filepath.write_text(json.dumps(data))

        with patch("tasq.cli.io.Store") as mock_store:
            instance = MagicMock()
            instance.migrate.return_value = None
            instance.add_task.return_value = 1
            mock_store.return_value = instance

            _import_json(instance, filepath)
            instance.add_task.assert_called_once()


class TestImportCsv:
    def test_import_csv_parses_correctly(self, tmp_path):
        content = "title,description,priority,status,tags\n" \
                  "CSV Task,Test desc,medium,todo,tag1|tag2\n"
        filepath = tmp_path / "test.csv"
        filepath.write_text(content)

        with patch("tasq.cli.io.Store") as mock_store:
            instance = MagicMock()
            instance.migrate.return_value = None
            instance.add_task.return_value = 1
            mock_store.return_value = instance

            _import_csv(instance, filepath)
            instance.add_task.assert_called_once()