from __future__ import annotations

import sys
from datetime import date, datetime
from pathlib import Path

import pytest

sys.path.insert(0, str(Path(__file__).parent.parent))

from tasq.formatters import (
    format_due_date,
    format_priority,
    format_status,
    format_datetime,
    print_task_detail,
    render_project_table,
    render_tag_table,
    render_task_table,
)
from tasq.models import Project, Tag, Task
from rich.console import Console
from io import StringIO


class TestFormatPriority:
    def test_low(self) -> None:
        result = format_priority("low")
        assert "low" in result.lower() or "LOW" in result

    def test_medium(self) -> None:
        result = format_priority("medium")
        assert "MEDIUM" in result

    def test_high(self) -> None:
        result = format_priority("high")
        assert "HIGH" in result

    def test_urgent(self) -> None:
        result = format_priority("urgent")
        assert "URGENT" in result

    def test_unknown(self) -> None:
        result = format_priority("unknown")
        assert "unknown" in result.lower()


class TestFormatStatus:
    def test_todo(self) -> None:
        result = format_status("todo")
        assert "TODO" in result

    def test_in_progress(self) -> None:
        result = format_status("in_progress")
        assert "IN_PROGRESS" in result

    def test_blocked(self) -> None:
        result = format_status("blocked")
        assert "BLOCKED" in result

    def test_done(self) -> None:
        result = format_status("done")
        assert "DONE" in result

    def test_cancelled(self) -> None:
        result = format_status("cancelled")
        assert "CANCELLED" in result


class TestFormatDueDate:
    def test_none(self) -> None:
        result = format_due_date(None, "todo")
        assert "-" in result or "none" in result.lower()

    def test_future_date(self) -> None:
        future = date(2099, 12, 31)
        result = format_due_date(future, "todo")
        assert "2099" in result

    def test_overdue(self) -> None:
        past = date(2020, 1, 1)
        result = format_due_date(past, "todo")
        assert "OVERDUE" in result or "red" in result.lower()

    def test_today(self) -> None:
        today = date.today()
        result = format_due_date(today, "todo")
        assert "TODAY" in result or str(today) in result

    def test_done_status_no_overdue_mark(self) -> None:
        past = date(2020, 1, 1)
        result = format_due_date(past, "done")
        assert "OVERDUE" not in result


class TestFormatDatetime:
    def test_none(self) -> None:
        result = format_datetime(None)
        assert result == ""

    def test_valid_datetime(self) -> None:
        dt = datetime(2024, 6, 15, 10, 30)
        result = format_datetime(dt)
        assert "2024" in result
        assert "10" in result


class TestRenderTaskTable:
    def test_empty_list(self) -> None:
        table = render_task_table([])
        assert table.row_count == 0

    def test_single_task(self) -> None:
        task = Task(
            id=1,
            title="Test task",
            priority="high",
            status="todo",
            due_date=date(2024, 7, 1),
            tags=["bug"],
        )
        table = render_task_table([task])
        assert table.row_count == 1

    def test_multiple_tasks(self) -> None:
        tasks = [
            Task(id=1, title="Task 1", priority="low", status="todo"),
            Task(id=2, title="Task 2", priority="high", status="done"),
        ]
        table = render_task_table(tasks)
        assert table.row_count == 2

    def test_task_without_project(self) -> None:
        task = Task(id=1, title="No project", priority="medium", status="todo")
        table = render_task_table([task])
        assert table.row_count == 1

    def test_task_without_tags(self) -> None:
        task = Task(id=1, title="No tags", priority="medium", status="todo", tags=[])
        table = render_task_table([task])
        assert table.row_count == 1


class TestRenderProjectTable:
    def test_empty(self) -> None:
        table = render_project_table([])
        assert table.row_count == 0

    def test_single_project(self) -> None:
        proj = Project(id=1, name="web", description="Web project")
        table = render_project_table([proj])
        assert table.row_count == 1


class TestRenderTagTable:
    def test_empty(self) -> None:
        table = render_tag_table([])
        assert table.row_count == 0

    def test_single_tag(self) -> None:
        tag = Tag(id=1, name="urgent")
        table = render_tag_table([tag])
        assert table.row_count == 1


class TestPrintTaskDetail:
    def test_prints_task_info(self) -> None:
        task = Task(
            id=1,
            title="Detailed task",
            description="A description",
            priority="high",
            status="in_progress",
            project_id=2,
            due_date=date(2024, 8, 1),
            tags=["bug", "urgent"],
            created_at=datetime(2024, 1, 1, 12, 0),
            updated_at=datetime(2024, 1, 2, 12, 0),
        )
        output = StringIO()
        test_console = Console(file=output, force_terminal=True)
        print_task_detail(task, out=test_console)
        output_str = output.getvalue()
        assert "Detailed task" in output_str