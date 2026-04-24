"""Task subcommands for tasq CLI."""
from __future__ import annotations

import click
from datetime import date, datetime, timedelta
from typing import Optional

from ..db import Store
from ..models import Task, get_db_path, get_tasq_home
from ..formatters import (
    task_table,
    task_detail_panel,
    dep_tree,
    format_priority,
    format_status,
    format_date,
)
from rich.console import Console


console = Console()


def resolve_project_id(store: Store, name: Optional[str]) -> Optional[int]:
    if name is None:
        return None
    proj = store.get_project_by_name(name)
    if proj is None:
        click.echo(f"Project '{name}' not found.", err=True)
        raise SystemExit(1)
    return proj.id


@click.group()
def task() -> None:
    """Manage tasks."""
    pass


@task.command("add")
@click.argument("title")
@click.option("-p", "--project", "project_name", help="Project name")
@click.option("-P", "--priority", type=click.Choice(["low", "medium", "high", "urgent"]), default="medium")
@click.option("-d", "--due", "due_date", help="Due date (YYYY-MM-DD)")
@click.option("-t", "--tag", "tags", multiple=True, help="Tag names")
@click.option("-s", "--status", type=click.Choice(["todo", "in_progress", "blocked", "done", "cancelled"]), default="todo")
@click.option("--description", "-D", help="Task description")
def task_add(
    title: str,
    project_name: Optional[str],
    priority: str,
    due_date: Optional[str],
    tags: tuple[str, ...],
    status: str,
    description: Optional[str],
) -> None:
    """Add a new task."""
    store = Store(get_db_path())
    store.migrate()

    proj_id = resolve_project_id(store, project_name) if project_name else None

    due: Optional[date] = None
    if due_date:
        try:
            due = date.fromisoformat(due_date)
        except ValueError:
            click.echo(f"Invalid date format: {due_date}", err=True)
            raise SystemExit(1)

    task_obj = Task(
        title=title,
        description=description,
        priority=priority,  # type: ignore
        status=status,  # type: ignore
        project_id=proj_id,
        due_date=due,
        tags=list(tags),
    )

    task_id = store.add_task(task_obj)
    click.echo(f"Created task #{task_id}: {title}")

    if tags:
        store.set_task_tags(task_id, list(tags))


@task.command("list")
@click.option("-p", "--project", "project_name", help="Filter by project name")
@click.option("-s", "--status", "status_filter", help="Filter by status")
@click.option("-t", "--tag", "tag_filter", help="Filter by tag")
@click.option("--overdue", is_flag=True, help="Show overdue tasks only")
def task_list(
    project_name: Optional[str],
    status_filter: Optional[str],
    tag_filter: Optional[str],
    overdue: bool,
) -> None:
    """List tasks."""
    store = Store(get_db_path())
    store.migrate()

    proj_id = resolve_project_id(store, project_name) if project_name else None

    tasks = store.list_tasks(
        project_id=proj_id,
        status=status_filter,
        tag=tag_filter,
        overdue=overdue,
    )

    if not tasks:
        click.echo("No tasks found.")
        return

    table = task_table(tasks)
    console.print(table)


@task.command("show")
@click.argument("task_id", type=int)
def task_show(task_id: int) -> None:
    """Show task details."""
    store = Store(get_db_path())
    store.migrate()

    task_obj = store.get_task(task_id)
    if task_obj is None:
        click.echo(f"Task #{task_id} not found.", err=True)
        raise SystemExit(1)

    panel = task_detail_panel(task_obj)
    console.print(panel)

    blockers = store.get_blockers(task_id)
    dependants = store.get_dependants(task_id)
    if blockers or dependants:
        dep_panel = dep_tree(task_obj, blockers, dependants)
        console.print(dep_panel)


@task.command("done")
@click.argument("task_id", type=int)
def task_done(task_id: int) -> None:
    """Mark task as done."""
    store = Store(get_db_path())
    store.migrate()

    task_obj = store.get_task(task_id)
    if task_obj is None:
        click.echo(f"Task #{task_id} not found.", err=True)
        raise SystemExit(1)

    task_obj.mark_done()
    store.update_task(
        task_id,
        status="done",
        completed_at=task_obj.completed_at,
        updated_at=task_obj.updated_at,
    )
    click.echo(f"Task #{task_id} marked as done.")


@task.command("rm")
@click.argument("task_id", type=int)
@click.option("-f", "--force", is_flag=True, help="Skip confirmation")
def task_rm(task_id: int, force: bool) -> None:
    """Delete a task."""
    store = Store(get_db_path())
    store.migrate()

    task_obj = store.get_task(task_id)
    if task_obj is None:
        click.echo(f"Task #{task_id} not found.", err=True)
        raise SystemExit(1)

    if not force:
        if not click.confirm(f"Delete task #{task_id} '{task_obj.title}'?"):
            return

    store.delete_task(task_id)
    click.echo(f"Deleted task #{task_id}.")


