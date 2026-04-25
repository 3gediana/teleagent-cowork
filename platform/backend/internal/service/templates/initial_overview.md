# Project Overview

> Living map of this codebase. Every agent reads it at session start.
> Update it in the SAME `change_submit` that introduces structural changes
> (new files, moved/renamed/removed files, new exported symbols, modules).
> Section headings are stable — they are part of the agent protocol.
> Read order for a typical task: §1 → §6 → §7 → §9 → §4 → §5.

---

## 1. Why  *(2–5 lines, never empty)*

<!-- One sentence on WHAT this project is, one on WHO uses it / WHY it exists.
     If you cannot fill this in 5 lines, the project lacks focus. -->

<TODO: pending first agent pass>

---

## 2. Run  *(commands only, no prose)*

<!-- Exactly the commands a fresh clone needs. No explanations.
     If a flag isn't obvious, the flag IS the documentation. -->

```
# install
<TODO>

# run / dev
<TODO>

# test
<TODO>

# lint / format
<TODO>
```

---

## 3. Stack  *(one-liners)*

<!-- Tech stack at a glance. Anything non-obvious belongs in §6 Conventions. -->

- **Language / runtime**: <TODO>
- **Main framework**: <TODO>
- **Storage**: <TODO>
- **Other**: <TODO>

---

## 4. Map  *(top 2 levels max, one line each)*

<!-- Directory tree. Skip vendored / generated / build dirs. Two levels is the cap. -->

```
src/
  <subdir>/   # <one-line purpose>
tests/        # test suite
docs/         # human-facing docs
```

---

## 5. Key Files  *(what FUTURE agents will open, not what you just touched)*

<!-- Pick by frequency-of-need, not recency-of-edit. -->

| File | What it does | When you'll touch it |
|---|---|---|
| `<path>` | <one-line purpose> | <typical task> |

---

## 6. Conventions  *(append-only)*

<!-- Project-specific rules a reviewer would reject for breaking.
     If a reviewer ever said "we don't do that here", record it. -->

- <TODO: e.g. "all handlers return {success, data, error}">

---

## 7. Danger Zones  *(append-only)*

<!-- Files / modules where a casual change has caused outages or merge hell.
     New agents need to see this BEFORE acquiring filelocks. -->

- <TODO: e.g. "src/auth/middleware — order matters, change breaks all routes">

---

## 8. Active Focus  *(5 lines max, refresh on milestone switch)*

<!-- What the team is currently doing. -->

- **Milestone**: <TODO>
- **Now**: <TODO>
- **Next**: <TODO>
- **Out of scope**: <TODO>

---

## 9. Pitfalls  *(append-only)*

<!-- Anti-knowledge: things that LOOK reasonable but have been tried and failed.
     One line per pitfall. Append, don't rewrite — history is the value. -->

- <TODO: e.g. "Calling X inside Y deadlocks under load">

---

## 10. Recent Structural Changes  *(append at top, version-prefixed)*

<!-- Format: `- vX.Y: <one line, structural only>`. Bug fixes do NOT belong here. -->

- _(no entries yet)_
