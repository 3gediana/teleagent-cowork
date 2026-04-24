import click
import sys
from rich.console import Console

from tasq.db import Store
from tasq.models import Task


console = Console()


@click.command(name="shell")
def shell_cmd():
    """Interactive REPL for tasq."""
    store = Store()
    store.migrate()

    console.print("[bold green]tasq shell[/bold green] - Type 'help' for commands, 'exit' to quit.")

    while True:
        try:
            line = input("tasq> ").strip()
        except (EOFError, KeyboardInterrupt):
            console.print("\n[yellow]Exiting shell.[/yellow]")
            break

        if not line:
            continue

        if line in ("exit", "quit", "q"):
            console.print("[yellow]Goodbye.[/yellow]")
            break

        if line == "help":
            _print_help()
            continue

        parts = line.split()
        cmd = parts[0].lower()
        args = parts[1:]

        if cmd == "ls" or cmd == "list":
            _cmd_list(store, args)
        elif cmd == "add":
            _cmd_add(store, args)
        elif cmd == "show":
            _cmd_show(store, args)
        elif cmd == "done":
            _cmd_done(store, args)
        elif cmd == "rm":
            _cmd_rm(store, args)
        elif cmd == "projects":
            _cmd_projects(store, args)
        elif cmd == "tags":
            _cmd_tags(store, args)
        else:
            console.print(f"[red]Unknown command: {cmd}[/red]")
            console.print("Type 'help' for available commands.")


def _print_help():
    console.print("[bold]Available commands:[/bold]")
    console.print("  ls, list          List all tasks")
    console.print("  add TITLE [-p PRIORITY]  Add a task")
    console.print("  show ID           Show task details")
    console.print("  done ID           Mark task as done")
    console.print("  rm ID             Delete a task")
    console.print("  projects          List projects")
    console.print("  tags              List tags")
    console.print("  exit, quit        Exit shell")


def _cmd_list(store: Store, args: list[str]):
    tasks = store.list_tasks()
    if not tasks:
        console.print("No tasks found.")
        return
    for t in tasks:
        status_icon = _status_icon(t.status)
        console.print(f"  [{t.id}] {status_icon} {t.title} ({t.priority})")


def _cmd_add(store: Store, args: list[str]):
    if not args:
        console.print("[red]Usage: add TITLE [-p PRIORITY][/red]")
        return

    title = args[0]
    priority = "medium"
    if len(args) > 1 and args[1] == "-p" and len(args) > 2:
        priority = args[2]

    task = Task(
        id=None,
        title=title,
        description=None,
        priority=priority,
        status="todo",
        project_id=None,
        due_date=None,
        created_at=None,
        updated_at=None,
        completed_at=None,
        tags=[],
    )
    tid = store.add_task(task)
    console.print(f"[green]Added task #{tid}: {title}[/green]")


def _cmd_show(store: Store, args: list[str]):
    if not args:
        console.print("[red]Usage: show ID[/red]")
        return
    try:
        tid = int(args[0])
    except ValueError:
        console.print("[red]Invalid ID[/red]")
        return

    task = store.get_task(tid)
    if not task:
        console.print(f"[red]Task #{tid} not found[/red]")
        return

    console.print(f"[bold]Task #{task.id}[/bold]")
    console.print(f"  Title: {task.title}")
    console.print(f"  Status: {task.status}")
    console.print(f"  Priority: {task.priority}")
    if task.due_date:
        console.print(f"  Due: {task.due_date}")
    if task.tags:
        console.print(f"  Tags: {', '.join(task.tags)}")
    if task.description:
        console.print(f"  Description: {task.description}")


def _cmd_done(store: Store, args: list[str]):
    if not args:
        console.print("[red]Usage: done ID[/red]")
        return
    try:
        tid = int(args[0])
    except ValueError:
        console.print("[red]Invalid ID[/red]")
        return

    task = store.get_task(tid)
    if not task:
        console.print(f"[red]Task #{tid} not found[/red]")
        return

    store.update_task(tid, status="done")
    console.print(f"[green]Task #{tid} marked as done[/green]")


def _cmd_rm(store: Store, args: list[str]):
    if not args:
        console.print("[red]Usage: rm ID[/red]")
        return
    try:
        tid = int(args[0])
    except ValueError:
        console.print("[red]Invalid ID[/red]")
        return

    store.delete_task(tid)
    console.print(f"[green]Task #{tid} deleted[/green]")


def _cmd_projects(store: Store, args: list[str]):
    projects = store.list_projects()
    if not projects:
        console.print("No projects found.")
        return
    for p in projects:
        console.print(f"  [{p.id}] {p.name}")


def _cmd_tags(store: Store, args: list[str]):
    tags = store.list_tags()
    if not tags:
        console.print("No tags found.")
        return
    for tag in tags:
        console.print(f"  {tag.name}")


def _status_icon(status: str) -> str:
    icons = {
        "todo": "[ ]",
        "in_progress": "[*]",
        "blocked": "[!]",
        "done": "[x]",
        "cancelled": "[-]",
    }
    return icons.get(status, "[?]")