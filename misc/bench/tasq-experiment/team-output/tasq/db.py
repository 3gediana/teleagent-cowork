from __future__ import annotations

import sqlite3
from datetime import date, datetime
from pathlib import Path
from typing import Any

from tasq.models import Project, Task, Tag


SCHEMA = """
CREATE TABLE IF NOT EXISTS projects (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL UNIQUE,
    description TEXT,
    created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS tags (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL UNIQUE
);

CREATE TABLE IF NOT EXISTS tasks (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    title TEXT NOT NULL,
    description TEXT,
    priority TEXT NOT NULL CHECK(priority IN ('low','medium','high','urgent')),
    status TEXT NOT NULL CHECK(status IN ('todo','in_progress','blocked','done','cancelled')),
    project_id INTEGER REFERENCES projects(id) ON DELETE SET NULL,
    due_date TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    completed_at TEXT
);

CREATE TABLE IF NOT EXISTS task_tags (
    task_id INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    tag_id INTEGER NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
    PRIMARY KEY (task_id, tag_id)
);

CREATE TABLE IF NOT EXISTS task_deps (
    task_id INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    depends_on_id INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    PRIMARY KEY (task_id, depends_on_id)
);

CREATE INDEX IF NOT EXISTS idx_tasks_status ON tasks(status);
CREATE INDEX IF NOT EXISTS idx_tasks_project ON tasks(project_id);
CREATE INDEX IF NOT EXISTS idx_tasks_due ON tasks(due_date);
"""


