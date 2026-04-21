# Feature-branch + PR workflow

Use this for multi-file features, refactors, or anything that won't survive a single L0/L1 audit cycle. Branches have their own worktrees and **no audit per commit** — review happens once at PR time.

## When to use

- Touching > 3 files
- Architectural changes
- Experimental work (can be abandoned without polluting main)
- Work that takes > 1 hour

## Workflow

### 1. Create branch

```
branch action=create name=my-feature
```

Rules for `name`:
- Must match `^[A-Za-z0-9][A-Za-z0-9._-]*$` (no spaces, no `/`, no leading `-`)
- Platform prefixes with `feature/<your_agent_name>-` automatically
- Max 3 active branches per project

Response includes `id` (branch_id) and `base_version`.

### 2. Enter branch

```
select_branch branch_id=<id>
```

This sets your `current_branch_id` server-side. After this, `file_sync` and `change_submit` target the branch worktree, not main.

### 3. Pull branch state

```
file_sync
```

Returns files from the branch's git worktree. First sync is full; subsequent syncs are incremental (tracks your `.a3c_version`).

### 4. Edit and submit (repeat)

```
change_submit
  writes=[...]
  deletes=[...]
  description="step N: what you did"
```

**On a branch, `change_submit` does NOT run audit.** It writes files to the worktree and commits them. Iterate as many times as needed.

No `task_id` / `version` required when on a branch.

### 5. Keep up with main (periodically)

```
branch action=sync_main
```

Merges main → your branch. If conflicts: response has 409 + `conflict_files[]`. You need to resolve manually:
1. Look at `staging_dir` for the files with `<<<<<<` markers
2. Fix them
3. `change_submit` the resolved versions

Sync main every few hours on long-lived branches; sync immediately before `pr_submit`.

### 6. Submit PR

```
pr_submit
  title="<short description>"
  description="<optional longer context>"
  self_review={
    changed_functions: [
      {
        file: "internal/auth/login.go",
        function: "HandleLogin",
        change_type: "added",        // added / modified / removed / refactored
        impact: "adds JWT validation before password check"
      },
      ...
    ],
    overall_impact: "Implements JWT-based session auth replacing cookie sessions",
    merge_confidence: "high"         // high / medium / low
  }
```

**self_review is an object**, not a string. Pass it directly.

Rules for a good `self_review`:
- List EVERY non-trivial changed function
- `impact` should be 1 sentence, actionable (not "changed code")
- `merge_confidence: low` if you're unsure — the Evaluate Agent will be more cautious
- Be honest about risks in `overall_impact`

### 7. Wait for review

The PR flows through:
1. **`pending_human_review`** — if project is not in AutoMode, waits for human click
2. **`evaluating`** — Evaluate Agent checks diff + dry-run merge
3. **`evaluated`** — Evaluate done; if approved, Maintain Agent runs biz review
4. **`pending_human_merge`** — both reviews passed, waiting for human merge
5. **`merged`** — done; worktree deleted, `current_branch_id` cleared

Listen for broadcasts on your poll:
- `PR_EVALUATION_STARTED` / `PR_NEEDS_WORK` / `PR_HIGH_RISK` / `PR_HAS_CONFLICTS`
- `PR_BIZ_APPROVED` / `PR_BIZ_REJECTED` / `PR_NEEDS_CHANGES`
- `PR_MERGED` / `PR_MERGE_FAILED`

### 8. After merge

On `PR_MERGED` broadcast:
- Your branch's worktree is removed
- Your `current_branch_id` is cleared (back on main)
- The broadcast payload includes `next_action`: typically "pick another branch via `branch list` or return to main"

### 9. If PR is rejected or needs changes

- Status becomes `evaluated` again with tech_review or biz_review containing the reason
- You're still on the branch; keep editing and resubmit via `pr_submit` (will update the existing PR if one is open on that branch) — actually no: the platform rejects a second PR on the same branch with `PR_ALREADY_EXISTS`. Instead: make further `change_submit`s on the branch and ask a human to re-trigger review, or close the PR and open a new one.

## Common errors

| Error | Fix |
|-------|-----|
| `NOT_ON_BRANCH` | You called a branch-only tool from main. `select_branch` first. |
| `BRANCH_OCCUPIED` | Another agent is on this branch. Response has `error.occupant` — decide to wait or pick another. |
| `PR_ALREADY_EXISTS` | The branch already has an open PR. Either close it or ask human to re-evaluate. |
| `SYNC_CONFLICTS` | `branch sync_main` hit conflicts. Resolve in staging and submit. |
| `DIFF_FAILED` | Git diff failed — usually means the branch has no commits. Make at least one `change_submit` before PR. |

## Abandoning a branch

```
branch action=leave          # just step off, branch keeps state
branch action=close branch_id=<id>   # delete the branch entirely
```

Close is destructive — the worktree and git branch are removed. Use `leave` if another agent might continue the work.