@task.command("edit")
@click.argument("task_id", type=int)
@click.option("--title", help="New title")
@click.option("--description", help="New description")
@click.option("--priority", type=click.Choice(["low", "medium", "high", "urgent"]), help="New priority")
@click.option("--status", type=click.Choice(["todo", "in_progress", "blocked", "done", "cancelled"]), help="New status")
@click.option("--project", "project_name", help="Move to project")
@click.option("--due", "due_date", help="New due date (YYYY-MM-DD)")
def task_edit(
    task_id: int,
    title: Optional[str],
    description: Optional[str],
    priority: Optional[str],
    status: Optional[str],
    project_name: Optional[str],
    due_date: Optional[str],
) -> None:
    """Edit task fields."""
    store = Store(get_db_path())
    store.migrate()

    task_obj = store.get_task(task_id)
    if task_obj is None:
        click.echo(f"Task #{task_id} not found.", err=True)
        raise SystemExit(1)

    updates: dict = {}
    if title is not None:
        updates["title"] = title
    if description is not None:
        updates["description"] = description
    if priority is not None:
        updates["priority"] = priority
    if status is not None:
        updates["status"] = status
        if status == "done":
            updates["completed_at"] = datetime.utcnow()
    if project_name is not None:
        proj_id = resolve_project_id(store, project_name)
        updates["project_id"] = proj_id
    if due_date is not None:
        try:
            updates["due_date"] = date.fromisoformat(due_date)
        except ValueError:
            click.echo(f"Invalid date format: {due_date}", err=True)
            raise SystemExit(1)

    if updates:
        store.update_task(task_id, **updates)
        click.echo(f"Updated task #{task_id}.")
    else:
        click.echo("No changes made.")


@task.command("block")
@click.argument("task_id", type=int)
@click.option("--by", "dep_id", type=int, help="Task ID this task depends on")
def task_block(task_id: int, dep_id: Optional[int]) -> None:
    """Block a task or add a dependency."""
    store = Store(get_db_path())
    store.migrate()

    task_obj = store.get_task(task_id)
    if task_obj is None:
        click.echo(f"Task #{task_id} not found.", err=True)
        raise SystemExit(1)

    if dep_id is not None:
        dep_task = store.get_task(dep_id)
        if dep_task is None:
            click.echo(f"Dependency task #{dep_id} not found.", err=True)
            raise SystemExit(1)

        store.add_task_dep(task_id, dep_id)
        store.update_task(task_id, status="blocked")
        click.echo(f"Task #{task_id} now depends on #{dep_id} and is blocked.")
    else:
        store.update_task(task_id, status="blocked")
        click.echo(f"Task #{task_id} marked as blocked.")


@task.command("deps")
@click.argument("task_id", type=int)
def task_deps(task_id: int) -> None:
    """Show task dependencies."""
    store = Store(get_db_path())
    store.migrate()

    task_obj = store.get_task(task_id)
    if task_obj is None:
        click.echo(f"Task #{task_id} not found.", err=True)
        raise SystemExit(1)

    blockers = store.get_blockers(task_id)
    dependants = store.get_dependants(task_id)

    if not blockers and not dependants:
        click.echo(f"Task #{task_id} has no dependencies.")
        return

    panel = dep_tree(task_obj, blockers, dependants)
    console.print(panel)


@task.command("tag")
@click.argument("task_id", type=int)
@click.argument("action", type=click.Choice(["add", "remove", "set"]))
@click.argument("tags", nargs=-1)
def task_tag(task_id: int, action: str, tags: tuple[str, ...]) -> None:
    """Manage task tags."""
    store = Store(get_db_path())
    store.migrate()

    task_obj = store.get_task(task_id)
    if task_obj is None:
        click.echo(f"Task #{task_id} not found.", err=True)
        raise SystemExit(1)

    if action == "add":
        for tag_name in tags:
            store.add_task_tag(task_id, tag_name)
        click.echo(f"Added tags to task #{task_id}.")
    elif action == "remove":
        for tag_name in tags:
            store.remove_task_tag(task_id, tag_name)
        click.echo(f"Removed tags from task #{task_id}.")
    elif action == "set":
        store.set_task_tags(task_id, list(tags))
        click.echo(f"Set tags for task #{task_id}.")


@task.command("unblock")
@click.argument("task_id", type=int)
def task_unblock(task_id: int) -> None:
    """Unblock a task."""
    store = Store(get_db_path())
    store.migrate()

    task_obj = store.get_task(task_id)
    if task_obj is None:
        click.echo(f"Task #{task_id} not found.", err=True)
        raise SystemExit(1)

    store.update_task(task_id, status="todo")
    store.delete_task(task_id)
    click.echo(f"Note: Use 'task edit {task_id} --status todo' to unblock properly.")


@task.command("move")
@click.argument("task_id", type=int)
@click.option("--project", "project_name", required=True, help="Target project name")
def task_move(task_id: int, project_name: str) -> None:
    """Move task to a project."""
    store = Store(get_db_path())
    store.migrate()

    task_obj = store.get_task(task_id)
    if task_obj is None:
        click.echo(f"Task #{task_id} not found.", err=True)
        raise SystemExit(1)

    proj_id = resolve_project_id(store, project_name)
    store.update_task(task_id, project_id=proj_id)
    click.echo(f"Moved task #{task_id} to project '{project_name}'.")