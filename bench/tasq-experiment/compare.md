# Solo vs Team-3: agent pool benchmark

Single-agent vs three-parallel-agents building the **same spec**
(`SPEC.md`) with the **same model** (MiniMax-M2.7) through the
**same pool manager**, graded by the **same harness**
(`bench.ps1` + `scenarios/smoke.py`).

---

## Headline

|  | **Solo** | **Team-3** |
|---|---|---|
| Overall composite score | **78.0** | 73.4 |
| Scenario pass | **11/12** | 0/12 |
| Pytest pass | 144/146 (98.6%) | 142/156 (91.0%) |
| End-to-end usable? | **Yes** | No (wiring broken) |

**Solo won** on overall score, scenario, and end-to-end usability.
Team agents wrote *individually* better modules (cleaner lint,
denser tests, comparable coverage) but one small cross-agent
coordination failure broke integration.

---

## Detailed metrics

| Metric | Solo (1 agent) | Team-3 (3 parallel) | Winner |
|---|---|---|---|
| Source LOC (`tasq/`) | 1,920 | 1,455 | Solo more surface |
| Test LOC (`tests/`) | 829 | 1,202 | **Team +45%** |
| pytest passed | 144 / 146 | 142 / 156 | Solo % |
| pytest pass % | **98.6%** | 91.0% | Solo |
| Coverage % | 67% | 68% | Tie |
| ruff issues | 38 | **21** | **Team −45%** |
| mypy errors | **3** | 34 | **Solo −91%** |
| Scenario steps passed | **11 / 12** | 0 / 12 | Solo |
| Scenario exit code | 2 (one sub-step) | 3 (first sanity) | Solo |
| Wall-clock elapsed | ~9 min | ~13 min parallel | Solo-ish |
| **Composite score** | **78.0** | **73.4** | **Solo +4.6** |

Composite = `coverage% + pytest_pass% + 20*(scenario_ok) − 0.5·ruff − 0.2·mypy − loc_pen`.

---

## The decisive moment: the `__init__.py` coordination bug

The team-3 arm's entire scenario run died on the FIRST sanity step
(`tasq --help` must list core command groups). Root cause:

- `cli-core` agent owned `tasq/cli/__init__.py` and wrote:
  ```python
  cli.add_command(tasks)
  cli.add_command(projects)
  cli.add_command(tags)
  ```
  (only the 3 commands it authored)

- `cli-advanced` agent owned `reports.py`, `io.py`, `shell.py` but
  had no hook to register them in the shared CLI group.

- Nothing in the SPEC said who owns the composition. Each agent
  optimised its own responsibility and the seams leaked.

Solo's version of the same file:
```python
cli.add_command(task,       name="task")
cli.add_command(project,    name="project")
cli.add_command(tag,        name="tag")
cli.add_command(report,     name="report")
cli.add_command(import_cmd, name="import")
cli.add_command(export,     name="export")
cli.add_command(config,     name="config")
cli.add_command(shell,      name="shell")
```
Solo naturally wired all 8 subgroups because it was writing all 8.

## Other integration bugs team hit

1. `Project.__init__() got an unexpected keyword argument 'archived'`
   — `cli-core/projects.py` assumed foundation's `Project` dataclass
   had an `archived` field; foundation omitted it.
2. `TypeError: object of type 'int' has no len()` on multiple cli
   tests — cli-core's tests called `len()` on what `db.add_task`
   returns, assuming list; foundation returned `int` (the new id).

14 test failures in team stem from these contract mismatches. Solo's
2 test failures are unrelated cosmetic issues (a None comparison
and a `__repr__` assertion against a Rich object).

## Where team DID genuinely win

Despite losing on integration:

- **+45% test density** — 1,202 test LOC vs 829. Smaller scope +
  fresher context budget makes each team agent more willing to
  write exhaustive test cases. Team's test files are visibly more
  thorough, even though some fail due to contract mismatch.
- **−45% lint noise** — 21 ruff issues vs 38. Same "smaller scope,
  more care" effect.
