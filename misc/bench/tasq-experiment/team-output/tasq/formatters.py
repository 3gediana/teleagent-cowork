from __future__ import annotations

from datetime import date, datetime
from typing import Iterable

from rich.console import Console
from rich.table import Table

from tasq.models import Task


console = Console()


def format_priority(priority: str) -> str:
    colors = {
        "low": "cyan",
        "medium": "yellow",
        "high": "magenta",
        "urgent": "red bold",
    }
    return f"[{colors.get(priority, 'white')}]{priority.upper()}[/]"


def format_status(status: str) -> str:
    colors = {
        "todo": "white",
        "in_progress": "blue",
        "blocked": "red",
        "done": "green",
        "cancelled": "dim",
    }
    return f"[{colors.get(status, 'white')}]{status.upper()}[/]"


def format_due_date(due_date: date | None, status: str) -> str:
    if due_date is None:
        return "[dim]-[/]"
    if status in ("done", "cancelled"):
        return due_date.isoformat()
    today = date.today()
    if due_date < today:
        return f"[red bold]{due_date.isoformat()}[/] (OVERDUE)"
    elif due_date == today:
        return f"[yellow bold]{due_date.isoformat()}[/] (TODAY)"
    else:
        return due_date.isoformat()


def format_datetime(dt: datetime | None) -> str:
    if dt is None:
        return ""
    return dt.strftime("%Y-%m-%d %H:%M")


def render_task_table(tasks: Iterable[Task], console: Console | None = None) -> Table:
    table = Table(show_header=True, header_style="bold magenta")
    table.add_column("ID", style="dim", width=4)
    table.add_column("Title", style="cyan")
    table.add_column("Priority", justify="center")
    table.add_column("Status", justify="center")
    table.add_column("Project", justify="center")
    table.add_column("Due", justify="center")
    table.add_column("Tags", style="dim")
    for task in tasks:
        project_str = f"#{task.project_id}" if task.project_id else "[dim]-[/]"
        tags_str = ", ".join(task.tags) if task.tags else "[dim]-[/]"
        table.add_row(
            str(task.id) if task.id else "",
            task.title,
            format_priority(task.priority),
            format_status(task.status),
            project_str,
            format_due_date(task.due_date, task.status),
            tags_str,
        )
    return table


def render_project_table(projects: Iterable, console: Console | None = None) -> Table:
    table = Table(show_header=True, header_style="bold green")
    table.add_column("ID", style="dim", width=4)
    table.add_column("Name", style="cyan")
    table.add_column("Description", style="dim")
    table.add_column("Created", style="dim")
    for proj in projects:
        table.add_row(
            str(proj.id) if proj.id else "",
            proj.name,
            proj.description or "",
            proj.created_at.strftime("%Y-%m-%d") if proj.created_at else "",
        )
    return table


def render_tag_table(tags: Iterable, console: Console | None = None) -> Table:
    table = Table(show_header=True, header_style="bold yellow")
    table.add_column("ID", style="dim", width=4)
    table.add_column("Name", style="cyan")
    for tag in tags:
        table.add_row(str(tag.id) if tag.id else "", tag.name)
    return table


def print_task_detail(task: Task, out: Console | None = None) -> None:
    if out is None:
        out = console
    out.print(f"[bold]Task #{task.id}[/]")
    out.print(f"  Title:       [cyan]{task.title}[/]")
    out.print(f"  Description: {task.description or '[dim]none[/]'}")
    out.print(f"  Priority:    {format_priority(task.priority)}")
    out.print(f"  Status:     {format_status(task.status)}")
    out.print(f"  Project:    {'#' + str(task.project_id) if task.project_id else '[dim]none[/]'}")
    out.print(f"  Due:        {format_due_date(task.due_date, task.status)}")
    out.print(f"  Tags:       {', '.join(task.tags) if task.tags else '[dim]none[/]'}")
    out.print(f"  Created:    [dim]{format_datetime(task.created_at)}[/]")
    out.print(f"  Updated:    [dim]{format_datetime(task.updated_at)}[/]")
    if task.completed_at:
        out.print(f"  Completed:  [green]{format_datetime(task.completed_at)}[/]")