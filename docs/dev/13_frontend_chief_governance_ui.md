# 13 · Frontend Chief Governance UI — Design Spec

**Status**: Design draft (awaiting approval to implement)
**Author**: Cascade
**Targets**: F1 (PR decision badge) · F2 (Chief decision queue) · F3 (AutoMode header toggle)
**Aesthetic**: Wooden cabin scrapbook — parchment, wood grain, leather, Permanent Marker / Kalam / Special Elite fonts, warm brown palette `#5d4037 / #8b4513 / #f4ece1 / #3e2723`.

---

## 0 · Why this doc exists

Last round's backend work ("C' Chief governance refactor") added three things that the UI does not yet expose. Humans can't see them, so the self-evolution story breaks:

1. **`recommended_action`** on `evaluate_output` (new field). Evaluate is now the primary technical decision-maker; its verdict tells Chief whether a PR can auto-advance, needs escalation, or should be sent back. Today it's buried as raw JSON in the PR card.
2. **`delegate_to_maintain`** (new Chief tool). Chief now forwards task/milestone edits to Maintain instead of mutating them directly. Delegations leave a paper trail in the DB but nothing in the UI.
3. **AutoMode** is a project-level flag (`project.AutoMode`) that Chief reads every session. UI has no prominent toggle; you can only flip it via the API.

Goal of this spec: **surface all three without breaking the cabin aesthetic**. No new routes, no new color system. Parchment, marker, wax seals.

---

## 1 · Visual language (stick to this)

| Token | Value | Where it lives |
|---|---|---|
| Background parchment | `.parchment` | Cards, panels |
| Background wood | `.wood-board` | Kanban only |
| Primary dark | `#5d4037` (dark brown) | Headings, active tab pill |
| Primary accent | `#8b4513` (saddle brown) | Borders, metadata |
| Paper cream | `#f4ece1` | Buttons / chips |
| Ink | `#3e2723` (espresso) | Body text |
| Marker font | `font-marker` (Permanent Marker) | Titles, tab labels |
| Hand font | `font-hand` (Kalam) | Descriptions, chat bubbles |
| Type font | `font-type` (Special Elite) | Nav labels |
| Mono | default mono | IDs, timestamps, JSON |

**Signals (reused across the three features)**:

- **Wax seal** (✅ auto): emerald glow, `ring-2 ring-emerald-400/40` + `shadow-[0_0_20px_rgba(16,185,129,0.35)]`. Says "the platform sealed the deal".
- **Hand stamp** (🖐️ escalate): amber parchment slip, `-rotate-2`, `border-2 border-amber-400/60`. Says "human, look here".
- **Sticky note reject** (↩️ request changes): rose sticky, `rotate-1`, `shadow-[5px_5px_15px_rgba(0,0,0,0.25)]`. Says "come back with changes".
- **Leather switch** (AutoMode): `#5d4037` base when on, desaturated `#8b4513/40` when off, with a brass rivet dot as the "knob".

None of these are new CSS — everything composes from the existing Tailwind utilities + `.parchment` / `.btn-cabin`. No new classes needed in `index.css`.

---

## 2 · F1 · PR decision badge on `PRPage.tsx`

### 2.1 Current state

`@D:\claude-code\coai2\frontend\src\pages\PRPage.tsx:159-173` renders `tech_review` / `biz_review` as raw `<pre>` JSON. Functional but nobody wants to read it.

### 2.2 New component: `PREvaluationCard`

Replaces the raw JSON dump. Structured layout that surfaces the three signals the human cares about — **verdict, merge cost, recommended action** — then hides the raw JSON behind a "Show raw" toggle.

```
┌─ Tech Evaluation ──────────────────────────────────────────────────┐
│                                                                    │
│  [✅ APPROVED]   cost: LOW   🚀 auto_advance                        │
│  ─────────────────────────────────────────────────────────────     │
│  Reason (Kalam, italic):                                           │
│  "Table-driven tests cover every public API method. No security    │
│   flags. Dry-run merge clean."                                     │
│                                                                    │
│  Policy matched: 'small-docs-auto' (priority 20)                   │
│  [AutoMode ON → will auto-approve in 12s]     [Take over] [Raw ▾]  │
└────────────────────────────────────────────────────────────────────┘
```

### 2.3 Badge specs

Three recommended-action badges, rendered as little leather tags pinned to the card:

| `recommended_action` | Label | Icon | Colors (Tailwind) | Rotation |
|---|---|---|---|---|
| `auto_advance` | AUTO ADVANCE | 🚀 | `bg-emerald-600 text-emerald-50 border-emerald-700` + `ring-2 ring-emerald-400/40` + glow | `-rotate-2` |
| `escalate_to_human` | ESCALATE | 🖐️ | `bg-amber-100 text-amber-800 border-amber-400` | `rotate-1` |
| `request_changes` | SEND BACK | ↩️ | `bg-rose-100 text-rose-700 border-rose-300` | `-rotate-1` |

