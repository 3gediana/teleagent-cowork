"""Rich table formatters for tasq CLI output."""
from __future__ import annotations

from datetime import date, datetime
from typing import Optional
from rich.table import Table
from rich.console import Console
from rich.panel import Panel

from .models import Task, Project, Tag


PRIORITY_COLORS = {
    "urgent": "red",
    "high": "yellow",
    "medium": "blue",
    "low": "green",
}

STATUS_COLORS = {
    "todo": "white",
    "in_progress": "cyan",
    "blocked": "red",
    "done": "green",
    "cancelled": "dim",
}


def format_date(d: Optional[date], fmt: str = "%Y-%m-%d") -> str:
    if d is None:
        return "-"
    return d.strftime(fmt)


def format_datetime(dt: Optional[datetime], fmt: str = "%Y-%m-%d %H:%M") -> str:
    if dt is None:
        return "-"
    return dt.strftime(fmt)


def format_priority(p: str) -> str:
    color = PRIORITY_COLORS.get(p, "white")
    return f"[{color}]{p}[/{color}]"


def format_status(s: str) -> str:
    color = STATUS_COLORS.get(s, "white")
    label = s.replace("_", " ")
    return f"[{color}]{label}[/{color}]"


def format_tags(tags: list[str]) -> str:
    if not tags:
        return "-"
    return ", ".join(f"[cyan]{t}[/cyan]" for t in tags)


def task_table(tasks: list[Task], console: Optional[Console] = None) -> Table:
    table = Table(show_header=True, header_style="bold magenta")
    table.add_column("ID", style="dim", width=4)
    table.add_column("Title", style="bold")
    table.add_column("Priority", justify="center")
    table.add_column("Status", justify="center")
    table.add_column("Due", justify="center")
    table.add_column("Tags")

    for task in tasks:
        due_str = format_date(task.due_date)
        if task.is_overdue():
            due_str = f"[red]{due_str}[/red]"

        row = [
            str(task.id),
            task.title,
            format_priority(task.priority),
            format_status(task.status),
            due_str,
            format_tags(task.tags),
        ]
        table.add_row(*row)

    return table


def project_table(projects: list[Project]) -> Table:
    table = Table(show_header=True, header_style="bold magenta")
    table.add_column("ID", style="dim", width=4)
    table.add_column("Name", style="bold")
    table.add_column("Description")
    table.add_column("Created")

    for proj in projects:
        table.add_row(
            str(proj.id),
            proj.name,
            proj.description or "-",
            format_datetime(proj.created_at),
        )
    return table


def tag_table(tags: list[Tag]) -> Table:
    table = Table(show_header=True, header_style="bold magenta")
    table.add_column("ID", style="dim", width=4)
    table.add_column("Name", style="bold")

    for tag in tags:
        table.add_row(str(tag.id), f"[cyan]{tag.name}[/cyan]")
    return table


def task_detail_panel(task: Task) -> Panel:
    lines = []
    lines.append(f"[bold]ID:[/bold] {task.id}")
    lines.append(f"[bold]Title:[/bold] {task.title}")
    if task.description:
        lines.append(f"[bold]Description:[/bold] {task.description}")
    lines.append(f"[bold]Priority:[/bold] {format_priority(task.priority)}")
    lines.append(f"[bold]Status:[/bold] {format_status(task.status)}")
    if task.project_id:
        lines.append(f"[bold]Project ID:[/bold] {task.project_id}")
    lines.append(f"[bold]Due:[/bold] {format_date(task.due_date)}")
    lines.append(f"[bold]Created:[/bold] {format_datetime(task.created_at)}")
    lines.append(f"[bold]Updated:[/bold] {format_datetime(task.updated_at)}")
    if task.completed_at:
        lines.append(f"[bold]Completed:[/bold] {format_datetime(task.completed_at)}")
    lines.append(f"[bold]Tags:[/bold] {format_tags(task.tags)}")

    return Panel("\n".join(lines), title=f"Task #{task.id}", border_style="cyan")


def stats_table(stats: dict) -> Table:
    table = Table(show_header=False, border_style="cyan")
    table.add_column("Metric", style="bold")
    table.add_column("Value", justify="right")

    for key, value in stats.items():
        table.add_row(str(key), str(value))

    return table


def dep_tree(task: Task, blockers: list[Task], dependants: list[Task]) -> Panel:
    lines = []

    if blockers:
        lines.append("[bold]Blocked by:[/bold]")
        for t in blockers:
            status_str = format_status(t.status)
            lines.append(f"  - [{t.id}] {t.title} [{status_str}]")
    else:
        lines.append("[bold]Blocked by:[/bold] (none)")

    if dependants:
        lines.append("[bold]Blocking:[/bold]")
        for t in dependants:
            status_str = format_status(t.status)
            lines.append(f"  - [{t.id}] {t.title} [{status_str}]")
    else:
        lines.append("[bold]Blocking:[/bold] (none)")

    return Panel("\n".join(lines), title=f"Dependencies for Task #{task.id}", border_style="yellow")


def burndown_chart(data: list[tuple[str, int, int]]) -> Table:
    table = Table(show_header=True, header_style="bold cyan")
    table.add_column("Date")
    table.add_column("Completed", justify="right")
    table.add_column("Remaining", justify="right")

    for date_str, completed, remaining in data:
        table.add_row(date_str, str(completed), str(remaining))

    return table