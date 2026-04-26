# Knowledge Refinery: Multi-Pass Distillation for Self-Evolving Agents

> *"One trace, seven lenses."*

## Problem

Most multi-agent platforms treat agent logs as write-once telemetry. The A3C
platform already collects rich structured data — `ToolCallTrace`,
`AgentSession`, `Experience`, `Change` audit results — but the existing
Analyze Agent does a **single pass**: it reads raw Experiences and outputs
SkillCandidates + Policy suggestions in one LLM call.

This is wasteful. The same raw trace data contains orthogonal signals that
different "lenses" can extract:

| Lens | Question it answers |
|------|---------------------|
| Pattern mining | What tool sequences consistently succeed? |
| Anti-pattern detection | What sequences are over-represented in failures? |
| Tool recipes | What ordered tool-call templates work for a task type? |
| Model routing | Which model is cost-effective for which task tag? |
| Failure taxonomy | Can we classify failure modes into a reusable tree? |
| Temporal rules | After event X, what tends to happen within N minutes? |
| Meta-reflection | Which lenses are producing the most useful artifacts? |

A single LLM call cannot simultaneously optimise for all of these — each
requires different statistical treatments, different thresholds, and different
output schemas.

## Design

### Architecture

```
Raw events (AgentSession + ToolCallTrace + Experience + Change)
    │
    ▼
┌─────────────────────────────────────────────────┐
│                  REFINERY                        │
│                                                 │
│  Pass 1: EpisodeGrouper                         │
│    sessions + traces → Episode rows              │
│    (cached, cheap to re-read)                    │
│                                                 │
│  Pass 2: PatternExtractor                       │
│    Episodes → pattern artifacts                  │
│    (n-gram frequency mining, deterministic)      │
│                                                 │
│  Pass 3: AntiPatternDetector                    │
│    Episodes → anti_pattern artifacts             │
│    (lift-based association rule mining)          │
│                                                 │
│  Pass 4+: ToolRecipeMiner, ModelRouter, ...     │
│    (future, same interface)                      │
│                                                 │
└─────────────────────────────────────────────────┘
    │
    ▼
KnowledgeArtifact table (kind discriminator)
    │
    ├→ injected into agent prompts via policy matching
    ├→ visualised in the Knowledge Growth dashboard
    └→ cross-project transferable (project_id = "" means global)
```

### Key data model

**`Episode`** — a pre-joined, cached view of one agent session's tool-call
sequence and outcome. Produced by `EpisodeGrouper`. Downstream passes read
Episodes instead of re-joining `ToolCallTrace` every time.

```sql
CREATE TABLE episode (
  id            VARCHAR(64) PRIMARY KEY,
  project_id    VARCHAR(64),
  session_id    VARCHAR(64) UNIQUE,  -- 1:1 with AgentSession
  role          VARCHAR(32),         -- audit/fix/evaluate/...
  outcome       VARCHAR(16),         -- success/failure/partial/unknown
  audit_level   VARCHAR(8),         -- L0/L1/L2/none
  tool_sequence TEXT,               -- "grep read edit change_submit"
  tool_call_count INT,
  files_touched JSON,
  duration_ms   INT,
  status        VARCHAR(16) DEFAULT 'new',
  created_at    DATETIME
);
```

**`KnowledgeArtifact`** — unified output type. Different `kind` values have
different `payload` JSON schemas. Using one table with a discriminator keeps
migrations cheap while the set of passes is still evolving.

```sql
CREATE TABLE knowledge_artifact (
  id             VARCHAR(64) PRIMARY KEY,
  project_id     VARCHAR(64),
  kind           VARCHAR(32),       -- pattern/anti_pattern/tool_recipe/...
  name           VARCHAR(256),      -- stable upsert key, e.g. "pat: grep→read→edit"
  summary        TEXT,
  payload        JSON,              -- kind-specific schema
  produced_by    VARCHAR(64),       -- "pattern_extractor/v1"
  source_events  JSON,              -- [ep_id, ep_id, ...]
  confidence     FLOAT DEFAULT 0,
  hit_count      INT DEFAULT 0,     -- times matched a situation
  usage_count    INT DEFAULT 0,     -- times injected into a prompt
  success_count  INT DEFAULT 0,     -- positive outcome after application
  failure_count  INT DEFAULT 0,     -- negative outcome after application
  status         VARCHAR(16) DEFAULT 'candidate',
  version        INT DEFAULT 1,
  last_used_at   DATETIME,
  created_at     DATETIME,
  updated_at     DATETIME,
  UNIQUE(project_id, kind, name)    -- upsert key
);
```

**`RefineryRun`** — audit trail for each pipeline execution.

### Pass interface