Result badge (left of the row): `APPROVED / NEEDS WORK / CONFLICTS / HIGH RISK`, cabin palette, same shape.

Cost pill (middle): `LOW / MEDIUM / HIGH` in mono caps on a `#8b4513/10` chip.

### 2.4 Data source (no backend change)

`pr.tech_review` is already the full `evaluate_output` args as JSON. Parse once:

```ts
type TechReview = {
  result: 'approved' | 'needs_work' | 'conflicts' | 'high_risk'
  merge_cost_rating: 'low' | 'medium' | 'high'
  recommended_action: 'auto_advance' | 'escalate_to_human' | 'request_changes'
  reason?: string
  conflict_files?: string[]
  quality_patterns?: string
  common_mistakes?: string
}
```

Missing `recommended_action` (old data before the schema change) → fall back to rendering the old raw JSON view so we don't crash historical PRs.

### 2.5 Policy-match surfacing

New helper on the backend (or computed client-side from `policyApi.list` + PR metadata): "the policy Chief will apply if AutoMode kicks in".

**For this iteration**: client-side only. Fetch active policies on page load, match client-side using the same fields documented in `create_policy` schema (`scope`, `file_count_gt/lt`, `file_pattern`, `merge_cost_in`, `submitter`). If no match → show "No matching policy". Backend-driven match (single source of truth) is a Phase 2 improvement.

### 2.6 Acceptance criteria

- Old PRs (no `recommended_action` in tech_review) render unchanged (fallback to raw JSON) — no regressions.
- New PRs show the three-badge header + reason.
- Raw JSON is behind a `Show raw ▾` toggle, collapsed by default.
- Layout doesn't grow the card significantly — target ≤ 180px tall for approved PRs.

---

## 3 · F2 · Chief Decision Queue

Two views of the same data:

- **3A**: Full queue as a new tab on `ChiefPage.tsx` (alongside `chat / policies / sessions / experience / skills`).
- **3B**: Compact "awaiting Chief" preview card on `OverviewPage.tsx`'s left column (below `Locks`).

### 3A · ChiefPage · `queue` tab

New tab between `chat` and `policies`:

```
┌─ CHAT | QUEUE ● 3 | POLICIES | SESSIONS | EXP | SKILLS ─────────────┐
│                                                                     │
│  📋 Pending Decisions   [AutoMode OFF]  [Enable AutoMode »]         │
│  ─────────────────────────────────────────────────────────────────  │
│                                                                     │
│  ┌─ PR #abc12345 · "Add retry on 429 responses" ──────────────┐    │
│  │ Filed 4m ago by @alice-agent  ·  3 files changed           │    │
│  │                                                             │    │
│  │  [✅ APPROVED]   cost: LOW   🚀 auto_advance                 │    │
│  │  "Retries are idempotent, tests cover the backoff curve."  │    │
│  │                                                             │    │
│  │  📜 Policy: small-backend-auto (P20) → auto_approve         │    │
│  │                                                             │    │
│  │  [Approve Review ✓]  [Reject ↩]  [View details →]          │    │
│  └────────────────────────────────────────────────────────────┘    │
│                                                                     │
│  ┌─ PR #def67890 · "Migrate auth to OAuth2" ───────────────────┐   │
│  │ Filed 12m ago by @bob-agent  ·  24 files changed            │   │
│  │                                                              │   │
│  │  [⚠ HIGH RISK]   cost: HIGH   🖐️ escalate_to_human           │   │
│  │  "Touches session refresh concurrency — human review req'd." │   │
│  │                                                              │   │
│  │  📜 Policy: db-schema-human-only (P90) → require_human      │   │
│  │                                                              │   │
│  │  [Approve anyway ✓]  [Reject ↩]  [View details →]           │   │
│  └────────────────────────────────────────────────────────────┘   │
│                                                                     │
└─────────────────────────────────────────────────────────────────────┘
```

**Empty state** (no pending decisions):

```
┌────────────────────────────────────────┐
│   ☕                                   │
│   All quiet.                           │
│   Chief's got nothing on the desk.     │
└────────────────────────────────────────┘
```

Uses Kalam for the flavor text, `#8b4513/40` color.

### 3B · OverviewPage compact card

Slot below `LocksCard` in the left column. ≤ 200px tall, max 3 items. Click-through goes to `/chief?tab=queue`.

