import click
from tasq.cli.tasks import tasks
from tasq.cli.projects import projects
from tasq.cli.tags import tags


@click.group()
def cli():
    """tasq — task management CLI"""
    pass


cli.add_command(tasks)
cli.add_command(projects)
cli.add_command(tags)


if __name__ == "__main__":
    cli()
