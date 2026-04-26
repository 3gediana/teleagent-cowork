# tasq — Task management CLI (the contract)

Python 3.11+. Pure-stdlib + click + rich + pytest + mypy + ruff.
No network. Storage = SQLite in ~/.tasq/tasq.db (or override via
$TASQ_HOME). All persistence you write must go through the Store
class below — don't shell out to the DB from commands directly.

## DB schema (MUST match exactly — contract across modules)

```sql
CREATE TABLE projects (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL UNIQUE,
    description TEXT,
    created_at TEXT NOT NULL
);

CREATE TABLE tags (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL UNIQUE
);

CREATE TABLE tasks (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    title TEXT NOT NULL,
    description TEXT,
    priority TEXT NOT NULL CHECK(priority IN ('low','medium','high','urgent')),
    status TEXT NOT NULL CHECK(status IN ('todo','in_progress','blocked','done','cancelled')),
    project_id INTEGER REFERENCES projects(id) ON DELETE SET NULL,
    due_date TEXT,               -- ISO-8601 date or NULL
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    completed_at TEXT            -- nullable; set when status -> done
);

CREATE TABLE task_tags (
    task_id INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    tag_id INTEGER NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
    PRIMARY KEY (task_id, tag_id)
);

CREATE TABLE task_deps (
    task_id INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    depends_on_id INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    PRIMARY KEY (task_id, depends_on_id)
);

CREATE INDEX idx_tasks_status ON tasks(status);
CREATE INDEX idx_tasks_project ON tasks(project_id);
CREATE INDEX idx_tasks_due ON tasks(due_date);
```

## Module layout (each file in `tasq/`)

| File | Responsibility | Approx LOC |
|---|---|---|
| `tasq/db.py` | Store class: conn mgmt, migrations, CRUD for all tables | 400+ |
| `tasq/models.py` | Dataclasses Task/Project/Tag + validation + to/from_row | 300+ |
| `tasq/config.py` | Load/save ~/.tasq/config.toml, resolve TASQ_HOME | 150+ |
| `tasq/cli/__init__.py` | click group + wire all sub-commands | 80+ |
| `tasq/cli/tasks.py` | task add / list / show / done / rm / edit / block / deps | 500+ |
| `tasq/cli/projects.py` | project add / list / rename / archive | 250+ |
| `tasq/cli/tags.py` | tag list / rename / rm + auto-create on task add | 150+ |
| `tasq/cli/reports.py` | report burndown / by-project / overdue / stats | 350+ |
| `tasq/cli/io.py` | import/export: json, csv, markdown | 400+ |
| `tasq/cli/shell.py` | interactive REPL mode (`tasq shell`) | 250+ |
| `tasq/formatters.py` | Rich table renderers used by multiple cli modules | 200+ |
| `tasq/__main__.py` | Entry point that calls cli() | 20 |

Plus `tests/` with one test file per module — aim for ≥60% coverage.

## Required CLI surface

Running `tasq --help` MUST list all of these commands. `tasq CMD --help`
must work for each. Options below are the minimum; feel free to add
sensible ones.

```
tasq task add TITLE [-p PROJECT] [-P priority] [-d DUE] [-t tag ...]
tasq task list [-p PROJECT] [-s STATUS] [-t TAG] [--overdue]
tasq task show ID
tasq task done ID
tasq task rm ID
tasq task edit ID [--title ...] [--due ...] [--priority ...] [--project ...]
tasq task block ID [--by DEP_ID]
tasq task deps ID                  # print blockers + dependants

tasq project add NAME [-d DESC]
tasq project list
tasq project rename OLD NEW
tasq project archive NAME

tasq tag list
tasq tag rename OLD NEW
tasq tag rm NAME

tasq report burndown [-p PROJECT] [--days N]
tasq report by-project
tasq report overdue
tasq report stats

tasq import FILE [--format json|csv]
tasq export [-o OUT] [--format json|csv|markdown]

tasq config get KEY
tasq config set KEY VALUE
tasq config path

tasq shell                          # interactive REPL
```

## Scenario contract (what the harness will run)

See `scenarios/smoke.py` in the experiment root. It will:

1. Create projects "web" and "infra"
2. Add 5 tasks across them with varied priorities + due dates + tags
3. Mark some as done, some as blocked (with deps)
4. Query with various filters (status, tag, project, overdue)
5. Run `report stats` and `report by-project`
6. Export to JSON
7. Wipe DB
8. Re-import the JSON
9. Verify task count + titles match exactly

The harness uses **TASQ_HOME=./scenario-home** to isolate data. Any
implementation that ignores $TASQ_HOME will lose points. Exit code
non-zero = scenario fail.

## Quality gates (harness runs these automatically)

| Check | Tool | Target |
|---|---|---|
| Source lines | `cloc tasq/` | 3000–8000 LOC (excluding tests) |
| Test coverage | `pytest --cov=tasq` | ≥60% |
| Lint | `ruff check tasq/ tests/` | ≤10 issues |
| Types | `mypy tasq/` | ≤20 errors |
| Scenario | `python scenarios/smoke.py` | exit 0 |

## Ground rules

- You MAY create additional helper modules if it clarifies the code.
- You MUST NOT use external deps beyond: click, rich, pytest, pytest-cov,
  mypy, ruff, tomli (python <3.11), tomli-w. Everything else = stdlib.
- Tests go in `tests/` at the project root, mirror module structure.
- `tasq/db.py` Store class MUST expose:
    Store(db_path: Path)
    Store.migrate() -> None
    Store.add_task(task: Task) -> int
    Store.get_task(id: int) -> Optional[Task]
    Store.list_tasks(**filters) -> list[Task]
    Store.update_task(id: int, **fields) -> None
    Store.delete_task(id: int) -> None
    (analogous methods for projects, tags, deps)
- `tasq/models.py` Task dataclass MUST have these fields (any name
  mismatch breaks the contract):
    id: int | None, title: str, description: str | None,
    priority: Literal['low','medium','high','urgent'],
    status: Literal['todo','in_progress','blocked','done','cancelled'],
    project_id: int | None, due_date: date | None,
    created_at: datetime, updated_at: datetime,
    completed_at: datetime | None, tags: list[str] (populated on load)

If you find ambiguity, **prefer the obvious behaviour**; don't ask back.