```
┌─ parchment ───────────────────┐
│  🤖 Chief's Desk · 3 pending  │
│  ─────────────────────────    │
│  🚀 auto — "Add retry..."     │
│  🖐️ human — "Migrate auth..." │
│  ↩ changes — "Fix typo..."   │
│                               │
│  [Open queue →]               │
└───────────────────────────────┘
```

Each row: one-line PR title (truncated), leading icon = recommended_action, Kalam font, `#5d4037` text. Whole card has a pulsing emerald dot (`animate-pulse shadow-[0_0_8px_rgba(16,185,129,0.5)]`) when `count > 0`, gray when idle.

### 3C · Data source

```
pending = prApi.list()
  .filter(pr => pr.status === 'pending_human_review' || pr.status === 'pending_human_merge')

for each pr:
  techReview  = pr.tech_review && JSON.parse(pr.tech_review)  // has recommended_action
  bizReview   = pr.biz_review && JSON.parse(pr.biz_review)
  matchedPolicy = matchPolicies(pr, activePolicies)  // client-side
```

No new API endpoint strictly required for F2 if we rely on client-side policy matching. If backend-truth is preferred later → add `GET /chief/queue?project_id=X` returning `[{pr, evaluation, matched_policy, suggested_action}]`.

### 3D · Action buttons

- **Approve Review** → `prApi.approveReview(pr.id)` (existing endpoint).
- **Approve Merge** → `prApi.approveMerge(pr.id)` (existing).
- **Reject** → `prApi.reject(pr.id, reason)` — opens a small prompt for the reason.
- **View details** → navigate to `/prs?focus=${pr.id}` — PRPage should scroll to it.

All buttons use `.btn-cabin` styling. Approve = cabin palette (`bg-[#5d4037]`). Reject = `bg-rose-500`. Hover: `-translate-y-0.5 shadow-lg`.

### 3E · Acceptance criteria

- Queue tab shows all `pending_human_*` PRs with evaluation summary.
- Client-side policy match surfaces the matching policy name + priority.
- Buttons work against existing `/pr/approve_review`, `/pr/approve_merge`, `/pr/reject` endpoints (no backend change needed).
- Empty state is cabin-flavored, not generic.
- Overview compact card matches count + top 3 items, clickable.

---

## 4 · F3 · AutoMode header toggle

### 4.1 Placement

Modify `@D:\claude-code\coai2\frontend\src\components\Layout.tsx:11-22`, specifically the header's right cluster (currently shows `N Agents Online`). Add an `AutoModeSwitch` component **left of** the agent count:

```
┌─ /  ProjectName ────────────────────[Switch] [● N Agents Online]  ┐
```

### 4.2 Component: `AutoModeSwitch`

Visual: a leather-textured toggle, cabin style, ~160px wide.

**Off state:**
```
┌─────────────────────────────┐
│  AutoMode  [      ●  ]      │   (knob right? no, left when off)
│  Manual — Chief waits       │
└─────────────────────────────┘
```

**On state:**
```
┌─────────────────────────────┐
│  AutoMode  [  ●        ]    │
│  Active — Chief auto-decides│    (emerald glow, wax seal)
└─────────────────────────────┘
```

Wait — standard UX says knob is on the right when ON. Corrected:

- **OFF**: knob on left, track `bg-[#8b4513]/20`, label `Manual`, text `#8b4513/60`.
- **ON**: knob slides right, track `bg-emerald-600`, label `Active`, text `#5d4037`, `ring-2 ring-emerald-400/30` + glow.

Animation: 200ms ease-out translate on the knob, color transition on track.

### 4.3 Interaction

1. Click anywhere on switch → opens a **confirmation modal** (uses existing `Modal` component at `@D:\claude-code\coai2\frontend\src\components\Modal.tsx`).

2. Modal body (switching ON):

   ```
   Enable AutoMode?
   ─────────────────────────────
   Chief will auto-decide based on:
     · Evaluate's recommended_action
     · Your active policies (3)
   
   Chief will NOT:
     · Edit tasks or milestones (delegates to Maintain)
     · Merge PRs flagged "high_risk" (always escalates)
   
   Currently pending: 2 PRs
   Chief will process these within ~30s after enabling.
   
   [Enable AutoMode]  [Cancel]
   ```

3. Modal body (switching OFF):

   ```
   Disable AutoMode?
   ─────────────────────────────
   Chief will stop auto-deciding. Any in-flight
   Chief sessions finish, but new PRs wait for you.
   
   Pending will be 2 PRs when you disable.
   
   [Disable]  [Cancel]
   ```

4. Confirm → `projectApi.setAutoMode(projectId, newValue)` → refresh store.

### 4.4 Data source

`DashboardState.auto_mode` — **this field doesn't exist in `types.ts` today**. Needs backend work:

