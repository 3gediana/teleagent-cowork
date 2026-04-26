"""Interactive shell for tasq."""
from __future__ import annotations

import click
import sys
from pathlib import Path
from typing import Optional

from ..db import Store
from ..models import get_db_path
from ..formatters import task_table, format_status
from rich.console import Console


console = Console()


HELP_TEXT = """
tasq shell - Interactive Task Manager
======================================
Commands:
  ls, list                 List all tasks
  ls -p PROJECT            List tasks in project
  ls -s STATUS             List tasks by status
  ls --overdue             List overdue tasks
  add TITLE                Add new task
  show ID                  Show task details
  done ID                  Mark task done
  rm ID                    Delete task
  proj add NAME            Add project
  proj ls                  List projects
  tag ls                   List tags
  stats                    Show statistics
  exit, quit, q            Exit shell

Examples:
  add Fix the login bug -p web -P high -t bug -t auth
  ls -s in_progress
  done 5
  proj add myproject
"""


@click.command("shell")
@click.option("--debug", is_flag=True, help="Enable debug mode")
def shell(debug: bool) -> None:
    """Interactive REPL for tasq."""
    store = Store(get_db_path())
    store.migrate()

    console.print("[bold cyan]Welcome to tasq shell![/bold cyan]")
    console.print("Type 'help' for commands, 'exit' to quit.\n")

    while True:
        try:
            line = console.input("[bold]>[/bold] ")
        except (EOFError, KeyboardInterrupt):
            console.print("\n[yellow]Exiting...[/yellow]")
            break

        line = line.strip()
        if not line:
            continue

        parts = line.split()
        cmd = parts[0].lower()

        if cmd in ("exit", "quit", "q"):
            console.print("[yellow]Goodbye![/yellow]")
            break

        if cmd == "help":
            console.print(HELP_TEXT)
            continue

        if cmd in ("ls", "list"):
            handle_list(store, parts)
            continue

        if cmd == "add":
            handle_add(store, parts[1:])
            continue

        if cmd == "show":
            if len(parts) < 2:
                console.print("[red]Usage: show ID[/red]")
                continue
            handle_show(store, parts[1])
            continue

        if cmd == "done":
            if len(parts) < 2:
                console.print("[red]Usage: done ID[/red]")
                continue
            handle_done(store, parts[1])
            continue

        if cmd == "rm":
            if len(parts) < 2:
                console.print("[red]Usage: rm ID[/red]")
                continue
            handle_rm(store, parts[1])
            continue

        if cmd == "proj":
            handle_proj(store, parts[1:])
            continue

        if cmd == "tag":
            handle_tag(store, parts[1:])
            continue

        if cmd == "stats":
            handle_stats(store)
            continue

        console.print(f"[red]Unknown command: {cmd}[/red]")
        console.print("Type 'help' for available commands.")


def handle_list(store: Store, parts: list[str]) -> None:
    project_name: Optional[str] = None
    status_filter: Optional[str] = None
    overdue = False

    i = 1
    while i < len(parts):
        if parts[i] == "-p" and i + 1 < len(parts):
            project_name = parts[i + 1]
            i += 2
        elif parts[i] == "-s" and i + 1 < len(parts):
            status_filter = parts[i + 1]
            i += 2
        elif parts[i] == "--overdue":
            overdue = True
            i += 1
        else:
            i += 1

    proj_id: Optional[int] = None
    if project_name:
        proj = store.get_project_by_name(project_name)
        if proj:
            proj_id = proj.id

    tasks = store.list_tasks(project_id=proj_id, status=status_filter, overdue=overdue)
    if not tasks:
        console.print("No tasks found.")
        return

    table = task_table(tasks)
    console.print(table)


def handle_add(store: Store, args: list[str]) -> None:
    from ..models import Task

    title_parts = []
    tags = []
    project_name: Optional[str] = None
    priority = "medium"

    i = 0
    while i < len(args):
        if args[i] == "-p" and i + 1 < len(args):
            project_name = args[i + 1]
            i += 2
        elif args[i] == "-P" and i + 1 < len(args):
            priority = args[i + 1]
            i += 2
        elif args[i] == "-t" and i + 1 < len(args):
            tags.append(args[i + 1])
            i += 2
        else:
            title_parts.append(args[i])
            i += 1

    if not title_parts:
        console.print("[red]Title required[/red]")
        return

    title = " ".join(title_parts)
    proj_id = None
    if project_name:
        proj = store.get_project_by_name(project_name)
        if proj:
            proj_id = proj.id

    task = Task(title=title, priority=priority, tags=tags)
    task_id = store.add_task(task)
    console.print(f"[green]Created task #{task_id}: {title}[/green]")


def handle_show(store: Store, id_str: str) -> None:
    try:
        task_id = int(id_str)
    except ValueError:
        console.print("[red]Invalid ID[/red]")
        return

    task = store.get_task(task_id)
    if not task:
        console.print(f"[red]Task #{task_id} not found[/red]")
        return

    from ..formatters import task_detail_panel
    panel = task_detail_panel(task)
    console.print(panel)


def handle_done(store: Store, id_str: str) -> None:
    try:
        task_id = int(id_str)
    except ValueError:
        console.print("[red]Invalid ID[/red]")
        return

    task = store.get_task(task_id)
    if not task:
        console.print(f"[red]Task #{task_id} not found[/red]")
        return

    from datetime import datetime
    store.update_task(task_id, status="done", completed_at=datetime.utcnow())
    console.print(f"[green]Task #{task_id} marked as done[/green]")


def handle_rm(store: Store, id_str: str) -> None:
    try:
        task_id = int(id_str)
    except ValueError:
        console.print("[red]Invalid ID[/red]")
        return

    task = store.get_task(task_id)
    if not task:
        console.print(f"[red]Task #{task_id} not found[/red]")
        return

    store.delete_task(task_id)
    console.print(f"[green]Deleted task #{task_id}[/green]")


def handle_proj(store: Store, args: list[str]) -> None:
    from ..models import Project

    if not args:
        console.print("[red]Usage: proj add NAME or proj ls[/red]")
        return

    subcmd = args[0].lower()
    if subcmd == "add" and len(args) > 1:
        name = args[1]
        proj = Project(name=name)
        proj_id = store.add_project(proj)
        console.print(f"[green]Created project #{proj_id}: {name}[/green]")
    elif subcmd == "ls":
        projects = store.list_projects()
        if not projects:
            console.print("No projects.")
            return
        from ..formatters import project_table
        table = project_table(projects)
        console.print(table)
    else:
        console.print("[red]Unknown proj command[/red]")


def handle_tag(store: Store, args: list[str]) -> None:
    if not args:
        console.print("[red]Usage: tag ls[/red]")
        return

    subcmd = args[0].lower()
    if subcmd == "ls":
        tags = store.list_tags()
        if not tags:
            console.print("No tags.")
            return
        from ..formatters import tag_table
        table = tag_table(tags)
        console.print(table)
    else:
        console.print("[red]Unknown tag command[/red]")


def handle_stats(store: Store) -> None:
    all_tasks = store.list_tasks()
    total = len(all_tasks)
    by_status: dict[str, int] = {}
    for t in all_tasks:
        by_status[t.status] = by_status.get(t.status, 0) + 1

    console.print(f"[bold]Total tasks:[/bold] {total}")
    for status, count in by_status.items():
        console.print(f"  {format_status(status)}: {count}")