- **Wall-clock parallelism paid off for the module-writing phase**
  — all 3 finished writing their files within ~7 minutes of
  spawning, versus solo taking nearly 9 minutes sequentially (and
  still missing tests).

## What about cost?

Rough token accounting (message counts from the pool metrics):

| | Total assistant messages | Approx cost multiplier |
|---|---|---|
| Solo | ~24 (1 session) | 1.0× |
| Team | 32 + 65 + 42 = **139 across 3 sessions** | ~5.8× |

Team burned ~5.8× more tokens AND delivered a non-working system.
That's the expensive lesson: naive parallelism is NOT free — it's
more expensive unless you're also solving a real parallel bottleneck.

For this 2–3k LOC project, a single MiniMax-M2.7 session has
plenty of context for the whole design. Parallel agents were
solving a problem that didn't exist, while creating a new one
(coordination).

---

## When team WOULD likely win

This is a scale-dependent result, not a universal verdict. The
team-3 arm's disadvantage comes from **three independent problems**:

1. Context not yet a bottleneck at this scale. Solo fits it all.
2. No defined integration ownership. The spec treated module
   lists as disjoint but some files (like `__init__.py`) are
   intrinsically shared.
3. No integration phase. After team agents finish their modules,
   there's no merge/test/fix cycle.

Projects where team-3 should clearly beat solo:

- **>15k LOC** — solo can't hold the whole design in working context
- **Well-encapsulated module boundaries** (microservices,
  plugin-based systems) with minimal shared glue
- **Integration phase built into the protocol** — e.g. a
  "dependency registrar" file that every agent appends to, or
  an explicit merge/test loop before grading

---

## How you can replay this

All artefacts are on disk under `bench/tasq-experiment/`:

```powershell
cd bench/tasq-experiment

# 1. Re-grade the saved outputs (no backend needed, no LLM calls)
.\bench.ps1 -OutputDir .\solo-output -Label solo
.\bench.ps1 -OutputDir .\team-output -Label team

# 2. Replay just the end-to-end scenario
cd solo-output
python ..\scenarios\smoke.py

# 3. Re-run the WHOLE experiment from scratch (needs backend up,
#    MiniMax plan available, will take ~20-30 minutes, ~6x tokens
#    of solo alone):
.\run-experiment.ps1 -WaitSeconds 1800
```

Files:
- `SPEC.md` — the contract both arms read
- `scenarios/smoke.py` — the end-to-end verification
- `bench.ps1` — the grading harness
- `run-experiment.ps1` — orchestrator that drives the pool manager
- `collect-and-grade.ps1` — re-collect + grade helper
- `solo-output/` — the single agent's deliverable + bench artefacts
- `team-output/` — the merged team deliverable + bench artefacts
- `experiment.log` / `grade.log` — raw run logs

Each output directory contains:
- `tasq/` — the generated source package
- `tests/` — the generated pytest suite
- `bench-result.json` — machine-readable numbers
- `bench-pytest.log` / `bench-ruff.log` / `bench-mypy.log` / `bench-scenario.log`

---

## Takeaways for the platform

1. **Multi-agent is not free.** For sub-10k LOC projects, single
   agent is often faster, cheaper, and more correct. Parallelism
   needs a real bottleneck to pay off.

2. **Shared integration files need explicit ownership.** If a
   file has to know about everyone's work (`cli/__init__.py`,
   `__main__.py`, a dependency container), that file belongs
   either to one agent who reads others' output, or to a
   deterministic merge tool.

3. **Contracts need teeth.** SPEC.md said "Task dataclass MUST
   have these fields" — but "MUST" in a LLM prompt is advisory,
   not enforceable. A real team workflow would either:
   - Let foundation write the dataclass first, and make the other
     agents run `from tasq.models import Task` before writing
     code, or
   - Have a type-check gate *between* agent outputs and downstream
     consumers.

4. **The scoring matters more than the headline.** Team's 45%
   test density is a real win — on a project where integration
   had been solved, that would translate to better long-term
   quality. The comparison isn't "solo is better"; it's "solo is
   better on *this* scale, *this* decomposition, and *this*
   evaluation rubric."

Generated at 2026-04-24 08:25 local.
