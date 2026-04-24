import click
from typing import Optional
from tasq.db import Store
from tasq.models import Tag


def _resolve_store():
    from tasq.config import get_store
    return get_store()


@click.group(name="tag")
def tags():
    """Tag management commands"""
    pass


@tags.command()
def list():
    """List all tags"""
    store = _resolve_store()
    tag_list = store.list_tags()

    if not tag_list:
        click.echo("No tags found.")
        return

    for tag in tag_list:
        click.echo(f"{tag.id}: {tag.name}")


@tags.command()
@click.argument("old_name")
@click.argument("new_name")
def rename(old_name: str, new_name: str):
    """Rename a tag"""
    store = _resolve_store()
    tag = store.get_tag_by_name(old_name)

    if not tag:
        click.echo(f"Tag '{old_name}' not found.")
        return

    store.update_tag(tag.id, name=new_name)
    click.echo(f"Renamed tag '{old_name}' to '{new_name}'.")


@tags.command()
@click.argument("name")
def rm(name: str):
    """Remove a tag"""
    store = _resolve_store()
    tag = store.get_tag_by_name(name)

    if not tag:
        click.echo(f"Tag '{name}' not found.")
        return

    store.delete_tag(tag.id)
    click.echo(f"Tag '{name}' deleted.")
