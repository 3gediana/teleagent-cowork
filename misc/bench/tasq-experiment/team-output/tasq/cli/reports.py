import click
from datetime import date, timedelta
from rich.console import Console
from rich.table import Table

from tasq.db import Store
from tasq.models import Task


console = Console()


@click.group(name="report")
def report_group():
    """Generate task reports."""
    pass


@report_group.command(name="burndown")
@click.option("-p", "--project", "project_name", help="Filter by project name")
@click.option("--days", default=30, help="Number of days to look back")
def burndown(project_name: str | None, days: int):
    """Burndown report showing completed tasks over time."""
    store = Store()
    store.migrate()

    cutoff = date.today() - timedelta(days=days)
    tasks = store.list_tasks()

    if project_name:
        projects = store.list_projects()
        proj_map = {p.name: p.id for p in projects}
        proj_id = proj_map.get(project_name)
        if proj_id is None:
            console.print(f"[red]Project '{project_name}' not found.[/red]")
            return
        tasks = [t for t in tasks if t.project_id == proj_id]

    completed = [t for t in tasks if t.status == "done" and t.completed_at]
    remaining = [t for t in tasks if t.status != "done"]

    table = Table(title=f"Burndown Report (last {days} days)")
    table.add_column("Metric", style="cyan")
    table.add_column("Value", style="magenta")

    table.add_row("Total tasks", str(len(tasks)))
    table.add_row("Completed", str(len(completed)))
    table.add_row("Remaining", str(len(remaining)))
    table.add_row("Completion rate", f"{len(completed)/len(tasks)*100:.1f}%" if tasks else "N/A")

    console.print(table)


@report_group.command(name="by-project")
def by_project():
    """Task counts grouped by project."""
    store = Store()
    store.migrate()

    tasks = store.list_tasks()
    projects = store.list_projects()
    proj_map = {p.id: p.name for p in projects}
    proj_map[None] = "No project"

    counts: dict[str, dict[str, int]] = {}
    for t in tasks:
        pname = proj_map.get(t.project_id, "No project")
        if pname not in counts:
            counts[pname] = {"total": 0, "done": 0, "todo": 0, "in_progress": 0, "blocked": 0}
        counts[pname]["total"] += 1
        if t.status == "done":
            counts[pname]["done"] += 1
        elif t.status == "todo":
            counts[pname]["todo"] += 1
        elif t.status == "in_progress":
            counts[pname]["in_progress"] += 1
        elif t.status == "blocked":
            counts[pname]["blocked"] += 1

    table = Table(title="Tasks by Project")
    table.add_column("Project", style="cyan")
    table.add_column("Total", justify="right")
    table.add_column("Done", justify="right", style="green")
    table.add_column("Todo", justify="right")
    table.add_column("In Progress", justify="right", style="yellow")
    table.add_column("Blocked", justify="right", style="red")

    for pname, c in sorted(counts.items()):
        table.add_row(
            pname,
            str(c["total"]),
            str(c["done"]),
            str(c["todo"]),
            str(c["in_progress"]),
            str(c["blocked"])
        )

    console.print(table)


@report_group.command(name="overdue")
def overdue():
    """List tasks that are past their due date and not done."""
    store = Store()
    store.migrate()

    tasks = store.list_tasks()
    today = date.today()
    overdue_tasks = [
        t for t in tasks
        if t.due_date and t.due_date < today and t.status not in ("done", "cancelled")
    ]

    if not overdue_tasks:
        console.print("[green]No overdue tasks.[/green]")
        return

    table = Table(title="Overdue Tasks")
    table.add_column("ID", justify="right")
    table.add_column("Title", style="cyan")
    table.add_column("Due Date", style="red")
    table.add_column("Priority", style="yellow")

    for t in overdue_tasks:
        table.add_row(str(t.id), t.title, str(t.due_date), t.priority)

    console.print(table)


@report_group.command(name="stats")
def stats():
    """Show overall task statistics."""
    store = Store()
    store.migrate()

    tasks = store.list_tasks()
    total = len(tasks)
    done = sum(1 for t in tasks if t.status == "done")
    todo = sum(1 for t in tasks if t.status == "todo")
    in_progress = sum(1 for t in tasks if t.status == "in_progress")
    blocked = sum(1 for t in tasks if t.status == "blocked")
    cancelled = sum(1 for t in tasks if t.status == "cancelled")

    today = date.today()
    overdue_count = sum(
        1 for t in tasks if t.due_date and t.due_date < today and t.status not in ("done", "cancelled")
    )

    table = Table(title="Task Statistics")
    table.add_column("Metric", style="cyan")
    table.add_column("Value", style="magenta")

    table.add_row("Total tasks", str(total))
    table.add_row("Done", f"{done} ({done/total*100:.1f}%)" if total else "0")
    table.add_row("Todo", str(todo))
    table.add_row("In Progress", str(in_progress))
    table.add_row("Blocked", str(blocked))
    table.add_row("Cancelled", str(cancelled))
    table.add_row("Overdue", str(overdue_count))

    console.print(table)