**Backend addition** (small): `@D:\claude-code\coai2\platform\backend\internal\handler\dashboard.go` — include `auto_mode: project.AutoMode` in the state response. Already queried in `chief.go` for session context, just not returned via dashboard endpoint.

**Frontend addition**: extend `DashboardState` in `types.ts` with `auto_mode?: boolean`.

### 4.5 Acceptance criteria

- Switch reflects server state on page load.
- Toggle calls the API, optimistic UI updates, rolls back on failure.
- Modal explains both what Chief will and won't do (matches the new governance rules).
- Switch shows pending-count hint in the confirmation.
- Accessible: `role="switch" aria-checked={value}`.

---

## 5 · File-level change plan

| File | Change | Est. lines |
|---|---|---|
| `frontend/src/api/types.ts` | Add `auto_mode?: boolean` to `DashboardState` | +1 |
| `frontend/src/components/Layout.tsx` | Import + place `AutoModeSwitch` in header | +5 |
| `frontend/src/components/AutoModeSwitch.tsx` | **NEW** — toggle + modal | +120 |
| `frontend/src/components/PREvaluationCard.tsx` | **NEW** — structured tech review renderer | +140 |
| `frontend/src/components/ChiefQueuePanel.tsx` | **NEW** — full queue tab | +180 |
| `frontend/src/components/ChiefQueueCompact.tsx` | **NEW** — compact card for Overview | +60 |
| `frontend/src/pages/PRPage.tsx` | Replace raw tech_review/biz_review blocks with `PREvaluationCard` | ~-30 / +10 |
| `frontend/src/pages/ChiefPage.tsx` | Add `queue` tab entry + mount `ChiefQueuePanel` | +15 |
| `frontend/src/pages/OverviewPage.tsx` | Add `ChiefQueueCompact` in left column | +3 |
| `frontend/src/hooks/usePolicyMatcher.ts` | **NEW** — client-side policy matching helper | +70 |
| `platform/backend/internal/handler/dashboard.go` | Expose `auto_mode` in state response | +1 |

Total: ~600 lines new / ~30 lines removed. Spans ~11 files, one of which is backend (a one-liner).

---

## 6 · Implementation order

1. **Backend one-liner** — expose `auto_mode` in dashboard state (5 min).
2. **`types.ts`** — extend DTO (1 min).
3. **`PREvaluationCard`** — new structured card. Unit tests with sample fixtures (approved / needs_work / conflicts / high_risk, all four shapes).
4. **Wire `PREvaluationCard` into `PRPage.tsx`** (replace raw JSON blocks; keep `Show raw ▾` as fallback).
5. **`usePolicyMatcher.ts`** — policy matching logic + tests.
6. **`ChiefQueuePanel`** — full queue tab.
7. **Add tab to `ChiefPage.tsx`**.
8. **`ChiefQueueCompact`** + slot into `OverviewPage`.
9. **`AutoModeSwitch`** — toggle + modal + state handling.
10. **Wire into `Layout.tsx`**.
11. Manual browser QA pass through: toggle AutoMode, submit a PR, watch queue populate, approve from queue, see PRPage update.

---

## 7 · Risks + caveats

| Risk | Mitigation |
|---|---|
| Historical PRs have no `recommended_action` → UI crashes | Fallback path renders old raw-JSON view if field absent. Covered by tests. |
| Client-side policy match diverges from backend match | Acknowledged Phase 2 problem. Document in code comments. Backend-truth endpoint later. |
| AutoMode confirm modal clutters the header flow | The toggle is click-to-open-modal, not instant — one extra step, but the modal's explanation is the safety net. |
| Cabin aesthetic inconsistency if I import Heroicons etc. | Use only Unicode emoji + existing Tailwind. No new icon libs. |
| `policyApi.list` being called repeatedly from compact card | Cache via the existing dashboard hook (10s polling). |

---

## 8 · Out of scope (save for later)

- F4 Session lineage (Chief→Maintain chain visualization).
- F5 Agent topology graph.
- F6 Proactive suggestions port from Claude Code.
- F7 Cost tracker.
- Backend-truth policy matching endpoint.
- Websocket push for queue updates (we rely on the existing 10s polling).

---

## 9 · Approval gate

Before I write code, confirm:

1. **F1 + F2 + F3 scope is right** (not too much, not too little).
2. **Cabin aesthetic is the right language** (vs e.g. adding some modernist charts — I'm not).
3. **Backend one-liner (adding `auto_mode` to dashboard state) is OK** (only backend change needed).
4. **Client-side policy matching is acceptable for v1** (vs. asking me to build a backend endpoint).

If yes to all four, I execute the 11-step plan in the next turn.