```go
type Pass interface {
    Name() string       // e.g. "pattern_extractor/v1"
    Produces() []string // artifact kinds this pass outputs
    Requires() []string // artifact kinds (or "episode") this pass depends on
    Run(ctx *Context) (Stats, error)
}
```

Passes are **deterministic first**: v1 implementations use frequency mining
and lift calculations, not LLM calls. This means:

- Results are reproducible bit-for-bit given the same Episode set
- Running the refinery is free (no token cost)
- A/B experiments are trivial: re-run with different thresholds and compare

### Pattern extraction algorithm

1. For each Episode, extract all n-grams of length 2–4 from `tool_sequence`
2. Count **distinct-episode support** (not raw occurrences) for each n-gram
3. Filter: `support ≥ 3` AND `confidence (success_rate) ≥ 0.70`
4. Upsert as `KnowledgeArtifact(kind=pattern, name="pat: grep→read→edit")`

### Anti-pattern detection algorithm

1. Compute baseline failure rate across all Episodes with known outcomes
2. For each n-gram, compute `P(failure | contains n-gram) / P(failure overall)` = **lift**
3. Filter: `support ≥ 2` AND `lift ≥ 1.5`
4. Upsert as `KnowledgeArtifact(kind=anti_pattern, name="anti: edit→edit")`

### Effectiveness tracking

Every artifact has `hit_count`, `usage_count`, `success_count`, `failure_count`.
When the scheduler injects an artifact into an agent prompt, it bumps
`usage_count`. When the session completes, the outcome bumps `success_count`
or `failure_count`. This enables:

- **Automatic deprecation**: artifacts with `success_rate < 0.3` over the
  last 30 uses get `status=deprecated`
- **A/B measurement**: compare outcomes of sessions with vs without a
  specific artifact injected
- **Meta-refinery pass**: a future pass that analyses which passes produce
  the highest-utility artifacts and adjusts pass priorities

## API

| Endpoint | Method | Purpose |
|----------|--------|---------|
| `/refinery/run` | POST | Trigger a refinery run (human-only) |
| `/refinery/runs` | GET | List recent runs with pass stats |
| `/refinery/artifacts` | GET | List artifacts, filter by kind/status |
| `/refinery/growth` | GET | Time-series of artifact counts per kind |

## Frontend: Knowledge Growth page

Accessible via the 🧠 Knowledge sidebar tab. Features:

- **Kind summary cards** — click to filter by kind
- **Growth sparklines** — 30-day trend per kind
- **Recent runs** — status, duration, trigger type
- **Artifact list** — expandable cards showing payload, effectiveness counters, provenance

## Relationship to existing Analyze Agent

The refinery is **additive**, not a replacement. The existing flow
(`TriggerAnalyzeAgent` → LLM-based `analyze_output` → SkillCandidate/Policy)
continues to work. The refinery feeds the same downstream tables via a
parallel, deterministic path. Over time, as refinery artifacts prove
effective, the LLM-based Analyze Agent can focus on higher-order synthesis
that only language models can do (e.g. generating natural-language
preconditions for a tool recipe).

## Future passes

| Pass | Input | Output | Complexity |
|------|-------|--------|------------|
| `ToolRecipeMiner` | successful Episodes by task_tag | ordered tool-call templates with per-step hints | medium |
| `ModelRouter` | Episodes + model + cost + outcome | "task_tag X → model Y" routing rules | medium |
| `FailureTaxonomist` | all failed Episodes | hierarchical failure classification | high (needs LLM) |
| `TemporalPolicyMiner` | cross-time Episodes | "after event X, Y happens within N min" | medium |
| `MetaRefinery` | KnowledgeArtifact effectiveness stats | pass priority adjustments | low |

## Experimental protocol

To produce the "killer metric" (Day 1 vs Day N improvement curve):

1. **Baseline**: clear all artifacts, run 10 benchmark tasks, record success rate
2. **Run refinery**: extract patterns + anti-patterns
3. **Apply**: inject active artifacts into agent prompts for the next 10 tasks
4. **Measure**: compare success rate, L2 rejection rate, time-to-completion
5. **Repeat**: each cycle adds more artifacts; plot the curve

This protocol is fully automatable via the `/refinery/run` API and the
existing task/claim/submit workflow.

## Why this matters

The refinery turns A3C from "a multi-agent platform with a self-improvement
feature" into **a measurably self-improving system**. The key insight is that
agent tool traces are an underutilised weak-supervision signal: the same
data can be read through multiple lenses, each producing orthogonal knowledge
that compounds over time.

This is the "information refinery" pattern: raw data → multiple extraction
passes → typed artifacts → effectiveness tracking → meta-optimization. It
applies beyond A3C — any system that records structured agent activity can
adopt it.
