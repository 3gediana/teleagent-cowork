"""Import/export subcommands for tasq CLI."""
from __future__ import annotations

import json
import csv
import click
from pathlib import Path
from typing import Optional, Any

from ..db import Store
from ..models import Task, Project, Tag, get_db_path
from ..formatters import task_table, project_table, tag_table
from rich.console import Console


console = Console()


@click.group()
def import_cmd() -> None:
    """Import data."""
    pass


@click.group()
def export() -> None:
    """Export data."""
    pass


@import_cmd.command("json")
@click.argument("file_path", type=click.Path(exists=True))
def import_json(file_path: Path) -> None:
    """Import tasks from JSON file."""
    store = Store(get_db_path())
    store.migrate()

    with open(file_path, "r", encoding="utf-8") as f:
        data = json.load(f)

    imported_tasks = 0
    imported_projects = 0

    if "projects" in data:
        for proj_data in data["projects"]:
            proj = Project.from_dict(proj_data)
            proj.id = None
            store.add_project(proj)
            imported_projects += 1

    if "tasks" in data:
        for task_data in data["tasks"]:
            task = Task.from_dict(task_data)
            task.id = None
            if task.project_id is not None:
                proj = store.get_project_by_name(
                    proj_data.get("name", "") if isinstance(proj_data, dict) else ""
                )
                if proj:
                    task.project_id = proj.id
            store.add_task(task)
            imported_tasks += 1

    click.echo(f"Imported {imported_projects} projects and {imported_tasks} tasks from JSON.")


@export.command("json")
@click.option("-o", "--out", "output_file", type=click.Path(), help="Output file path")
def export_json(output_file: Optional[Path]) -> None:
    """Export all data as JSON."""
    store = Store(get_db_path())
    store.migrate()

    projects = store.list_projects()
    tasks = store.list_tasks()

    data = {
        "projects": [p.to_dict() for p in projects],
        "tasks": [t.to_dict() for t in tasks],
        "tags": [tag.to_dict() for tag in store.list_tags()],
    }

    json_str = json.dumps(data, indent=2, ensure_ascii=False)

    if output_file:
        with open(output_file, "w", encoding="utf-8") as f:
            f.write(json_str)
        click.echo(f"Exported to {output_file}")
    else:
        click.echo(json_str)


@import_cmd.command("csv")
@click.argument("file_path", type=click.Path(exists=True))
def import_csv(file_path: Path) -> None:
    """Import tasks from CSV file."""
    store = Store(get_db_path())
    store.migrate()

    with open(file_path, "r", encoding="utf-8", newline="") as f:
        reader = csv.DictReader(f)
        count = 0
        for row in reader:
            due_date = None
            if row.get("due_date"):
                try:
                    from datetime import date
                    due_date = date.fromisoformat(row["due_date"])
                except Exception:
                    pass

            task = Task(
                title=row.get("title", ""),
                description=row.get("description"),
                priority=row.get("priority", "medium"),
                status=row.get("status", "todo"),
                project_id=None,
                due_date=due_date,
                tags=[],
            )
            store.add_task(task)
            count += 1

    click.echo(f"Imported {count} tasks from CSV.")


@export.command("csv")
@click.option("-o", "--out", "output_file", type=click.Path(), help="Output file path")
def export_csv(output_file: Optional[Path]) -> None:
    """Export tasks as CSV."""
    store = Store(get_db_path())
    store.migrate()

    tasks = store.list_tasks()

    rows = []
    for t in tasks:
        rows.append({
            "id": t.id,
            "title": t.title,
            "description": t.description or "",
            "priority": t.priority,
            "status": t.status,
            "project_id": t.project_id,
            "due_date": t.due_date.isoformat() if t.due_date else "",
            "tags": ",".join(t.tags),
            "created_at": t.created_at.isoformat(),
            "updated_at": t.updated_at.isoformat(),
            "completed_at": t.completed_at.isoformat() if t.completed_at else "",
        })

    output = csv.DictReader([])
    if not rows:
        return

    fieldnames = list(rows[0].keys())
    csv_str = ",".join(fieldnames) + "\n"

    for row in rows:
        csv_str += ",".join(str(row.get(f, "")) for f in fieldnames) + "\n"

    if output_file:
        with open(output_file, "w", encoding="utf-8", newline="") as f:
            f.write(csv_str)
        click.echo(f"Exported {len(rows)} tasks to {output_file}")
    else:
        click.echo(csv_str)