class Store:
    def __init__(self, db_path: Path) -> None:
        self.db_path = db_path
        self.conn: sqlite3.Connection | None = None

    def connect(self) -> None:
        self.conn = sqlite3.connect(str(self.db_path))
        self.conn.row_factory = sqlite3.Row

    def close(self) -> None:
        if self.conn:
            self.conn.close()
            self.conn = None

    def __enter__(self) -> Store:
        self.connect()
        return self

    def __exit__(self, *args: Any) -> None:
        self.close()

    def migrate(self) -> None:
        if self.conn is None:
            raise RuntimeError("Not connected")
        self.conn.executescript(SCHEMA)
        self.conn.commit()

    def _row_to_task(self, row: sqlite3.Row) -> Task:
        data = dict(row)
        tags = self.get_task_tags(data["id"])
        data["tags"] = tags
        return Task.from_row(data)

    def _row_to_project(self, row: sqlite3.Row) -> Project:
        return Project.from_row(dict(row))

    def _row_to_tag(self, row: sqlite3.Row) -> Tag:
        return Tag.from_row(dict(row))

    def add_task(self, task: Task) -> int:
        if self.conn is None:
            raise RuntimeError("Not connected")
        cursor = self.conn.cursor()
        row = task.to_row()
        now = datetime.now().isoformat()
        cursor.execute(
            """INSERT INTO tasks (title, description, priority, status, project_id,
                                 due_date, created_at, updated_at, completed_at)
               VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)""",
            (
                row["title"],
                row["description"],
                row["priority"],
                row["status"],
                row["project_id"],
                row["due_date"],
                now,
                now,
                row["completed_at"],
            ),
        )
        task_id = cursor.lastrowid
        for tag_name in task.tags:
            tag_id = self._ensure_tag(tag_name)
            cursor.execute("INSERT OR IGNORE INTO task_tags (task_id, tag_id) VALUES (?, ?)", (task_id, tag_id))
        self.conn.commit()
        return task_id

    def get_task(self, id: int) -> Task | None:
        if self.conn is None:
            raise RuntimeError("Not connected")
        cursor = self.conn.execute("SELECT * FROM tasks WHERE id = ?", (id,))
        row = cursor.fetchone()
        if row is None:
            return None
        return self._row_to_task(row)

    def list_tasks(
        self,
        status: str | None = None,
        project_id: int | None = None,
        tag: str | None = None,
        overdue: bool = False,
    ) -> list[Task]:
        if self.conn is None:
            raise RuntimeError("Not connected")
        query = "SELECT DISTINCT t.* FROM tasks t"
        params: list[Any] = []
        joins = []
        if tag:
            joins.append("JOIN task_tags tt ON t.id = tt.task_id")
            joins.append("JOIN tags tg ON tt.tag_id = tg.id")
            query += " " + " ".join(joins)
            query += " WHERE tg.name = ?"
            params.append(tag)
        else:
            where_parts = []
            if status:
                where_parts.append("t.status = ?")
                params.append(status)
            if project_id is not None:
                where_parts.append("t.project_id = ?")
                params.append(project_id)
            if overdue:
                today = date.today().isoformat()
                if where_parts:
                    query += " WHERE " + " AND ".join(where_parts) + " AND t.due_date < ? AND t.status != 'done' AND t.status != 'cancelled'"
                else:
                    query += " WHERE t.due_date < ? AND t.status != 'done' AND t.status != 'cancelled'"
                params.append(today)
            elif where_parts:
                query += " WHERE " + " AND ".join(where_parts)
        query += " ORDER BY t.created_at DESC"
        cursor = self.conn.execute(query, params)
        rows = cursor.fetchall()
        tasks = []
        for row in rows:
            data = dict(row)
            tags = self.get_task_tags(data["id"])
            data["tags"] = tags
            tasks.append(Task.from_row(data))
        return tasks

    def update_task(self, id: int, **fields: Any) -> None:
        if self.conn is None:
            raise RuntimeError("Not connected")
        allowed = {"title", "description", "priority", "status", "project_id", "due_date", "completed_at"}
        updates = []
        params: list[Any] = []
        now = datetime.now().isoformat()
        for key, value in fields.items():
            if key in allowed:
                if key == "due_date" and value is not None:
                    if isinstance(value, date):
                        value = value.isoformat()
                elif key == "completed_at" and value is not None:
                    if isinstance(value, datetime):
                        value = value.isoformat()
                updates.append(f"{key} = ?")
                params.append(value)
        updates.append("updated_at = ?")
        params.append(now)
        params.append(id)
        query = f"UPDATE tasks SET {', '.join(updates)} WHERE id = ?"
        self.conn.execute(query, params)
        if "status" in fields and fields["status"] == "done":
            self.conn.execute(
                "UPDATE tasks SET completed_at = ? WHERE id = ? AND completed_at IS NULL",
                (now, id),
            )
        self.conn.commit()

    def delete_task(self, id: int) -> None:
        if self.conn is None:
            raise RuntimeError("Not connected")
        self.conn.execute("DELETE FROM task_tags WHERE task_id = ?", (id,))
        self.conn.execute("DELETE FROM task_deps WHERE task_id = ? OR depends_on_id = ?", (id, id))
        self.conn.execute("DELETE FROM tasks WHERE id = ?", (id,))
        self.conn.commit()

    def add_project(self, project: Project) -> int:
        if self.conn is None:
            raise RuntimeError("Not connected")
        cursor = self.conn.execute(
            "INSERT INTO projects (name, description, created_at) VALUES (?, ?, ?)",
            (project.name, project.description, datetime.now().isoformat()),
        )
        self.conn.commit()
        return cursor.lastrowid

    def get_project(self, id: int) -> Project | None:
        if self.conn is None:
            raise RuntimeError("Not connected")
        cursor = self.conn.execute("SELECT * FROM projects WHERE id = ?", (id,))
        row = cursor.fetchone()
        if row is None:
            return None
        return self._row_to_project(row)

    def get_project_by_name(self, name: str) -> Project | None:
        if self.conn is None:
            raise RuntimeError("Not connected")
        cursor = self.conn.execute("SELECT * FROM projects WHERE name = ?", (name,))
        row = cursor.fetchone()
        if row is None:
            return None
        return self._row_to_project(row)

    def list_projects(self) -> list[Project]:
        if self.conn is None:
            raise RuntimeError("Not connected")
        cursor = self.conn.execute("SELECT * FROM projects ORDER BY name")
        return [self._row_to_project(dict(row)) for row in cursor.fetchall()]

    def update_project(self, id: int, **fields: Any) -> None:
        if self.conn is None:
            raise RuntimeError("Not connected")
        allowed = {"name", "description"}
        updates = []
        params: list[Any] = []
        for key, value in fields.items():
            if key in allowed:
                updates.append(f"{key} = ?")
                params.append(value)
        if updates:
            params.append(id)
            query = f"UPDATE projects SET {', '.join(updates)} WHERE id = ?"
            self.conn.execute(query, params)
            self.conn.commit()

    def delete_project(self, id: int) -> None:
        if self.conn is None:
            raise RuntimeError("Not connected")
        self.conn.execute("DELETE FROM projects WHERE id = ?", (id,))
        self.conn.commit()

    def add_tag(self, tag: Tag) -> int:
        if self.conn is None:
            raise RuntimeError("Not connected")
        cursor = self.conn.execute("INSERT OR IGNORE INTO tags (name) VALUES (?)", (tag.name,))
        self.conn.commit()
        return cursor.lastrowid

    def get_tag(self, id: int) -> Tag | None:
        if self.conn is None:
            raise RuntimeError("Not connected")
        cursor = self.conn.execute("SELECT * FROM tags WHERE id = ?", (id,))
        row = cursor.fetchone()
        if row is None:
            return None
        return self._row_to_tag(row)

    def get_tag_by_name(self, name: str) -> Tag | None:
        if self.conn is None:
            raise RuntimeError("Not connected")
        cursor = self.conn.execute("SELECT * FROM tags WHERE name = ?", (name,))
        row = cursor.fetchone()
        if row is None:
            return None
        return self._row_to_tag(row)

    def list_tags(self) -> list[Tag]:
        if self.conn is None:
            raise RuntimeError("Not connected")
        cursor = self.conn.execute("SELECT * FROM tags ORDER BY name")
        return [self._row_to_tag(dict(row)) for row in cursor.fetchall()]

    def delete_tag(self, name: str) -> None:
        if self.conn is None:
            raise RuntimeError("Not connected")
        self.conn.execute("DELETE FROM tags WHERE name = ?", (name,))
        self.conn.commit()

    def get_task_tags(self, task_id: int) -> list[str]:
        if self.conn is None:
            raise RuntimeError("Not connected")
        cursor = self.conn.execute(
            "SELECT t.name FROM tags t JOIN task_tags tt ON t.id = tt.tag_id WHERE tt.task_id = ?",
            (task_id,),
        )
        return [row["name"] for row in cursor.fetchall()]

    def _ensure_tag(self, name: str) -> int:
        if self.conn is None:
            raise RuntimeError("Not connected")
        cursor = self.conn.execute("SELECT id FROM tags WHERE name = ?", (name,))
        row = cursor.fetchone()
        if row:
            return row["id"]
        cursor = self.conn.execute("INSERT INTO tags (name) VALUES (?)", (name,))
        self.conn.commit()
        return cursor.lastrowid

    def add_dependency(self, task_id: int, depends_on_id: int) -> None:
        if self.conn is None:
            raise RuntimeError("Not connected")
        self.conn.execute(
            "INSERT OR IGNORE INTO task_deps (task_id, depends_on_id) VALUES (?, ?)",
            (task_id, depends_on_id),
        )
        self.conn.commit()

    def remove_dependency(self, task_id: int, depends_on_id: int) -> None:
        if self.conn is None:
            raise RuntimeError("Not connected")
        self.conn.execute(
            "DELETE FROM task_deps WHERE task_id = ? AND depends_on_id = ?",
            (task_id, depends_on_id),
        )
        self.conn.commit()

    def get_blockers(self, task_id: int) -> list[Task]:
        if self.conn is None:
            raise RuntimeError("Not connected")
        cursor = self.conn.execute(
            """SELECT t.* FROM tasks t
               JOIN task_deps td ON t.id = td.depends_on_id
               WHERE td.task_id = ?""",
            (task_id,),
        )
        tasks = []
        for row in cursor.fetchall():
            data = dict(row)
            tags = self.get_task_tags(data["id"])
            data["tags"] = tags
            tasks.append(Task.from_row(data))
        return tasks

    def get_dependants(self, task_id: int) -> list[Task]:
        if self.conn is None:
            raise RuntimeError("Not connected")
        cursor = self.conn.execute(
            """SELECT t.* FROM tasks t
               JOIN task_deps td ON t.id = td.task_id
               WHERE td.depends_on_id = ?""",
            (task_id,),
        )
        tasks = []
        for row in cursor.fetchall():
            data = dict(row)
            tags = self.get_task_tags(data["id"])
            data["tags"] = tags
            tasks.append(Task.from_row(data))
        return tasks

    def get_all_tasks_for_export(self) -> list[Task]:
        if self.conn is None:
            raise RuntimeError("Not connected")
        cursor = self.conn.execute("SELECT * FROM tasks ORDER BY id")
        tasks = []
        for row in cursor.fetchall():
            data = dict(row)
            tags = self.get_task_tags(data["id"])
            data["tags"] = tags
            tasks.append(Task.from_row(data))
        return tasks

    def clear_all(self) -> None:
        if self.conn is None:
            raise RuntimeError("Not connected")
        self.conn.executescript(
            """DELETE FROM task_tags; DELETE FROM task_deps; DELETE FROM tasks; DELETE FROM tags; DELETE FROM projects;"""
        )
        self.conn.commit()

    def import_tasks(self, tasks: list[Task]) -> None:
        if self.conn is None:
            raise RuntimeError("Not connected")
        for task in tasks:
            cursor = self.conn.execute(
                """INSERT INTO tasks (title, description, priority, status, project_id,
                                     due_date, created_at, updated_at, completed_at)
                   VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)""",
                (
                    task.title,
                    task.description,
                    task.priority,
                    task.status,
                    task.project_id,
                    task.due_date.isoformat() if task.due_date else None,
                    task.created_at.isoformat() if task.created_at else datetime.now().isoformat(),
                    task.updated_at.isoformat() if task.updated_at else datetime.now().isoformat(),
                    task.completed_at.isoformat() if task.completed_at else None,
                ),
            )
            task_id = cursor.lastrowid
            for tag_name in task.tags:
                tag_id = self._ensure_tag(tag_name)
                self.conn.execute(
                    "INSERT OR IGNORE INTO task_tags (task_id, tag_id) VALUES (?, ?)",
                    (task_id, tag_id),
                )
        self.conn.commit()