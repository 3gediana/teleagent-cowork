"""Tests for tasq.formatters module."""
from __future__ import annotations

import pytest
from datetime import date, datetime

from tasq.formatters import (
    format_date,
    format_datetime,
    format_priority,
    format_status,
    format_tags,
    task_table,
    project_table,
    tag_table,
    task_detail_panel,
    stats_table,
    dep_tree,
    burndown_chart,
    PRIORITY_COLORS,
    STATUS_COLORS,
)
from tasq.models import Task, Project, Tag


class TestFormatHelpers:
    def test_format_date_none(self) -> None:
        assert format_date(None) == "-"

    def test_format_date_with_value(self) -> None:
        d = date(2025, 6, 15)
        assert format_date(d) == "2025-06-15"

    def test_format_date_custom_format(self) -> None:
        d = date(2025, 6, 15)
        assert format_date(d, "%d/%m/%Y") == "15/06/2025"

    def test_format_datetime_none(self) -> None:
        assert format_datetime(None) == "-"

    def test_format_datetime_with_value(self) -> None:
        dt = datetime(2025, 6, 15, 10, 30)
        assert format_datetime(dt) == "2025-06-15 10:30"

    def test_format_priority_all(self) -> None:
        for p in ["low", "medium", "high", "urgent"]:
            result = format_priority(p)
            assert p in result.lower() or "[" in result

    def test_format_status_all(self) -> None:
        for s in ["todo", "in_progress", "blocked", "done", "cancelled"]:
            result = format_status(s)
            assert result

    def test_format_tags_empty(self) -> None:
        assert format_tags([]) == "-"

    def test_format_tags_with_values(self) -> None:
        tags = ["bug", "urgent"]
        result = format_tags(tags)
        assert "bug" in result
        assert "urgent" in result


class TestTaskTable:
    def test_task_table_empty(self) -> None:
        table = task_table([])
        assert table is not None
        assert table.columns

    def test_task_table_with_tasks(self) -> None:
        task = Task(
            id=1,
            title="Test Task",
            priority="high",
            status="todo",
            tags=["bug"],
        )
        table = task_table([task])
        assert table is not None


class TestProjectTable:
    def test_project_table_empty(self) -> None:
        table = project_table([])
        assert table is not None

    def test_project_table_with_projects(self) -> None:
        proj = Project(id=1, name="Test Project", description="Desc")
        table = project_table([proj])
        assert table is not None


class TestTagTable:
    def test_tag_table_empty(self) -> None:
        table = tag_table([])
        assert table is not None

    def test_tag_table_with_tags(self) -> None:
        tag = Tag(id=1, name="bug")
        table = tag_table([tag])
        assert table is not None


class TestTaskDetailPanel:
    def test_task_detail_panel(self) -> None:
        task = Task(
            id=1,
            title="Detail Test",
            description="A description",
            priority="high",
            status="todo",
        )
        panel = task_detail_panel(task)
        assert panel is not None
        assert "Detail Test" in str(panel)


class TestStatsTable:
    def test_stats_table_empty(self) -> None:
        table = stats_table({})
        assert table is not None

    def test_stats_table_with_data(self) -> None:
        stats = {
            "Total": 10,
            "Done": 5,
            "Todo": 3,
        }
        table = stats_table(stats)
        assert table is not None


class TestDepTree:
    def test_dep_tree_no_deps(self) -> None:
        task = Task(id=1, title="No Deps")
        panel = dep_tree(task, [], [])
        assert panel is not None

    def test_dep_tree_with_blockers(self) -> None:
        task = Task(id=1, title="Task")
        blocker = Task(id=2, title="Blocker", status="todo")
        panel = dep_tree(task, [blocker], [])
        assert panel is not None

    def test_dep_tree_with_dependants(self) -> None:
        task = Task(id=1, title="Task")
        dependant = Task(id=3, title="Dependant", status="todo")
        panel = dep_tree(task, [], [dependant])
        assert panel is not None


class TestBurndownChart:
    def test_burndown_chart_empty(self) -> None:
        data: list[tuple[str, int, int]] = []
        table = burndown_chart(data)
        assert table is not None

    def test_burndown_chart_with_data(self) -> None:
        data = [
            ("2025-01-01", 0, 10),
            ("2025-01-02", 2, 8),
            ("2025-01-03", 5, 5),
        ]
        table = burndown_chart(data)
        assert table is not None