@import_cmd.command("markdown")
@click.argument("file_path", type=click.Path(exists=True))
def import_markdown(file_path: Path) -> None:
    """Import tasks from markdown file (parse checkboxes)."""
    store = Store(get_db_path())
    store.migrate()

    with open(file_path, "r", encoding="utf-8") as f:
        content = f.read()

    lines = content.split("\n")
    count = 0

    for line in lines:
        line = line.strip()
        if line.startswith("- [ ]") or line.startswith("* [ ]"):
            title = line[5:].strip()
            task = Task(title=title, status="todo", priority="medium")
            store.add_task(task)
            count += 1
        elif line.startswith("- [x]") or line.startswith("* [x]"):
            title = line[5:].strip()
            task = Task(title=title, status="done", priority="medium")
            store.add_task(task)
            count += 1

    click.echo(f"Imported {count} tasks from markdown.")


@export.command("markdown")
@click.option("-o", "--out", "output_file", type=click.Path(), help="Output file path")
@click.option("-p", "--project", "project_name", help="Filter by project")
def export_markdown(output_file: Optional[Path], project_name: Optional[str]) -> None:
    """Export tasks as markdown checklist."""
    store = Store(get_db_path())
    store.migrate()

    proj_id = None
    if project_name:
        proj = store.get_project_by_name(project_name)
        if proj:
            proj_id = proj.id

    tasks = store.list_tasks(project_id=proj_id)

    lines = ["# TASQ Tasks\n"]

    current_status = None
    for t in tasks:
        if t.status != current_status:
            if current_status is not None:
                lines.append("")
            lines.append(f"## {t.status.replace('_', ' ').title()}\n")
            current_status = t.status

        checkbox = "[x]" if t.status == "done" else "[ ]"
        priority_marker = {"urgent": "!!!", "high": "!!", "low": "", "medium": ""}.get(t.priority, "")
        line = f"- [{checkbox}] {t.title}"
        if priority_marker:
            line += f" {priority_marker}"
        if t.due_date:
            line += f" (due: {t.due_date.isoformat()})"
        lines.append(line)

    markdown = "\n".join(lines) + "\n"

    if output_file:
        with open(output_file, "w", encoding="utf-8") as f:
            f.write(markdown)
        click.echo(f"Exported {len(tasks)} tasks to {output_file}")
    else:
        click.echo(markdown)


# Add format dispatch to import
@import_cmd.command()
@click.argument("file_path", type=click.Path(exists=True))
@click.option("--format", "fmt", type=click.Choice(["json", "csv", "markdown"]), default="json")
def import_data(file_path: Path, fmt: str) -> None:
    """Import data from file (auto-detect format)."""
    if fmt == "json":
        import_json.main(standalone_mode=False, file_path=file_path)
    elif fmt == "csv":
        import_csv.main(standalone_mode=False, file_path=file_path)
    elif fmt == "markdown":
        import_markdown.main(standalone_mode=False, file_path=file_path)


# Add format dispatch to export
@export.command()
@click.option("-o", "--out", "output_file", type=click.Path(), help="Output file path")
@click.option("--format", "fmt", type=click.Choice(["json", "csv", "markdown"]), default="json")
def export_data(output_file: Optional[Path], fmt: str) -> None:
    """Export data to file."""
    if fmt == "json":
        export_json.main(standalone_mode=False, output_file=output_file)
    elif fmt == "csv":
        export_csv.main(standalone_mode=False, output_file=output_file)
    elif fmt == "markdown":
        export_markdown.main(standalone_mode=False, output_file=output_file, project_name=None)