import click
from datetime import datetime, date
from typing import Optional
from tasq.db import Store
from tasq.models import Task


def _resolve_store():
    from tasq.config import get_store
    return get_store()


@click.group(name="task")
def tasks():
    """Task management commands"""
    pass


@tasks.command()
@click.argument("title")
@click.option("-p", "--project", "project_name", help="Project name")
@click.option("-P", "--priority", type=click.Choice(["low", "medium", "high", "urgent"]), default="medium")
@click.option("-d", "--due", "due_date", help="Due date (YYYY-MM-DD)")
@click.option("-t", "--tag", "tags", multiple=True, help="Tag(s)")
def add(title: str, project_name: Optional[str], priority: str, due_date: Optional[str], tags: tuple):
    """Add a new task"""
    store = _resolve_store()

    due: Optional[date] = None
    if due_date:
        due = datetime.strptime(due_date, "%Y-%m-%d").date()

    project_id = None
    if project_name:
        project = store.get_project_by_name(project_name)
        if project:
            project_id = project.id

    task = Task(
        id=None,
        title=title,
        description=None,
        priority=priority,
        status="todo",
        project_id=project_id,
        due_date=due,
        created_at=datetime.now(),
        updated_at=datetime.now(),
        completed_at=None,
        tags=list(tags),
    )

    task_id = store.add_task(task)
    click.echo(f"Created task {task_id}: {title}")


@tasks.command()
@click.option("-p", "--project", "project_name", help="Filter by project name")
@click.option("-s", "--status", type=click.Choice(["todo", "in_progress", "blocked", "done", "cancelled"]), help="Filter by status")
@click.option("-t", "--tag", "tag_filter", help="Filter by tag")
@click.option("--overdue", is_flag=True, help="Show overdue tasks")
def list(project_name: Optional[str], status: Optional[str], tag_filter: Optional[str], overdue: bool):
    """List tasks"""
    store = _resolve_store()

    filters = {}
    if project_name:
        project = store.get_project_by_name(project_name)
        if project:
            filters["project_id"] = project.id
    if status:
        filters["status"] = status
    if tag_filter:
        filters["tag"] = tag_filter
    if overdue:
        filters["overdue"] = True

    task_list = store.list_tasks(**filters)

    if not task_list:
        click.echo("No tasks found.")
        return

    for task in task_list:
        status_icon = {"todo": "[ ]", "in_progress": "[~]", "blocked": "[!]", "done": "[x]", "cancelled": "[-]"}.get(task.status, "[ ]")
        due_str = f" due:{task.due_date}" if task.due_date else ""
        project_str = ""
        if task.project_id:
            proj = store.get_project(task.project_id)
            if proj:
                project_str = f" @{proj.name}"
        tags_str = f" +{','.join(task.tags)}" if task.tags else ""
        click.echo(f"{task.id}: {status_icon} {task.title}{due_str}{project_str}{tags_str}")


@tasks.command()
@click.argument("task_id", type=int)
def show(task_id: int):
    """Show task details"""
    store = _resolve_store()
    task = store.get_task(task_id)

    if not task:
        click.echo(f"Task {task_id} not found.")
        return

    click.echo(f"ID:      {task.id}")
    click.echo(f"Title:   {task.title}")
    if task.description:
        click.echo(f"Desc:    {task.description}")
    click.echo(f"Status:  {task.status}")
    click.echo(f"Priority:{task.priority}")
    if task.due_date:
        click.echo(f"Due:     {task.due_date}")
    if task.project_id:
        proj = store.get_project(task.project_id)
        if proj:
            click.echo(f"Project: {proj.name}")
    if task.tags:
        click.echo(f"Tags:    {', '.join(task.tags)}")
    click.echo(f"Created: {task.created_at}")
    click.echo(f"Updated: {task.updated_at}")
    if task.completed_at:
        click.echo(f"Done:    {task.completed_at}")


@tasks.command()
@click.argument("task_id", type=int)
def done(task_id: int):
    """Mark task as done"""
    store = _resolve_store()
    task = store.get_task(task_id)

    if not task:
        click.echo(f"Task {task_id} not found.")
        return

    store.update_task(task_id, status="done", completed_at=datetime.now())
    click.echo(f"Task {task_id} marked as done.")


@tasks.command()
@click.argument("task_id", type=int)
def rm(task_id: int):
    """Remove a task"""
    store = _resolve_store()
    task = store.get_task(task_id)

    if not task:
        click.echo(f"Task {task_id} not found.")
        return

    store.delete_task(task_id)
    click.echo(f"Task {task_id} deleted.")


@tasks.command()
@click.argument("task_id", type=int)
@click.option("--title", help="New title")
@click.option("--desc", "description", help="New description")
@click.option("--due", "due_date", help="New due date (YYYY-MM-DD)")
@click.option("--priority", type=click.Choice(["low", "medium", "high", "urgent"]), help="New priority")
@click.option("--project", "project_name", help="Move to project")
def edit(task_id: int, title: Optional[str], description: Optional[str], due_date: Optional[str], priority: Optional[str], project_name: Optional[str]):
    """Edit a task"""
    store = _resolve_store()
    task = store.get_task(task_id)

    if not task:
        click.echo(f"Task {task_id} not found.")
        return

    updates = {"updated_at": datetime.now()}

    if title:
        updates["title"] = title
    if description:
        updates["description"] = description
    if due_date:
        updates["due_date"] = datetime.strptime(due_date, "%Y-%m-%d").date()
    if priority:
        updates["priority"] = priority
    if project_name:
        project = store.get_project_by_name(project_name)
        if project:
            updates["project_id"] = project.id
        else:
            click.echo(f"Project '{project_name}' not found.")
            return

    store.update_task(task_id, **updates)
    click.echo(f"Task {task_id} updated.")


@tasks.command()
@click.argument("task_id", type=int)
@click.option("--by", "dep_id", type=int, help="Task ID this task depends on")
def block(task_id: int, dep_id: Optional[int]):
    """Mark task as blocked, optionally specify a dependency"""
    store = _resolve_store()
    task = store.get_task(task_id)

    if not task:
        click.echo(f"Task {task_id} not found.")
        return

    store.update_task(task_id, status="blocked", updated_at=datetime.now())

    if dep_id:
        dep_task = store.get_task(dep_id)
        if not dep_task:
            click.echo(f"Dependency task {dep_id} not found.")
            return
        store.add_dependency(task_id, dep_id)

    click.echo(f"Task {task_id} marked as blocked.")


@tasks.command()
@click.argument("task_id", type=int)
def deps(task_id: int):
    """Show blockers and dependants for a task"""
    store = _resolve_store()
    task = store.get_task(task_id)

    if not task:
        click.echo(f"Task {task_id} not found.")
        return

    blockers = store.get_blockers(task_id)
    dependents = store.get_dependents(task_id)

    if blockers:
        click.echo("Blocked by:")
        for b in blockers:
            click.echo(f"  {b.id}: {b.title}")
    else:
        click.echo("No blockers.")

    if dependents:
        click.echo("Blocks:")
        for d in dependents:
            click.echo(f"  {d.id}: {d.title}")
    else:
        click.echo("No dependents.")
