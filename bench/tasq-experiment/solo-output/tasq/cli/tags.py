"""Tag subcommands for tasq CLI."""
from __future__ import annotations

import click
from typing import Optional

from ..db import Store
from ..models import Tag, get_db_path
from ..formatters import tag_table
from rich.console import Console


console = Console()


@click.group()
def tag() -> None:
    """Manage tags."""
    pass


@tag.command("list")
def tag_list() -> None:
    """List all tags."""
    store = Store(get_db_path())
    store.migrate()

    tags = store.list_tags()
    if not tags:
        click.echo("No tags found.")
        return

    table = tag_table(tags)
    console.print(table)


@tag.command("rename")
@click.argument("old_name")
@click.argument("new_name")
def tag_rename(old_name: str, new_name: str) -> None:
    """Rename a tag."""
    store = Store(get_db_path())
    store.migrate()

    existing = store.get_tag_by_name(old_name)
    if existing is None:
        click.echo(f"Tag '{old_name}' not found.", err=True)
        raise SystemExit(1)

    store.update_tag(existing.id, new_name)
    click.echo(f"Renamed tag '{old_name}' to '{new_name}'.")


@tag.command("rm")
@click.argument("name")
@click.option("-f", "--force", is_flag=True, help="Skip confirmation")
def tag_rm(name: str, force: bool) -> None:
    """Delete a tag."""
    store = Store(get_db_path())
    store.migrate()

    existing = store.get_tag_by_name(name)
    if existing is None:
        click.echo(f"Tag '{name}' not found.", err=True)
        raise SystemExit(1)

    if not force:
        if not click.confirm(f"Delete tag '{name}'?"):
            return

    store.delete_tag(existing.id)
    click.echo(f"Deleted tag '{name}'.")


@tag.command("show")
@click.argument("name")
def tag_show(name: str) -> None:
    """Show tag details."""
    store = Store(get_db_path())
    store.migrate()

    existing = store.get_tag_by_name(name)
    if existing is None:
        click.echo(f"Tag '{name}' not found.", err=True)
        raise SystemExit(1)

    from rich.panel import Panel

    lines = [
        f"[bold]ID:[/bold] {existing.id}",
        f"[bold]Name:[/bold] {existing.name}",
    ]
    panel = Panel("\n".join(lines), title=f"Tag #{existing.id}", border_style="cyan")
    console.print(panel)