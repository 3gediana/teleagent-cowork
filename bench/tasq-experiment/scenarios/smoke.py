"""End-to-end smoke test against a tasq install.

Run from inside whichever output directory you want to grade. It
uses a disposable TASQ_HOME next to cwd so nothing pollutes the
real home directory. Exits 0 on full pass, non-zero on the first
failure — a fail-fast grader, not a traditional test suite.

Usage:
    python scenarios/smoke.py            # runs against the tasq in the
                                         # current interpreter path
Every assertion prints a [PASS] / [FAIL] line so the harness can
tail the log and know exactly which step broke.
"""
from __future__ import annotations

import json
import os
import shutil
import subprocess
import sys
from pathlib import Path


HERE = Path(__file__).resolve().parent
ROOT = HERE.parent  # experiment root
HOME = ROOT / "scenario-home"


def run(cmd: list[str], *, check: bool = True, capture: bool = True) -> subprocess.CompletedProcess:
    """Invoke tasq or a subprocess and return the CompletedProcess.

    We use PYTHONPATH=./tasq-src so the tasq package is resolvable
    regardless of whether the agent set up a proper editable install.
    This keeps the harness decoupled from packaging quirks.
    """
    env = os.environ.copy()
    env["TASQ_HOME"] = str(HOME)
    env["PYTHONIOENCODING"] = "utf-8"
    env["PYTHONUTF8"] = "1"
    # Run via "python -m tasq" rather than relying on an entry-point
    # script — not every agent will have set up console_scripts.
    full_cmd = [sys.executable, "-X", "utf8", "-m", "tasq", *cmd]
    cp = subprocess.run(
        full_cmd,
        cwd=str(ROOT / "cwd"),
        env=env,
        capture_output=capture,
        # Pin subprocess output decoding to UTF-8 with a safety net.
        # Default on Windows is GBK (cp936 / mbcs) which chokes on
        # rich / click unicode glyphs; wrap it here so the grader
        # doesn't blow up on a perfectly-fine CLI.
        encoding="utf-8",
        errors="replace",
    )
    if check and cp.returncode != 0:
        print(f"[FAIL] `tasq {' '.join(cmd)}` exited {cp.returncode}")
        if cp.stdout:
            print("stdout:\n" + cp.stdout)
        if cp.stderr:
            print("stderr:\n" + cp.stderr)
        sys.exit(2)
    return cp


def ok(msg: str) -> None:
    print(f"[PASS] {msg}")


def fail(msg: str) -> None:
    print(f"[FAIL] {msg}")
    sys.exit(3)


def setup_home() -> None:
    if HOME.exists():
        shutil.rmtree(HOME)
    HOME.mkdir(parents=True)
    cwd = ROOT / "cwd"
    if cwd.exists():
        shutil.rmtree(cwd)
    cwd.mkdir(parents=True)


def step_projects() -> None:
    run(["project", "add", "web", "-d", "Frontend revamp"])
    run(["project", "add", "infra", "-d", "Kubernetes migration"])
    cp = run(["project", "list"])
    if "web" not in cp.stdout or "infra" not in cp.stdout:
        fail(f"project list missing entries; got:\n{cp.stdout}")
    ok("project add + list")


def step_add_tasks() -> None:
    seeds = [
        ["task", "add", "Build login page",  "-p", "web",   "-P", "high",   "-d", "2026-05-01", "-t", "ux"],
        ["task", "add", "Set up CI",         "-p", "infra", "-P", "urgent", "-d", "2026-04-20", "-t", "devops"],
        ["task", "add", "Write docs",        "-p", "web",   "-P", "medium", "-t", "docs"],
        ["task", "add", "Migrate DB",        "-p", "infra", "-P", "high",   "-t", "devops", "-t", "risk"],
        ["task", "add", "Polish README",     "-P", "low",   "-t", "docs"],
    ]
    for s in seeds:
        run(s)
    cp = run(["task", "list"])
    for title in ("Build login page", "Set up CI", "Write docs", "Migrate DB", "Polish README"):
        if title not in cp.stdout:
            fail(f"task list missing '{title}'; got:\n{cp.stdout}")
    ok("task add (5) + list all")


