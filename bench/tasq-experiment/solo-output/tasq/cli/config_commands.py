"""Config subcommands for tasq CLI."""
from __future__ import annotations

import click

from ..config import Config, load_config
from ..models import get_tasq_home, get_config_path


@click.group()
def config() -> None:
    """Manage tasq configuration."""
    pass


@config.command("get")
@click.argument("key")
def config_get(key: str) -> None:
    """Get a config value."""
    cfg = load_config()
    value = cfg.get(key)
    if value is None:
        click.echo(f"Key '{key}' not found.", err=True)
        raise SystemExit(1)
    click.echo(value)


@config.command("set")
@click.argument("key")
@click.argument("value")
def config_set(key: str, value: str) -> None:
    """Set a config value."""
    cfg = load_config()
    cfg.set(key, value)
    click.echo(f"Set {key} = {value}")


@config.command("path")
def config_path() -> None:
    """Show config file path."""
    path = get_config_path()
    click.echo(str(path))


@config.command("list")
def config_list() -> None:
    """List all config values."""
    cfg = load_config()
    click.echo(f"Config path: {cfg.config_path}")
    click.echo(f"TASQ_HOME: {get_tasq_home()}")
    click.echo("\nCurrent settings:")
    for section, values in cfg._data.items():
        click.echo(f"\n[{section}]")
        if isinstance(values, dict):
            for k, v in values.items():
                click.echo(f"  {k} = {v}")