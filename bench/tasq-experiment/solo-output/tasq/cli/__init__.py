"""CLI entry point and command group for tasq."""
from __future__ import annotations

import click

from ..models import get_db_path


@click.group()
@click.version_option(version="1.0.0")
def cli() -> None:
    """Task management CLI with projects, tags, and reporting."""
    pass


def get_store() -> Store:
    from ..db import Store
    return Store(get_db_path())


from .tasks import task
from .projects import project
from .tags import tag
from .reports import report
from .io import import_cmd, export
from .config_commands import config
from .shell import shell


cli.add_command(task, name="task")
cli.add_command(project, name="project")
cli.add_command(tag, name="tag")
cli.add_command(report, name="report")
cli.add_command(import_cmd, name="import")
cli.add_command(export, name="export")
cli.add_command(config, name="config")
cli.add_command(shell, name="shell")


if __name__ == "__main__":
    cli()