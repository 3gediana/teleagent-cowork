"""Database storage layer for tasq."""
from __future__ import annotations

import sqlite3
import contextlib
from datetime import datetime, date
from pathlib import Path
from typing import Any, Optional

from .models import Task, Project, Tag, TaskDeps


CREATE_SCHEMA = """
CREATE TABLE IF NOT EXISTS projects (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL UNIQUE,
    description TEXT,
    created_at TEXT NOT NULL,
    archived INTEGER NOT NULL DEFAULT 0
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
        self.db_path.parent.mkdir(parents=True, exist_ok=True)
        self._conn: Optional[sqlite3.Connection] = None

    def _get_conn(self) -> sqlite3.Connection:
        if self._conn is None:
            self._conn = sqlite3.connect(str(self.db_path))
            self._conn.row_factory = sqlite3.Row
        return self._conn

    def close(self) -> None:
        if self._conn:
            self._conn.close()
            self._conn = None

    def __enter__(self) -> Store:
        return self

    def __exit__(self, *args: Any) -> None:
        self.close()

    def migrate(self) -> None:
        conn = self._get_conn()
        conn.executescript(CREATE_SCHEMA)
        conn.commit()

    def _now_iso(self) -> str:
        return datetime.utcnow().isoformat()

    def _parse_iso(self, s: Optional[str]) -> Optional[datetime]:
        if s is None:
            return None
        return datetime.fromisoformat(s)

    def _parse_date(self, s: Optional[str]) -> Optional[date]:
        if s is None:
            return None
        return date.fromisoformat(s)

    def add_project(self, project: Project) -> int:
        conn = self._get_conn()
        cur = conn.execute(
            "INSERT INTO projects (name, description, created_at) VALUES (?, ?, ?)",
            (project.name, project.description, self._now_iso()),
        )
        conn.commit()
        return cur.lastrowid  # type: ignore

    def get_project(self, id: int) -> Optional[Project]:
        conn = self._get_conn()
        row = conn.execute("SELECT * FROM projects WHERE id = ?", (id,)).fetchone()
        if row is None:
            return None
        return Project(
            id=row["id"],
            name=row["name"],
            description=row["description"],
            created_at=datetime.fromisoformat(row["created_at"]),
            archived=False,
        )

    def get_project_by_name(self, name: str) -> Optional[Project]:
        conn = self._get_conn()
        row = conn.execute(
            "SELECT * FROM projects WHERE name = ?", (name,)
        ).fetchone()
        if row is None:
            return None
        return Project(
            id=row["id"],
            name=row["name"],
            description=row["description"],
            created_at=datetime.fromisoformat(row["created_at"]),
            archived=False,
        )

    def list_projects(self, include_archived: bool = False) -> list[Project]:
        conn = self._get_conn()
        if include_archived:
            rows = conn.execute("SELECT * FROM projects ORDER BY name").fetchall()
        else:
            rows = conn.execute(
                "SELECT * FROM projects ORDER BY name"
            ).fetchall()
        return [
            Project(
                id=row["id"],
                name=row["name"],
                description=row["description"],
                created_at=datetime.fromisoformat(row["created_at"]),
                archived=False,
            )
            for row in rows
        ]

    def update_project(self, id: int, **fields: Any) -> None:
        allowed = {"name", "description", "archived"}
        sets = []
        vals = []
        for k, v in fields.items():
            if k in allowed:
                sets.append(f"{k} = ?")
                vals.append(v)
        if not sets:
            return
        vals.append(id)
        conn = self._get_conn()
        conn.execute(f"UPDATE projects SET {', '.join(sets)} WHERE id = ?", vals)
        conn.commit()

    def delete_project(self, id: int) -> None:
        conn = self._get_conn()
        conn.execute("DELETE FROM projects WHERE id = ?", (id,))
        conn.commit()

    def add_tag(self, tag: Tag) -> int:
        conn = self._get_conn()
        cur = conn.execute(
            "INSERT OR IGNORE INTO tags (name) VALUES (?)", (tag.name,)
        )
        conn.commit()
        if cur.rowcount == 0:
            existing = conn.execute(
                "SELECT id FROM tags WHERE name = ?", (tag.name,)
            ).fetchone()
            return existing["id"]  # type: ignore
        return cur.lastrowid  # type: ignore

    def get_tag(self, id: int) -> Optional[Tag]:
        conn = self._get_conn()
        row = conn.execute("SELECT * FROM tags WHERE id = ?", (id,)).fetchone()
        if row is None:
            return None
        return Tag(id=row["id"], name=row["name"])

    def get_tag_by_name(self, name: str) -> Optional[Tag]:
        conn = self._get_conn()
        row = conn.execute("SELECT * FROM tags WHERE name = ?", (name,)).fetchone()
        if row is None:
            return None
        return Tag(id=row["id"], name=row["name"])

    def list_tags(self) -> list[Tag]:
        conn = self._get_conn()
        rows = conn.execute("SELECT * FROM tags ORDER BY name").fetchall()
        return [Tag(id=row["id"], name=row["name"]) for row in rows]

    def update_tag(self, id: int, name: str) -> None:
        conn = self._get_conn()
        conn.execute("UPDATE tags SET name = ? WHERE id = ?", (name, id))
        conn.commit()

    def delete_tag(self, id: int) -> None:
        conn = self._get_conn()
        conn.execute("DELETE FROM tags WHERE id = ?", (id,))
        conn.commit()

    def _task_from_row(self, row: sqlite3.Row) -> Task:
        conn = self._get_conn()
        tag_rows = conn.execute(
            "SELECT t.name FROM tags t "
            "JOIN task_tags tt ON tt.tag_id = t.id "
            "WHERE tt.task_id = ?",
            (row["id"],),
        ).fetchall()
        tags = [r["name"] for r in tag_rows]
        return Task(
            id=row["id"],
            title=row["title"],
            description=row["description"],
            priority=row["priority"],
            status=row["status"],
            project_id=row["project_id"],
            due_date=self._parse_date(row["due_date"]),
            created_at=datetime.fromisoformat(row["created_at"]),
            updated_at=datetime.fromisoformat(row["updated_at"]),
            completed_at=self._parse_iso(row["completed_at"]),
            tags=tags,
        )

    def add_task(self, task: Task) -> int:
        conn = self._get_conn()
        now = self._now_iso()
        cur = conn.execute(
            "INSERT INTO tasks (title, description, priority, status, "
            "project_id, due_date, created_at, updated_at, completed_at) "
            "VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
            (
                task.title,
                task.description,
                task.priority,
                task.status,
                task.project_id,
                task.due_date.isoformat() if task.due_date else None,
                now,
                now,
                task.completed_at.isoformat() if task.completed_at else None,
            ),
        )
        task_id = cur.lastrowid  # type: ignore
        conn.commit()
        for tag_name in task.tags:
            tag = Tag(name=tag_name)
            tag_id = self.add_tag(tag)
            conn.execute(
                "INSERT OR IGNORE INTO task_tags (task_id, tag_id) VALUES (?, ?)",
                (task_id, tag_id),
            )
        conn.commit()
        return task_id

    def get_task(self, id: int) -> Optional[Task]:
        conn = self._get_conn()
        row = conn.execute("SELECT * FROM tasks WHERE id = ?", (id,)).fetchone()
        if row is None:
            return None
        return self._task_from_row(row)

    def list_tasks(
        self,
        project_id: Optional[int] = None,
        status: Optional[str] = None,
        tag: Optional[str] = None,
        overdue: bool = False,
    ) -> list[Task]:
        conn = self._get_conn()
        parts = ["SELECT t.* FROM tasks t"]
        joins = []
        wheres = []
        params: list[Any] = []

        if tag:
            joins.append(
                "JOIN task_tags tt ON tt.task_id = t.id "
                "JOIN tags tg ON tg.id = tt.tag_id"
            )
            wheres.append("tg.name = ?")
            params.append(tag)

        if project_id is not None:
            wheres.append("t.project_id = ?")
            params.append(project_id)

        if status:
            wheres.append("t.status = ?")
            params.append(status)

        if overdue:
            wheres.append(
                "t.due_date < date('now') AND t.status NOT IN ('done','cancelled')"
            )

        if joins:
            parts.append(", ".join(joins))
        if wheres:
            parts.append("WHERE " + " AND ".join(wheres))

        query = " ".join(parts) + " ORDER BY t.created_at DESC"
        rows = conn.execute(query, params).fetchall()
        return [self._task_from_row(row) for row in rows]

    def update_task(self, id: int, **fields: Any) -> None:
        allowed = {
            "title",
            "description",
            "priority",
            "status",
            "project_id",
            "due_date",
            "completed_at",
        }
        sets = ["updated_at = ?"]
        vals: list[Any] = [self._now_iso()]
        for k, v in fields.items():
            if k in allowed:
                if k == "due_date" and v is not None:
                    v = v.isoformat() if isinstance(v, date) else v
                elif k == "completed_at" and v is not None:
                    v = v.isoformat() if isinstance(v, datetime) else v
                sets.append(f"{k} = ?")
                vals.append(v)
        vals.append(id)
        conn = self._get_conn()
        conn.execute(f"UPDATE tasks SET {', '.join(sets)} WHERE id = ?", vals)
        conn.commit()

    def delete_task(self, id: int) -> None:
        conn = self._get_conn()
        conn.execute("DELETE FROM tasks WHERE id = ?", (id,))
        conn.commit()

    def add_task_tag(self, task_id: int, tag_name: str) -> None:
        conn = self._get_conn()
        tag_id = self.add_tag(Tag(name=tag_name))
        conn.execute(
            "INSERT OR IGNORE INTO task_tags (task_id, tag_id) VALUES (?, ?)",
            (task_id, tag_id),
        )
        conn.commit()

    def remove_task_tag(self, task_id: int, tag_name: str) -> None:
        conn = self._get_conn()
        tag = self.get_tag_by_name(tag_name)
        if tag:
            conn.execute(
                "DELETE FROM task_tags WHERE task_id = ? AND tag_id = ?",
                (task_id, tag.id),
            )
            conn.commit()

    def set_task_tags(self, task_id: int, tag_names: list[str]) -> None:
        conn = self._get_conn()
        conn.execute("DELETE FROM task_tags WHERE task_id = ?", (task_id,))
        for name in tag_names:
            tag_id = self.add_tag(Tag(name=name))
            conn.execute(
                "INSERT OR IGNORE INTO task_tags (task_id, tag_id) VALUES (?, ?)",
                (task_id, tag_id),
            )
        conn.commit()

    def add_task_dep(self, task_id: int, depends_on_id: int) -> None:
        conn = self._get_conn()
        conn.execute(
            "INSERT OR IGNORE INTO task_deps (task_id, depends_on_id) VALUES (?, ?)",
            (task_id, depends_on_id),
        )
        conn.commit()

    def remove_task_dep(self, task_id: int, depends_on_id: int) -> None:
        conn = self._get_conn()
        conn.execute(
            "DELETE FROM task_deps WHERE task_id = ? AND depends_on_id = ?",
            (task_id, depends_on_id),
        )
        conn.commit()

    def get_blockers(self, task_id: int) -> list[Task]:
        conn = self._get_conn()
        rows = conn.execute(
            "SELECT t.* FROM tasks t "
            "JOIN task_deps td ON td.depends_on_id = t.id "
            "WHERE td.task_id = ?",
            (task_id,),
        ).fetchall()
        return [self._task_from_row(row) for row in rows]

    def get_dependants(self, task_id: int) -> list[Task]:
        conn = self._get_conn()
        rows = conn.execute(
            "SELECT t.* FROM tasks t "
            "JOIN task_deps td ON td.task_id = t.id "
            "WHERE td.depends_on_id = ?",
            (task_id,),
        ).fetchall()
        return [self._task_from_row(row) for row in rows]

    def get_task_deps(self, task_id: int) -> list[TaskDeps]:
        conn = self._get_conn()
        rows = conn.execute(
            "SELECT depends_on_id FROM task_deps WHERE task_id = ?", (task_id,)
        ).fetchall()
        return [TaskDeps(task_id=task_id, depends_on_id=r["depends_on_id"]) for r in rows]

    def get_all_tasks_for_export(self) -> list[Task]:
        return self.list_tasks()

    def get_tasks_by_project(self, project_id: int) -> list[Task]:
        return self.list_tasks(project_id=project_id)

    def get_overdue_tasks(self) -> list[Task]:
        return self.list_tasks(overdue=True)