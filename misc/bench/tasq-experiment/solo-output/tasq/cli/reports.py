"""Report subcommands for tasq CLI."""
from __future__ import annotations

import click
from datetime import date, timedelta
from typing import Optional

from ..db import Store
from ..models import get_db_path
from ..formatters import (
    task_table,
    stats_table,
    burndown_chart,
    format_status,
)
from rich.console import Console
from rich.panel import Panel


console = Console()


@click.group()
def report() -> None:
    """Generate reports."""
    pass


@report.command("stats")
def report_stats() -> None:
    """Show task statistics."""
    store = Store(get_db_path())
    store.migrate()

    all_tasks = store.list_tasks()

    total = len(all_tasks)
    by_status: dict[str, int] = {}
    by_priority: dict[str, int] = {}

    for t in all_tasks:
        by_status[t.status] = by_status.get(t.status, 0) + 1
        by_priority[t.priority] = by_priority.get(t.priority, 0) + 1

    overdue_count = len(store.list_tasks(overdue=True))

    stats = {
        "Total tasks": total,
        "Todo": by_status.get("todo", 0),
        "In progress": by_status.get("in_progress", 0),
        "Blocked": by_status.get("blocked", 0),
        "Done": by_status.get("done", 0),
        "Cancelled": by_status.get("cancelled", 0),
        "Overdue": overdue_count,
        "High/Urgent": by_priority.get("high", 0) + by_priority.get("urgent", 0),
        "Medium": by_priority.get("medium", 0),
        "Low": by_priority.get("low", 0),
    }

    table = stats_table(stats)
    console.print(Panel(table, title="Task Statistics", border_style="cyan"))


@report.command("by-project")
def report_by_project() -> None:
    """Show task breakdown by project."""
    store = Store(get_db_path())
    store.migrate()

    projects = store.list_projects()
    from rich.table import Table

    table = Table(show_header=True, header_style="bold magenta")
    table.add_column("Project")
    table.add_column("Total", justify="right")
    table.add_column("Todo", justify="right")
    table.add_column("In Progress", justify="right")
    table.add_column("Blocked", justify="right")
    table.add_column("Done", justify="right")

    for proj in projects:
        tasks = store.list_tasks(project_id=proj.id)
        total = len(tasks)
        todo = len([t for t in tasks if t.status == "todo"])
        in_prog = len([t for t in tasks if t.status == "in_progress"])
        blocked = len([t for t in tasks if t.status == "blocked"])
        done = len([t for t in tasks if t.status == "done"])

        table.add_row(
            proj.name,
            str(total),
            str(todo),
            str(in_prog),
            str(blocked),
            str(done),
        )

    no_proj_tasks = store.list_tasks(project_id=None)
    no_proj_done = len([t for t in no_proj_tasks if t.status == "done"])
    no_proj_total = len(no_proj_tasks)
    table.add_row(
        "(no project)",
        str(no_proj_total),
        str(no_proj_total - no_proj_done - len([t for t in no_proj_tasks if t.status in ('in_progress','blocked')])),
        str(len([t for t in no_proj_tasks if t.status == 'in_progress'])),
        str(len([t for t in no_proj_tasks if t.status == 'blocked'])),
        str(no_proj_done),
    )

    console.print(Panel(table, title="Tasks by Project", border_style="cyan"))


@report.command("overdue")
def report_overdue() -> None:
    """Show overdue tasks."""
    store = Store(get_db_path())
    store.migrate()

    tasks = store.list_tasks(overdue=True)
    if not tasks:
        click.echo("No overdue tasks.")
        return

    table = task_table(tasks)
    console.print(Panel(table, title=f"Overdue Tasks ({len(tasks)})", border_style="red"))


@report.command("burndown")
@click.option("-p", "--project", "project_name", help="Filter by project name")
@click.option("--days", type=int, default=30, help="Number of days to show")
def report_burndown(project_name: Optional[str], days: int) -> None:
    """Show burndown chart."""
    store = Store(get_db_path())
    store.migrate()

    from ..models import get_tasq_home

    proj_id: Optional[int] = None
    if project_name:
        proj = store.get_project_by_name(project_name)
        if proj is None:
            click.echo(f"Project '{project_name}' not found.", err=True)
            raise SystemExit(1)
        proj_id = proj.id

    tasks = store.list_tasks(project_id=proj_id)

    total_initial = len([t for t in tasks if t.status != "done" and t.status != "cancelled"])

    end_date = date.today()
    start_date = end_date - timedelta(days=days)

    data: list[tuple[str, int, int]] = []

    for i in range(days + 1):
        day = start_date + timedelta(days=i)
        day_str = day.isoformat()

        completed_by_day = len([
            t for t in tasks
            if t.completed_at and t.completed_at.date() <= day
        ])

        remaining = total_initial - completed_by_day
        data.append((day_str, completed_by_day, max(remaining, 0)))

    chart = burndown_chart(data)
    console.print(Panel(chart, title=f"Burndown (last {days} days)", border_style="green"))


@report.command("gantt")
@click.option("-p", "--project", "project_name", help="Filter by project name")
def report_gantt(project_name: Optional[str]) -> None:
    """Show simple gantt-style view."""
    store = Store(get_db_path())
    store.migrate()

    proj_id: Optional[int] = None
    if project_name:
        proj = store.get_project_by_name(project_name)
        if proj is None:
            click.echo(f"Project '{project_name}' not found.", err=True)
            raise SystemExit(1)
        proj_id = proj.id

    tasks = store.list_tasks(project_id=proj_id)

    from rich.table import Table
    table = Table(show_header=True, header_style="bold magenta")
    table.add_column("ID")
    table.add_column("Title")
    table.add_column("Status")
    table.add_column("Due")

    for t in tasks:
        due_str = t.due_date.isoformat() if t.due_date else "-"
        if t.is_overdue():
            due_str = f"[red]{due_str}[/red]"
        table.add_row(str(t.id), t.title, format_status(t.status), due_str)

    console.print(table)