def step_filters() -> None:
    cp = run(["task", "list", "-p", "web"])
    for title in ("Build login page", "Write docs"):
        if title not in cp.stdout:
            fail(f"task list -p web missing '{title}'")
    if "Set up CI" in cp.stdout or "Migrate DB" in cp.stdout:
        fail(f"task list -p web leaked infra tasks:\n{cp.stdout}")
    ok("task list --project filter")

    cp = run(["task", "list", "-t", "docs"])
    if "Write docs" not in cp.stdout or "Polish README" not in cp.stdout:
        fail(f"task list -t docs missing entries:\n{cp.stdout}")
    if "Build login page" in cp.stdout:
        fail(f"tag filter leaked unrelated task:\n{cp.stdout}")
    ok("task list --tag filter")

    cp = run(["task", "list", "--overdue"])
    if "Set up CI" not in cp.stdout:
        fail(f"overdue should catch 'Set up CI' due 2026-04-20:\n{cp.stdout}")
    ok("task list --overdue")


def step_done_and_block() -> None:
    # Mark task 3 ("Write docs") done
    run(["task", "done", "3"])
    cp = run(["task", "show", "3"])
    if "done" not in cp.stdout.lower():
        fail(f"task 3 not marked done:\n{cp.stdout}")
    ok("task done")

    # Block task 4 ("Migrate DB") on task 2 ("Set up CI")
    run(["task", "block", "4", "--by", "2"])
    cp = run(["task", "deps", "4"])
    if "2" not in cp.stdout:
        fail(f"task 4 dep on 2 not recorded:\n{cp.stdout}")
    ok("task block + deps")


def step_reports() -> None:
    cp = run(["report", "stats"])
    if "5" not in cp.stdout:  # total = 5 created
        fail(f"report stats missing total count:\n{cp.stdout}")
    ok("report stats")

    cp = run(["report", "by-project"])
    if "web" not in cp.stdout or "infra" not in cp.stdout:
        fail(f"report by-project missing projects:\n{cp.stdout}")
    ok("report by-project")

    cp = run(["report", "overdue"])
    if "Set up CI" not in cp.stdout:
        fail(f"report overdue missing overdue task:\n{cp.stdout}")
    ok("report overdue")


def step_export_reimport() -> None:
    dump = ROOT / "cwd" / "snapshot.json"
    run(["export", "-o", str(dump), "--format", "json"])
    if not dump.exists() or dump.stat().st_size < 100:
        fail(f"export did not produce a useful json dump at {dump}")
    data = json.loads(dump.read_text(encoding="utf-8"))
    # Agent implementations vary — the dump might wrap data in {tasks:[...]}
    # or be a flat list. Normalise before counting.
    if isinstance(data, dict) and "tasks" in data:
        tasks = data["tasks"]
    else:
        tasks = data
    if not isinstance(tasks, list) or len(tasks) != 5:
        fail(f"expected 5 tasks in export, got {len(tasks) if hasattr(tasks,'__len__') else '?'}; shape={type(data).__name__}")
    ok(f"export json ({len(tasks)} tasks)")

    # Wipe DB by deleting the home
    shutil.rmtree(HOME)
    HOME.mkdir()

    # Re-import
    run(["import", str(dump), "--format", "json"])
    cp = run(["task", "list"])
    for title in ("Build login page", "Set up CI", "Write docs", "Migrate DB", "Polish README"):
        if title not in cp.stdout:
            fail(f"re-imported task list missing '{title}':\n{cp.stdout}")
    ok("import round-trip (DB wipe → re-import → all 5 tasks back)")


def main() -> None:
    setup_home()
    # --help must work as a basic sanity check
    cp = run(["--help"])
    if "task" not in cp.stdout.lower() or "report" not in cp.stdout.lower():
        fail(f"--help output missing core command groups:\n{cp.stdout}")
    ok("tasq --help lists required command groups")

    step_projects()
    step_add_tasks()
    step_filters()
    step_done_and_block()
    step_reports()
    step_export_reimport()

    print()
    print("========== SMOKE SCENARIO OK ==========")


if __name__ == "__main__":
    main()
