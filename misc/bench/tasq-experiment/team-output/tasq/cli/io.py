import csv
import io
import json
import click
from pathlib import Path
from rich.console import Console

from tasq.db import Store
from tasq.models import Task


console = Console()


def _task_to_dict(task: Task) -> dict:
    return {
        "id": task.id,
        "title": task.title,
        "description": task.description,
        "priority": task.priority,
        "status": task.status,
        "project_id": task.project_id,
        "due_date": task.due_date.isoformat() if task.due_date else None,
        "created_at": task.created_at.isoformat() if task.created_at else None,
        "updated_at": task.updated_at.isoformat() if task.updated_at else None,
        "completed_at": task.completed_at.isoformat() if task.completed_at else None,
        "tags": task.tags,
    }


def _dict_to_task(d: dict) -> dict:
    d = d.copy()
    d.pop("id", None)
    if d.get("due_date"):
        from datetime import date
        d["due_date"] = date.fromisoformat(d["due_date"])
    if d.get("created_at"):
        from datetime import datetime
        d["created_at"] = datetime.fromisoformat(d["created_at"])
    if d.get("updated_at"):
        from datetime import datetime
        d["updated_at"] = datetime.fromisoformat(d["updated_at"])
    if d.get("completed_at"):
        from datetime import datetime
        d["completed_at"] = datetime.fromisoformat(d["completed_at"])
    return d


@click.group(name="import")
def import_group():
    """Import tasks from file."""
    pass


@import_group.command(name="FILE")
@click.argument("filepath", type=click.Path(exists=True))
@click.option("--format", "fmt", type=click.Choice(["json", "csv"]), default=None, help="File format (auto-detected from extension if omitted)")
def import_cmd(filepath: str, fmt: str | None):
    """Import tasks from FILE."""
    store = Store()
    store.migrate()

    path = Path(filepath)
    if fmt is None:
        fmt = path.suffix.lstrip(".")

    if fmt == "json":
        _import_json(store, path)
    elif fmt == "csv":
        _import_csv(store, path)
    else:
        console.print(f"[red]Unknown format: {fmt}[/red]")
        return

    console.print(f"[green]Imported from {filepath}[/green]")


def _import_json(store: Store, path: Path):
    data = json.loads(path.read_text())
    if isinstance(data, dict) and "tasks" in data:
        tasks_data = data["tasks"]
    elif isinstance(data, list):
        tasks_data = data
    else:
        tasks_data = [data]

    for td in tasks_data:
        task_dict = _dict_to_task(td)
        tags = task_dict.pop("tags", [])
        task_dict["id"] = None
        task_dict["tags"] = tags
        task = Task(**task_dict)
        tid = store.add_task(task)
        for tag in tags:
            store.add_tag_to_task(tid, tag)


def _import_csv(store: Store, path: Path):
    with open(path, newline="", encoding="utf-8") as f:
        reader = csv.DictReader(f)
        for row in reader:
            task_dict = {
                "id": None,
                "title": row.get("title", ""),
                "description": row.get("description") or None,
                "priority": row.get("priority", "medium"),
                "status": row.get("status", "todo"),
                "project_id": None,
                "due_date": None,
                "created_at": None,
                "updated_at": None,
                "completed_at": None,
                "tags": [],
            }
            if row.get("due_date"):
                from datetime import date
                task_dict["due_date"] = date.fromisoformat(row["due_date"])
            if row.get("project_id") and row["project_id"].isdigit():
                task_dict["project_id"] = int(row["project_id"])
            task = Task(**task_dict)
            tid = store.add_task(task)
            tags_str = row.get("tags", "")
            if tags_str:
                for tag in tags_str.split("|"):
                    tag = tag.strip()
                    if tag:
                        store.add_tag_to_task(tid, tag)


@click.group(name="export")
def export_group():
    """Export tasks to file."""
    pass


@export_group.command(name="export")
@click.option("-o", "--out", "filepath", help="Output file (stdout if omitted)")
@click.option("--format", "fmt", type=click.Choice(["json", "csv", "markdown"]), default="json", help="Export format")
def export_cmd(filepath: str | None, fmt: str):
    """Export tasks to file."""
    store = Store()
    store.migrate()

    tasks = store.list_tasks()

    if fmt == "json":
        content = _export_json(tasks)
    elif fmt == "csv":
        content = _export_csv(tasks)
    elif fmt == "markdown":
        content = _export_markdown(tasks)
    else:
        console.print(f"[red]Unknown format: {fmt}[/red]")
        return

    if filepath:
        Path(filepath).write_text(content, encoding="utf-8")
        console.print(f"[green]Exported to {filepath}[/green]")
    else:
        console.print(content)


def _export_json(tasks: list[Task]) -> str:
    data = {"tasks": [_task_to_dict(t) for t in tasks]}
    return json.dumps(data, indent=2, ensure_ascii=False)


def _export_csv(tasks: list[Task]) -> str:
    output = io.StringIO()
    fieldnames = ["id", "title", "description", "priority", "status", "project_id", "due_date", "tags"]
    writer = csv.DictWriter(output, fieldnames=fieldnames, extrasaction="ignore")
    writer.writeheader()
    for t in tasks:
        row = {
            "id": t.id,
            "title": t.title,
            "description": t.description or "",
            "priority": t.priority,
            "status": t.status,
            "project_id": t.project_id or "",
            "due_date": t.due_date.isoformat() if t.due_date else "",
            "tags": "|".join(t.tags),
        }
        writer.writerow(row)
    return output.getvalue()


def _export_markdown(tasks: list[Task]) -> str:
    lines = ["# Tasks\n"]
    for t in tasks:
        lines.append(f"## {t.title}\n")
        lines.append(f"- **ID**: {t.id}\n")
        lines.append(f"- **Status**: {t.status}\n")
        lines.append(f"- **Priority**: {t.priority}\n")
        if t.due_date:
            lines.append(f"- **Due**: {t.due_date}\n")
        if t.tags:
            lines.append(f"- **Tags**: {', '.join(t.tags)}\n")
        if t.description:
            lines.append(f"\n{t.description}\n")
        lines.append("\n---\n")
    return "".join(lines)