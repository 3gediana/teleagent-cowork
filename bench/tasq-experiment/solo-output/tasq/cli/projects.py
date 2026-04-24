"""Project subcommands for tasq CLI."""
from __future__ import annotations

import click
from typing import Optional

from ..db import Store
from ..models import Project, get_db_path
from ..formatters import project_table
from rich.console import Console


console = Console()


@click.group()
def project() -> None:
    """Manage projects."""
    pass


@project.command("add")
@click.argument("name")
@click.option("-d", "--description", help="Project description")
def project_add(name: str, description: Optional[str]) -> None:
    """Add a new project."""
    store = Store(get_db_path())
    store.migrate()

    proj = Project(name=name, description=description)
    try:
        proj_id = store.add_project(proj)
        click.echo(f"Created project #{proj_id}: {name}")
    except Exception as e:
        click.echo(f"Failed to create project: {e}", err=True)
        raise SystemExit(1)


@project.command("list")
@click.option("--archived", is_flag=True, help="Include archived projects")
def project_list(archived: bool) -> None:
    """List all projects."""
    store = Store(get_db_path())
    store.migrate()

    projects = store.list_projects(include_archived=archived)
    if not projects:
        click.echo("No projects found.")
        return

    table = project_table(projects)
    console.print(table)


@project.command("rename")
@click.argument("old_name")
@click.argument("new_name")
def project_rename(old_name: str, new_name: str) -> None:
    """Rename a project."""
    store = Store(get_db_path())
    store.migrate()

    proj = store.get_project_by_name(old_name)
    if proj is None:
        click.echo(f"Project '{old_name}' not found.", err=True)
        raise SystemExit(1)

    store.update_project(proj.id, name=new_name)
    click.echo(f"Renamed project '{old_name}' to '{new_name}'.")


@project.command("archive")
@click.argument("name")
def project_archive(name: str) -> None:
    """Archive a project."""
    store = Store(get_db_path())
    store.migrate()

    proj = store.get_project_by_name(name)
    if proj is None:
        click.echo(f"Project '{name}' not found.", err=True)
        raise SystemExit(1)

    store.update_project(proj.id, archived=True)
    click.echo(f"Archived project '{name}'.")


@project.command("unarchive")
@click.argument("name")
def project_unarchive(name: str) -> None:
    """Unarchive a project."""
    store = Store(get_db_path())
    store.migrate()

    proj = store.get_project_by_name(name)
    if proj is None:
        click.echo(f"Project '{name}' not found.", err=True)
        raise SystemExit(1)

    store.update_project(proj.id, archived=False)
    click.echo(f"Unarchived project '{name}'.")


@project.command("show")
@click.argument("name")
def project_show(name: str) -> None:
    """Show project details."""
    store = Store(get_db_path())
    store.migrate()

    proj = store.get_project_by_name(name)
    if proj is None:
        click.echo(f"Project '{name}' not found.", err=True)
        raise SystemExit(1)

    from rich.panel import Panel
    from ..formatters import format_datetime

    lines = [
        f"[bold]ID:[/bold] {proj.id}",
        f"[bold]Name:[/bold] {proj.name}",
        f"[bold]Description:[/bold] {proj.description or '-'}",
        f"[bold]Created:[/bold] {format_datetime(proj.created_at)}",
        f"[bold]Archived:[/bold] {proj.archived}",
    ]
    panel = Panel("\n".join(lines), title=f"Project #{proj.id}", border_style="cyan")
    console.print(panel)


@project.command("rm")
@click.argument("name")
@click.option("-f", "--force", is_flag=True, help="Skip confirmation")
def project_rm(name: str, force: bool) -> None:
    """Delete a project."""
    store = Store(get_db_path())
    store.migrate()

    proj = store.get_project_by_name(name)
    if proj is None:
        click.echo(f"Project '{name}' not found.", err=True)
        raise SystemExit(1)

    if not force:
        if not click.confirm(f"Delete project '{name}'?"):
            return

    store.delete_project(proj.id)
    click.echo(f"Deleted project '{name}'.")