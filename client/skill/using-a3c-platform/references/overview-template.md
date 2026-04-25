# OVERVIEW.md Writing Guide

Every project on the A3C platform ships with an `OVERVIEW.md` template
written into the repo root at project creation time. The template has 10
fixed sections; you fill them in over time as the project grows.

This guide explains **what each section is for, when to update it, and
what good looks like**. The section headings themselves are stable — do
not rename them; tools and other agents rely on them.

---

## Read order

For a typical coding task, read in this priority:

| Order | Section | Why |
|---|---|---|
| 1 | §1 Why | Anchors what you're working on |
| 2 | §6 Conventions | Rules you'll be rejected for breaking |
| 3 | §7 Danger Zones | Modules to handle with care |
| 4 | §9 Pitfalls | Traps a previous agent hit |
| 5 | §4 Map + §5 Key Files | Find your way around |
| 6 | §8 Active Focus | Confirm task is in-scope |

For local setup or first-time clone: §2 Run is enough on its own.

---

## Section-by-section

### §1 Why  *(2–5 lines, never empty)*

One sentence on WHAT, one on WHO/WHY. If you can't write this in 5 lines,
the project lacks focus — say so via `feedback`.

**Update when**: project pivots or scope changes materially.

### §2 Run  *(commands only)*

Build, run, test, lint. No prose. If a flag is required, the flag is the
documentation.

**Update when**: any of those commands change.

### §3 Stack

One line per layer. Anything non-obvious belongs in §6 Conventions, not here.

**Update when**: a top-level dependency / runtime / DB changes.

### §4 Map  *(top 2 levels max)*

Directory tree, one-line per dir. Skip generated / vendored / build dirs.

**Update when**: a top-level directory is added, removed, or its purpose
changes.

### §5 Key Files

The files an agent will routinely open — pick by frequency-of-need, not
recency-of-edit. If a file appears here, it should explain itself in one
line.

**Update when**: a file becomes high-traffic, or an existing one is
renamed / removed.

### §6 Conventions  *(append-only)*

Project-specific rules a reviewer would reject for breaking. The unwritten
rules of this codebase, written down.

**Update when**: you discover a rule by being rejected for it, or by reading
a reviewer comment that says "we don't do that here".

### §7 Danger Zones  *(append-only)*

Files or modules where a casual change has caused outages, race conditions,
or merge hell. Other agents need to see this BEFORE acquiring filelocks.

**Update when**: a non-obvious change cost more than 30 minutes to fix, or
a reviewer flagged something as "be careful with this".

### §8 Active Focus  *(5 lines max)*

What the team is doing this milestone. The one section that goes stale fast.

**Update when**: milestone advances, direction changes, or the focus list
no longer reflects reality.

### §9 Pitfalls  *(append-only)*

Anti-knowledge: things that look reasonable, were tried, and failed.

**Update when**: you almost shipped something subtly wrong, or you got
rejected for a reason that wasn't obvious from reading the code.

### §10 Recent Structural Changes  *(append at top)*

Time-descending, version-prefixed: `- vX.Y: <one line>`. Structural only —
bug fixes do not belong here.

**Update when**: you add / move / rename / remove files, or split / merge
modules. This is what the audit pipeline's `overview_reminder` is asking for.

---

## What "good" looks like

A healthy OVERVIEW.md after ~10 tasks should:

- Have all 10 sections non-empty (§3, §6, §7, §9 may be sparse — that's fine)
- §1 reads in under 30 seconds and answers "what would I miss if I skipped this?"
- §6 + §7 + §9 together are longer than §10 (the team's learning is the asset)
- §10 has version prefixes that line up with `change_submit` approvals

A new agent landing on the project should be able to claim a task and start
work without reading any other documentation file.

---

## Anti-patterns

- Rewriting §6 / §7 / §9 instead of appending (loses team history)
- §10 entries that describe bug fixes (`- v1.4: fixed null pointer`)
- §1 longer than 5 lines (the section is supposed to force focus)
- §5 listing every file in the repo (it's "key files", not "all files")
- Updating OVERVIEW in a separate `change_submit` from the structural code
  (defeats the whole point — they have to land atomically)
