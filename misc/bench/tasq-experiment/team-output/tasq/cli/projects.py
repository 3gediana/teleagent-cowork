import click
from datetime import datetime
from typing import Optional
from tasq.db import Store
from tasq.models import Project


def _resolve_store():
    from tasq.config import get_store
    return get_store()


@click.group(name="project")
def projects():
    """Project management commands"""
    pass


@projects.command()
@click.argument("name")
@click.option("-d", "--desc", "description", help="Project description")
def add(name: str, description: Optional[str]):
    """Add a new project"""
    store = _resolve_store()

    project = Project(
        id=None,
        name=name,
        description=description,
        created_at=datetime.now(),
        archived=False,
    )

    try:
        project_id = store.add_project(project)
        click.echo(f"Created project {project_id}: {name}")
    except Exception as e:
        click.echo(f"Error: {e}", err=True)


@projects.command()
def list():
    """List all projects"""
    store = _resolve_store()
    project_list = store.list_projects()

    if not project_list:
        click.echo("No projects found.")
        return

    for proj in project_list:
        arch = " (archived)" if proj.archived else ""
        desc = f" — {proj.description}" if proj.description else ""
        click.echo(f"{proj.id}: {proj.name}{desc}{arch}")


@projects.command()
@click.argument("old_name")
@click.argument("new_name")
def rename(old_name: str, new_name: str):
    """Rename a project"""
    store = _resolve_store()
    project = store.get_project_by_name(old_name)

    if not project:
        click.echo(f"Project '{old_name}' not found.")
        return

    try:
        store.update_project(project.id, name=new_name)
        click.echo(f"Renamed project '{old_name}' to '{new_name}'.")
    except Exception as e:
        click.echo(f"Error: {e}", err=True)


@projects.command()
@click.argument("name")
def archive(name: str):
    """Archive a project"""
    store = _resolve_store()
    project = store.get_project_by_name(name)

    if not project:
        click.echo(f"Project '{name}' not found.")
        return

    store.update_project(project.id, archived=True)
    click.echo(f"Project '{name}' archived.")
