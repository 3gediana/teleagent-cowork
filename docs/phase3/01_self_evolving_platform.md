# A3C Phase 3：自进化 Agent 开发平台

> **创建时间**: 2026-04-21  
> **版本**: 基于 revert-v1.3 分支代码分析  
> **目标**: 将 A3C 从"多 Agent 协作控制平台"升级为"具备经验积累、策略演化、自治调度能力的 Agent 开发操作系统"
> 
> **⚠️ 本文档为思路文档，已拆分为两个可执行开发文档**：
> - **Phase 3A 自动化** → `docs/dev/11_phase3a_automation.md`（先做）
> - **Phase 3B 自演进** → `docs/dev/12_phase3b_self_evolution.md`（后做）
> 
> **为什么先做自动化**：自演进需要大量自动运行的轨迹作为输入。如果人类还在手动审批每一步，数据太稀疏，Analyze Agent 学不到东西。先让系统"自己跑起来"，再让它"越跑越好"。

---

## 1. 为什么需要自进化

### 1.1 当前痛点

| 痛点 | 说明 |
|------|------|
| **每次都像第一次** | Agent 执行任务时没有历史经验参考，相同类型的错误反复出现 |
| **Token 白白消耗** | Agent 消耗几万 token 执行任务，但只留下 diff，决策推理、踩坑过程全部丢弃 |
| **错误无法沉淀** | L1/L2 审核结果、Fix 失败、PR 拒绝等信号未被结构化记录和学习 |
| **策略全靠人定** | 模型选择、审核强度、流程路由全靠人类手动配置 |
| **人工介入率高** | 大量审批节点需要人类操作，平台无法自主决策 |

### 1.2 目标形态

**一个会从自己历史中学习的 Agent 开发操作系统。**

人类只负责：
- 提需求
- 定边界
- 做最终兜底授权

平台自己负责：
- 需求澄清 → 任务拆解 → 派工 → 执行 → 审查 → 复盘 → 提炼经验 → 下次自动应用

### 1.3 核心闭环

```
需求输入
  → 任务建模（标签 + 画像）
  → Agent 执行（策略路由 + 防错约束）
  → 经验内化（feedback 工具 + 扩展 output）  ← 关键：让消耗的 token 不浪费
  → 过程记录（轨迹 + 工具调用）
  → 结果评估（审核信号 + 成功/失败标签）
  → 失败/成功模式提炼（Analyze Agent 蒸馏 raw experience）
  → 生成技能/策略（SkillCandidate + Policy）
  → 下次自动匹配应用（TaskProfiler + PolicyEngine）
  → 错误率下降
```

### 1.4 核心原则：Token 不浪费

Agent 每次执行任务消耗几万 token，但当前只留下 diff 和审核结果。**决策推理、踩坑过程、方案选择理由全部丢弃**。

自进化的第一步不是"采集原始日志再分析"，而是**让已经消耗了 token 的 Agent 在任务结束时自己做"经验压缩"**。一条 `key_insight` 比一万行原始日志更有价值。

为什么这样做比事后分析更高效：
1. **Agent 自己知道什么重要** — 它经历过整个过程
2. **天然压缩** — 几万 token 的执行过程 → 几百 token 的结构化经验
3. **即时性** — 不用等定时分析，经验在任务完成时就沉淀了
4. **可操作性** — Agent 提炼的是"下次该怎么做"，不是原始数据

---

## 2. 架构设计

### 2.1 三层架构

```
┌─────────────────────────────────────────────────────┐
│                    A3C Platform                       │
├─────────────────────────────────────────────────────┤
│  Observe 层 — 采集一切执行轨迹 + 经验内化              │
│  ├── AgentSession (持久化)                            │
│  ├── ToolCallTrace (工具调用轨迹)                     │
│  ├── Change.FailureMode (失败模式标签)                │
│  ├── TaskTag (任务场景标签)                           │
│  ├── Experience (经验记录，核心)                      │
│  ├── feedback 工具 (客户端 Agent 经验压缩)             │
│  └── 扩展 output 参数 (内部 Agent 推理捕获)            │
├─────────────────────────────────────────────────────┤
│  Learn 层 — 蒸馏经验、提炼技能、归纳场景                │
│  ├── Analyze Agent (蒸馏 raw experience → distilled)  │
│  ├── SkillCandidate 库                               │
│  ├── Policy 库                                       │
│  └── Analyze Timer (每日定时触发)                     │
├─────────────────────────────────────────────────────┤
│  Act 层 — 运行时匹配经验、路由策略                     │
│  ├── TaskProfiler (任务画像)                          │
│  ├── PolicyEngine (策略路由)                          │
│  ├── Chief Agent (人类接口，新角色)                   │
│  └── AutonomyLevel (自治等级)                         │
└─────────────────────────────────────────────────────┘
```

### 2.2 数据流

```
客户端 Agent 完成任务
  ↓ 调用 feedback 工具（经验压缩）
  ↓
Experience 记录 (source=agent_feedback, status=raw)    ← Observe
  ↓
内部 Agent 执行审核/修复/评审
  ↓ 扩展的 output 参数（推理捕获）
  ↓
Experience 记录 (source=audit_observation/fix_strategy/eval_pattern, status=raw)  ← Observe
  ↓
Session 持久化 + ToolCallTrace 记录                    ← Observe
  ↓
审核结果 → Change.FailureMode 自动标注                 ← Observe
  ↓
Analyze Agent 定时蒸馏 raw experience → distilled      ← Learn
  ↓
输出 SkillCandidate + Policy 建议                      ← Learn
  ↓
人类审批 → Skill/Policy 变为 active                    ← Learn
  ↓
新任务进来 → TaskProfiler 画像                         ← Act
  ↓
PolicyEngine 匹配策略 → 注入执行配置                   ← Act
  ↓
Agent 带着经验执行 → 错误率下降                        ← 效果
```

---

## 3. Observe 层详细设计

### 3.1 AgentSession 持久化

**现状**：`agent.Session` 只在内存 map（`DefaultManager.sessions`），进程重启丢失。

**新增模型**：

```go
type AgentSession struct {
    ID                string    `gorm:"primaryKey;size:64" json:"id"`
    Role              string    `gorm:"size:32;index" json:"role"`
    ProjectID         string    `gorm:"size:64;index" json:"project_id"`
    ChangeID          string    `gorm:"size:64" json:"change_id"`
    PRID              string    `gorm:"size:64" json:"pr_id"`
    TriggerReason     string    `gorm:"size:64" json:"trigger_reason"`
    Status            string    `gorm:"size:20;index" json:"status"` // pending/running/completed/failed
    ModelProvider     string    `gorm:"size:64" json:"model_provider"`
    ModelID           string    `gorm:"size:128" json:"model_id"`
    OpenCodeSessionID string    `gorm:"size:128" json:"opencode_session_id"`
    Output            string    `gorm:"type:text" json:"output"`
    PromptHash        string    `gorm:"size:64" json:"prompt_hash"` // 追踪 prompt 变更效果
    DurationMs        int       `json:"duration_ms"`
    CreatedAt         time.Time `gorm:"index" json:"created_at"`
    CompletedAt       *time.Time `json:"completed_at"`
}
```

**改动点**：
- `agent/manager.go`：`CreateSession` / `RegisterSession` 同时写 DB
- `UpdateSessionOutput` / `MarkSessionFailed` 同步更新 DB
- `GetSession` 先查内存，miss 则查 DB
- `scheduler.go`：session 完成时记录 `DurationMs` 和 `CompletedAt`

### 3.2 ToolCallTrace

**现状**：`processToolCall` 只调 handler + 广播，不记录。

**新增模型**：

```go
type ToolCallTrace struct {
    ID         string    `gorm:"primaryKey;size:64" json:"id"`
    SessionID  string    `gorm:"size:64;index" json:"session_id"`
    ProjectID  string    `gorm:"size:64;index" json:"project_id"`
    ToolName   string    `gorm:"size:32;index" json:"tool_name"`
    Args       string    `gorm:"type:json" json:"args"`
    Result     string    `gorm:"type:text" json:"result"`      // handler 返回结果摘要
    Success    bool      `gorm:"index" json:"success"`
    CreatedAt  time.Time `gorm:"index" json:"created_at"`
}
```

**改动点**：
- `scheduler.go` 的 `processToolCall`：在调 `ToolCallHandler` 后，异步写 `ToolCallTrace`
- `tool_handler.go`：各 handler 返回 error 信息，供 trace 记录 success/fail

### 3.3 Change 失败模式标签

**新增字段**（在 `model.Change` 上）：

```go
FailureMode string  `gorm:"size:64" json:"failure_mode"` // wrong_assumption / tool_misuse / incomplete_fix / over_edit / invalid_output / missing_context
RetryCount  int     `gorm:"default:0" json:"retry_count"` // 同一 task 被重新提交的次数
```

**失败模式枚举**：

| 模式 | 说明 | 触发条件 |
|------|------|---------|
| `wrong_assumption` | Agent 基于错误假设修改代码 | Audit 发现改动与需求不符 |
| `tool_misuse` | 工具调用方式错误 | 工具参数不合法或结果异常 |
| `incomplete_fix` | 修复不完整 | Fix Agent 标记 fixed=true 但 Audit2 仍拒绝 |
| `over_edit` | 改动范围过大 | 改了不该改的文件 |
| `invalid_output` | 输出格式不合规 | result 值不在枚举范围内 |
| `missing_context` | 缺少必要上下文 | Agent 未读关键文件就修改 |

**改动点**：
- `audit.go` 的 `ProcessAuditOutput`：L1 时根据 issues 类型自动标 FailureMode
- `ProcessFixOutput`：fix 失败时标 FailureMode
- `change.go` handler：同一 task 重复提交时 RetryCount++

### 3.4 TaskTag

**新增模型**：

```go
type TaskTag struct {
    ID        string    `gorm:"primaryKey;size:64" json:"id"`
    TaskID    string    `gorm:"size:64;index" json:"task_id"`
    Tag       string    `gorm:"size:64;index" json:"tag"`
    Source    string    `gorm:"size:20" json:"source"` // human / auto / analyze
    CreatedAt time.Time `json:"created_at"`
}
```

**标签分类体系**：

- **任务类型**：`bugfix` / `feature` / `refactor` / `integration` / `infra` / `docs` / `review_only`
- **技术场景**：`frontend` / `backend` / `db` / `api_contract` / `git_conflict` / `agent_tooling` / `prompt_alignment`
- **风险等级**：`high_context_dependency` / `multi_file` / `schema_change` / `side_effect_heavy`
- **失败模式**：`wrong_assumption` / `missing_context` / `tool_misuse` / `incomplete_fix` / `over_edit`

**改动点**：
- `handler/task.go`：Create/Claim 时允许传 tags
- `agent/tools.go`：`create_task` 工具加 `tags` 参数
- `tool_handler.go`：`handleCreateTask` 处理 tags

### 3.5 Experience — 经验记录（核心）

**设计理念**：不是存原始日志再分析，而是让每个消耗了 token 的 Agent 在完成时主动做"经验压缩"。Experience 是 Observe 层最重要的模型。

**新增模型**：

```go
type Experience struct {
    ID           string    `gorm:"primaryKey;size:64" json:"id"`
    ProjectID    string    `gorm:"size:64;index" json:"project_id"`
    SourceType   string    `gorm:"size:32;index" json:"source_type"`   // agent_feedback / audit_observation / fix_strategy / eval_pattern / maintain_rationale
    SourceID     string    `gorm:"size:64" json:"source_id"`           // session ID or task ID
    AgentRole    string    `gorm:"size:32;index" json:"agent_role"`    // 哪个角色产出的
    TaskID       string    `gorm:"size:64;index" json:"task_id"`      // 关联的任务
    Outcome      string    `gorm:"size:20;index" json:"outcome"`      // success / partial / failed

    // 核心经验内容（来自 feedback 工具或扩展 output）
    Approach     string    `gorm:"type:text" json:"approach"`          // 采取了什么方法
    Pitfalls     string    `gorm:"type:text" json:"pitfalls"`          // 踩了什么坑
    KeyInsight   string    `gorm:"type:text" json:"key_insight"`       // 关键洞察（最核心，一条就够）
    MissingCtx   string    `gorm:"type:text" json:"missing_context"`   // 缺少什么上下文
    DoDifferently string   `gorm:"type:text" json:"do_differently"`   // 下次怎么做（反事实学习）

    // 结构化补充（来自内部 Agent 扩展 output）
    PatternObserved string  `gorm:"type:text" json:"pattern_observed"` // 审核发现的模式
    FixStrategy     string  `gorm:"type:text" json:"fix_strategy"`     // 修复策略
    QualityPatterns string  `gorm:"type:json" json:"quality_patterns"` // 代码质量模式
    FalsePositive   bool   `gorm:"default:false" json:"false_positive"` // 是否误判

    // 上下文标签
    Tags         string    `gorm:"type:json" json:"tags"`             // 自动标注的标签
    FilesInvolved string   `gorm:"type:json" json:"files_involved"`   // 涉及的文件路径

    Status       string    `gorm:"size:20;default:'raw'" json:"status"` // raw / distilled / skill / deprecated
    CreatedAt    time.Time `gorm:"index" json:"created_at"`
}
```

**状态流转**：

```
raw → (Analyze Agent 蒸馏) → distilled → (人类审批) → skill
                                                      ↓
                                               (过时) → deprecated
```

**Experience 的来源**：

| 来源 | 触发时机 | SourceType |
|------|---------|------------|
| 客户端 Agent feedback 工具 | 任务完成/失败后主动调用 | `agent_feedback` |
| Audit Agent 扩展 output | 审核完成时 | `audit_observation` |
| Fix Agent 扩展 output | 修复完成时 | `fix_strategy` |
| Evaluate Agent 扩展 output | PR 评审完成时 | `eval_pattern` |
| Maintain Agent 扩展 output | 创建任务/规划时 | `maintain_rationale` |

### 3.6 信息浪费分析

现有流程中每个环节消耗的 token 和丢失的信息：

| 环节 | 消耗 token | 当前保留 | 丢失（有价值） |
|------|-----------|---------|---------------|
| 客户端 Agent 执行任务 | ~5-20K | diff, files 列表 | 决策理由、踩坑过程、哪些上下文有用 |
| Audit1 审核 | ~2-5K | level, issues, reason | 边界判断推理、发现的模式、改进建议 |
| Fix 修复 | ~3-8K | action, fixed(bool) | 修复策略、是否误判、其他问题 |
| Evaluate 评审 PR | ~5-15K | result, rating, reason | 代码质量模式、常见错误模式 |
| Maintain 管理项目 | ~3-10K | 创建的 task/milestone | 规划推理、风险判断、优先级理由 |

**哪些数据不值得存**：

| 数据 | 为什么不值得 | 替代方案 |
|------|-------------|---------|
| Agent 完整输出文本 | 太长、重复、不可操作 | 只存 key_insight |
| 每个 tool call 的完整参数 | 大部分是样板 | 只存 tool name + success/fail |
| Session 的完整 prompt | 变化不大 | 只存 prompt_hash 做版本追踪 |
| 文件完整内容 | 已在 git 里 | 只存文件路径列表 |
| LLM 中间推理过程 | 太长且不可靠 | 只存最终决策 + 理由 |

**结论**：不是所有数据都需要存，只存"压缩后的经验"。ToolCallTrace 存轻量摘要（tool name + success），Experience 存 Agent 自己提炼的结构化洞察。

### 3.7 客户端 Agent feedback 工具

在 MCP 客户端新增 `feedback` 工具，让客户端 Agent 在完成任务后主动总结经验：

```typescript
// client/mcp/src/index.ts 新增
{
  name: "feedback",
  description: "Submit task completion feedback with lessons learned. " +
    "Call this after completing or failing a task to help the platform learn. " +
    "Your insights will be distilled into reusable skills for future tasks.",
  inputSchema: {
    type: "object",
    properties: {
      task_id:           { type: "string",  description: "Task ID" },
      outcome:           { type: "string",  enum: ["success", "partial", "failed"],
                          description: "Task outcome" },
      approach:          { type: "string",  description: "What approach did you take and why" },
      pitfalls:          { type: "string",  description: "What went wrong or what was tricky" },
      key_insight:       { type: "string",  description: "One key insight for future similar tasks" },
      missing_context:   { type: "string",  description: "What info did you need but didn't have" },
      would_do_differently: { type: "string", description: "What would you do differently next time" },
      files_read:        { type: "array",   items: { type: "string" },
                          description: "Files that were actually useful" }
    },
    required: ["task_id", "outcome"]
  }
}
```

**关键设计**：
- `outcome` 必填，其他选填 — 降低调用门槛，不强制 Agent 写长总结
- `key_insight` 只要求一条 — 强制做最关键压缩，避免泛泛而谈
- `would_do_differently` — 这是"反事实学习"的核心，比记录"做了什么"更有价值
- `files_read` — 标记哪些文件对这类任务有用，下次可自动注入上下文

**后端处理**：

```go
// handler/feedback.go
func (h *FeedbackHandler) Submit(c *gin.Context) {
    // 解析 feedback 参数
    // 写入 Experience 表 (source_type=agent_feedback, status=raw)
    // 自动关联 TaskTag
    // 如果有 files_read，更新 TaskProfile 缓存
}
```

**新增 API**：

| 路径 | 方法 | 说明 |
|------|------|------|
| `/api/v1/feedback/submit` | POST | 客户端 Agent 提交反馈 |

### 3.8 内部 Agent output 参数扩展

不是所有 Agent 都需要单独的 feedback 工具。内部 Agent（Audit/Fix/Evaluate/Maintain）已有 output 工具，只需**扩展输出参数**来捕获推理过程：

#### audit_output 扩展

```diff
  { name: "level",         type: "string",  required: true },
  { name: "issues",        type: "array" },
  { name: "reject_reason", type: "string" },
+ { name: "pattern_observed",          type: "string",  description: "Recurring pattern seen in this submission" },
+ { name: "suggestion_for_submitter", type: "string",  description: "How the submitter could avoid this issue next time" }
```

#### fix_output 扩展

```diff
  { name: "action",        type: "string",  required: true },
  { name: "fixed",         type: "boolean" },
  { name: "delegate_to",   type: "string" },
  { name: "reject_reason", type: "string" },
+ { name: "fix_strategy",    type: "string",  description: "What fix approach worked or why it couldn't be fixed" },
+ { name: "false_positive",  type: "boolean", description: "Was this a false positive from audit?" }
```

#### evaluate_output 扩展

```diff
  { name: "result",            type: "string",  required: true },
  { name: "merge_cost_rating",  type: "string" },
  { name: "reason",             type: "string" },
+ { name: "quality_patterns",   type: "array",   description: "Code quality patterns observed (good or bad)" },
+ { name: "common_mistakes",    type: "array",   description: "Recurring mistakes seen across PRs from this project" }
```

#### biz_review_output 扩展

```diff
  { name: "result",             type: "string",  required: true },
  { name: "biz_review",         type: "string" },
  { name: "version_suggestion", type: "string" },
+ { name: "alignment_rationale", type: "string", description: "Why this PR aligns or misaligns with project direction" }
```

**后端处理改动**：

`tool_handler.go` 的 `HandleToolCallResult`：在处理原有参数后，提取扩展参数，写入 Experience 表。

```go
// 伪代码
func HandleToolCallResult(sessionID, changeID, projectID, toolName string, args map[string]interface{}) {
    // ... 原有逻辑 ...
    
    // 提取经验并写入 Experience
    if hasExperienceFields(toolName, args) {
        writeExperience(sessionID, projectID, toolName, args)
    }
}
```

---

## 4. Learn 层详细设计

### 4.1 Analyze Agent

**新角色**：

```go
RoleAnalyze Role = "analyze"
```

**职责**：
- 蒸馏 raw Experience 记录（合并同一 task 的多条 experience，去重、提炼、归纳）
- 聚类失败模式（哪些 FailureMode + pitfall 组合高频出现）
- 找高成功复用模式（哪些 tag 组合下 key_insight 可复用）
- 产出候选技能（SkillCandidate）
- 产出候选策略（Policy 建议）
- 模型效果对比（不同模型在同一角色下的成功率差异）
- 将 raw experience 标记为 distilled

**触发方式**：
- 新增 `StartAnalyzeTimer()`，每日凌晨运行（类似 `StartMaintainTimer`）
- 也可通过 Dashboard 手动触发

**工具**：
- `analyze_output`：输出分析结果

**Prompt 核心输入**：
- 最近 status=raw 的 Experience 记录（含 source_type, outcome, key_insight, pitfalls, do_differently）
- 对应的 AgentSession（含 role, status, duration, model）
- Change 审核结果（L0/L1/L2 + FailureMode）
- TaskTag 分布
- 当前已有 Skill/Policy 列表（避免重复）

**Prompt 核心输出要求**：
- 结构化的 skill 候选（name, type, applicable_tags, action, prohibition, evidence）
- 结构化的 policy 候选（name, match_condition, actions, priority）
- 标签建议（哪些 task 应该打什么标签）
- 模型建议（哪个角色应该用什么模型）
- 需要标记为 distilled 的 experience ID 列表

### 4.2 SkillCandidate 库

**新增模型**：

```go
type SkillCandidate struct {
    ID             string    `gorm:"primaryKey;size:64" json:"id"`
    Name           string    `gorm:"size:128;not null" json:"name"`
    Type           string    `gorm:"size:32;index" json:"type"` // process / prompt / routing / guard
    ApplicableTags string    `gorm:"type:json" json:"applicable_tags"` // ["bugfix","backend"]
    Precondition   string    `gorm:"type:text" json:"precondition"`    // 什么时候用
    Action         string    `gorm:"type:text" json:"action"`          // 具体建议
    Prohibition    string    `gorm:"type:text" json:"prohibition"`    // 禁止事项
    SourceCaseIDs  string    `gorm:"type:json" json:"source_case_ids"` // 来源 session ID 列表
    Evidence       string    `gorm:"type:text" json:"evidence"`        // 证据摘要
    Status         string    `gorm:"size:20;index" json:"status"`      // candidate / approved / active / deprecated
    Version        int       `gorm:"default:1" json:"version"`
    ApprovedBy     string    `gorm:"size:64" json:"approved_by"`       // 审批人 agent ID
    CreatedAt      time.Time `json:"created_at"`
    UpdatedAt      time.Time `json:"updated_at"`
}
```

**技能类型**：

| 类型 | 说明 | 示例 |
|------|------|------|
| `process` | 流程技能 | "多文件改动必须走 PR 流" |
| `prompt` | 提示技能 | "有枚举输出的 Agent 必须附加 result schema" |
| `routing` | 路由技能 | "high_risk 任务用 claude-sonnet-4" |
| `guard` | 防错技能 | "改 schema 前必须读当前 schema 文件" |

**状态流转**：

```
candidate → (人类审批) → active → (新版本替代) → deprecated
                ↓
           (人类拒绝) → rejected
```

### 4.3 Policy 库

**新增模型**：

```go
type Policy struct {
    ID             string    `gorm:"primaryKey;size:64" json:"id"`
    Name           string    `gorm:"size:128;not null" json:"name"`
    MatchCondition string    `gorm:"type:json" json:"match_condition"` // {"tags":["multi_file","high_risk"],"role":"audit_1"}
    Actions        string    `gorm:"type:json" json:"actions"`         // {"model":"anthropic/claude-sonnet-4","audit_level":"L1_required","require_pr":true}
    Priority       int       `gorm:"default:0" json:"priority"`        // 高优先级先匹配
    Status         string    `gorm:"size:20;index" json:"status"`      // candidate / active / deprecated
    SourceSkillID  string    `gorm:"size:64" json:"source_skill_id"`   // 关联的 skill
    HitCount       int       `gorm:"default:0" json:"hit_count"`       // 命中次数
    SuccessRate    float64   `gorm:"default:0" json:"success_rate"`    // 命中后成功率
    Version        int       `gorm:"default:1" json:"version"`
    CreatedAt      time.Time `json:"created_at"`
    UpdatedAt      time.Time `json:"updated_at"`
}
```

**Policy Actions 可包含**：

| Action | 说明 |
|--------|------|
| `model` | 覆盖模型选择 |
| `audit_level` | 强制最低审核等级 |
| `require_pr` | 强制走 PR 流程 |
| `guard_prompt` | 追加防错提示到 prompt |
| `require_context` | 强制先读取指定文件 |
| `max_file_changes` | 限制最大文件改动数 |

---

## 5. Act 层详细设计

### 5.1 TaskProfiler — 任务画像

**新 service**：`task_profiler.go`

```go
type TaskProfile struct {
    TaskID         string
    Tags           []string
    SimilarPast    []string   // 相似历史 session ID
    RiskLevel      string     // low / medium / high
    SuggestedFlow  string     // "change" / "pr" / "pr_with_review"
    SuggestedModel string
    GuardRails     []string   // 防错约束
}
```

**画像逻辑**：
1. 接收任务 ID
2. 查 TaskTag 获取标签
3. 查历史相似任务（按 tag 集合 Jaccard 相似度，取 top 5）
4. 匹配 Policy（按 MatchCondition，按 Priority 排序）
5. 汇总返回 TaskProfile

**调用点**：
- `handler/task.go` 的 `Claim`：Agent 领任务时返回 profile
- MCP 客户端的 `task` 工具：返回 profile 作为上下文
- Chief Agent 派工时使用

### 5.2 PolicyEngine — 策略路由

**新 service**：`policy_engine.go`

```go
func MatchPolicies(tags []string, role agent.Role) []*model.Policy
func ApplyPolicy(session *agent.Session, policy *model.Policy) *agent.Session
```

**MatchPolicies 逻辑**：
1. 查所有 `status=active` 的 Policy
2. 按 Priority 降序排列
3. 逐个检查 MatchCondition：tags 是否包含 policy 要求的标签，role 是否匹配
4. 返回所有匹配的 Policy

**ApplyPolicy 逻辑**：
- `model`：覆盖 session 使用的模型
- `audit_level`：在 SessionContext 中标记最低审核等级
- `require_pr`：强制走 PR 流程而非 Change
- `guard_prompt`：追加到 prompt 末尾
- `require_context`：在 prompt 中注入"必须先读取以下文件"
- `max_file_changes`：在 prompt 中注入文件数限制

**改动点**：
- `scheduler.go` 的 `Dispatch`：在 `BuildPrompt` 之前，先调 `MatchPolicies` + `ApplyPolicy`
- `audit.go`：检查 policy 要求的最低审核等级

### 5.3 AutonomyLevel — 自治等级

**Project 新增字段**：

```go
AutonomyLevel string `gorm:"size:20;default:'supervised'" json:"autonomy_level"`
```

| 等级 | 说明 | 行为 |
|------|------|------|
| `supervised` | 人类审批每一步 | 当前行为不变 |
| `semi_auto` | 人类只审批关键节点 | PR 自动 approve_review，但 approve_merge 仍需人类 |
| `full_auto` | 人类只提需求 | PR 全自动，人类只看报告 |

**改动点**：
- `model/models.go`：Project 加字段
- `handler/pr.go`：`ApproveReview` / `ApproveMerge` 根据 AutonomyLevel 决定是否自动通过
- Dashboard：新增自治等级切换 UI

### 5.4 Chief Agent — 人类接口

**新角色**：

```go
RoleChief Role = "chief"
```

**职责**：
- 接收人类需求（自然语言）
- 澄清需求（多轮对话）
- 拆解为里程碑 + 任务
- 调用 TaskProfiler 为任务画像
- 派工给其他 Agent
- 监控进度
- 协调审核
- 向人类汇报

**工具**：
- `create_milestone` — 创建里程碑
- `create_task` — 创建任务（复用现有，加 tags）
- `assign_task` — 指派任务给 Agent
- `request_review` — 请求审核
- `report_status` — 向人类汇报进度
- `chief_output` — 输出最终结果

**调用入口**：
- Dashboard 的 input 接口（复用现有 `dashboard/input`）
- 新增 `/api/v1/chief/chat` 端点（独立入口）

**关键设计**：Chief Agent 不是"大 prompt"，它调用 `TaskProfiler` + `PolicyEngine`，基于经验做决策。它的上下文包含：
- 项目方向 + 里程碑
- 当前任务状态
- 历史经验（匹配的 Skill/Policy）
- Agent 可用性

---

## 6. 安全闭环设计

### 6.1 Shadow Mode

`SkillCandidate.Status=candidate` 时不影响运行时。系统记录"如果用了这条策略会怎样"（counterfactual），但不实际执行。

### 6.2 Canary

`Policy` 的 `MatchCondition` 先只匹配低风险 tag 组合。高风险 tag 组合的策略必须经过更多验证才能激活。

### 6.3 Rollback

`Policy.Version` + `Status=deprecated`。当 `SuccessRate` 连续 3 天低于基线时，自动将 Policy 回退到上一个版本。

### 6.4 Human Gate

- Skill 从 `candidate → active` 必须人类审批（Dashboard 操作）
- Policy 从 `candidate → active` 必须人类审批
- AutonomyLevel 从 `supervised → semi_auto → full_auto` 必须人类操作

### 6.5 Metrics 监控

新增定时统计任务：

| 指标 | 计算方式 | 告警条件 |
|------|---------|---------|
| 首次通过率 | L0 / (L0+L1+L2) | < 50% |
| 人工介入率 | 需人类操作的 task / 总 task | > 50% |
| 技能命中率 | 命中 Policy 的 session / 总 session | — |
| 技能有效率 | 命中后 L0 / 命中后总数 | < 基线 |
| 任务完成时长 | avg(completed_at - created_at) | 连续上升 |
| 回归率 | 新 Policy 上线后 L1+L2 率上升 | > 基线 +5% |

---

## 7. 新增文件清单

### 后端模型
| 文件 | 内容 |
|------|------|
| `model/agent_session.go` | AgentSession 模型 |
| `model/tool_call_trace.go` | ToolCallTrace 模型 |
| `model/task_tag.go` | TaskTag 模型 |
| `model/experience.go` | Experience 经验记录模型（核心） |
| `model/skill_candidate.go` | SkillCandidate 模型 |
| `model/policy.go` | Policy 模型 |

### 后端 service
| 文件 | 内容 |
|------|------|
| `service/task_profiler.go` | TaskProfiler 画像逻辑 |
| `service/policy_engine.go` | PolicyEngine 策略路由 |
| `service/analyze.go` | Analyze Agent 触发 + 输出处理 |
| `service/experience.go` | Experience 写入 + 查询逻辑 |

### 后端 handler
| 文件 | 内容 |
|------|------|
| `handler/skill.go` | Skill CRUD API |
| `handler/policy.go` | Policy CRUD API |
| `handler/chief.go` | Chief Agent 对话 API |
| `handler/feedback.go` | 客户端 Agent feedback 接收 API |

### MCP 客户端
| 文件 | 内容 |
|------|------|
| `client/mcp/src/index.ts` | 新增 `feedback` 工具定义 |

### Agent 定义
| 文件 | 内容 |
|------|------|
| `.opencode/agents/analyze.md` | Analyze Agent 定义 |
| `.opencode/agents/chief.md` | Chief Agent 定义 |
| `.opencode/tools/analyze_output.ts` | Analyze 输出工具 |
| `.opencode/tools/chief_output.ts` | Chief 输出工具 |

### Agent output 扩展（修改现有文件）
| 文件 | 改动 |
|------|------|
| `.opencode/tools/audit_output.ts` | 新增 pattern_observed, suggestion_for_submitter 参数 |
| `.opencode/tools/fix_output.ts` | 新增 fix_strategy, false_positive 参数 |
| `.opencode/tools/evaluate_output.ts` | 新增 quality_patterns, common_mistakes 参数 |
| `.opencode/tools/biz_review_output.ts` | 新增 alignment_rationale 参数 |
| `agent/tools.go` | 对应更新 PlatformTools 定义 |

### 前端
| 页面/组件 | 内容 |
|----------|------|
| Skills 页面 | 查看/审批候选技能 |
| Policies 页面 | 查看/启停策略 |
| 指标仪表盘 | 核心指标可视化 |
| AutonomyLevel 开关 | 项目自治等级切换 |

---

## 8. 实施计划

### 8.1 依赖关系

```
P0-1 Session持久化 ──┐
P0-2 ToolCall轨迹 ───┤
P0-3 结果标签自动标注 ┤──→ P1-1 Analyze Agent ──→ P1-2 SkillCandidate ──→ P2-1 TaskProfiler
P0-4 TaskTag ─────────┤                                                    │
P0-5 Experience ──────┤                                                    ↓
P0-6 feedback工具+output扩展 ┘                                    P2-2 PolicyEngine ──→ P2-4 Chief Agent
                                                                           ↑
                                                                    P1-3 Policy库 ─────┘
                                                                    P2-3 AutonomyLevel
```

### 8.2 Sprint 计划

| Sprint | 内容 | 预计工作量 | 交付物 |
|--------|------|-----------|--------|
| **Sprint 1** | P0-1 + P0-2 + P0-5 | 2-3 天 | AgentSession 持久化 + ToolCallTrace + Experience 模型 |
| **Sprint 2** | P0-3 + P0-4 + P0-6 | 2-3 天 | FailureMode + TaskTag + feedback 工具 + output 扩展 |
| **Sprint 3** | P1-1 + analyze.md + analyze_output.ts | 2-3 天 | Analyze Agent 可运行，蒸馏 raw experience |
| **Sprint 4** | P1-2 + P1-3 | 1-2 天 | SkillCandidate + Policy 数据库 + API |
| **Sprint 5** | P2-1 + P2-2 | 2-3 天 | TaskProfiler + PolicyEngine 运行时生效 |
| **Sprint 6** | P2-3 + P2-4 | 3-4 天 | AutonomyLevel + Chief Agent |

### 8.3 验证标准

每个 Sprint 的验证标准：

| Sprint | 验证方式 |
|--------|---------|
| Sprint 1 | 重启后端后，历史 Session 仍可查询；ToolCallTrace 表有数据；Experience 表可写入 |
| Sprint 2 | 提交一个 L1 change，FailureMode 自动填充；客户端 Agent 可调用 feedback 工具；内部 Agent output 扩展参数写入 Experience |
| Sprint 3 | Analyze Agent 定时触发，将 raw experience 蒸馏为 distilled，输出 SkillCandidate 记录到 DB |
| Sprint 4 | Dashboard 可查看/审批 SkillCandidate；Policy API 可 CRUD |
| Sprint 5 | 新任务领用时返回 TaskProfile；Policy 匹配后影响模型选择/prompt 注入 |
| Sprint 6 | 通过 Dashboard 与 Chief Agent 对话，自动创建里程碑和任务 |

---

## 9. API 新增清单

### 9.1 认证 API

| 路径 | 方法 | 说明 |
|------|------|------|
| `/api/v1/feedback/submit` | POST | 客户端 Agent 提交任务反馈（经验内化） |
| `/api/v1/task/:task_id/tags` | GET/POST | 获取/添加任务标签 |
| `/api/v1/chief/chat` | POST | 与 Chief Agent 对话 |
| `/api/v1/skill/list` | GET | 列出技能候选 |
| `/api/v1/skill/:id/approve` | POST | 审批技能 |
| `/api/v1/skill/:id/reject` | POST | 拒绝技能 |
| `/api/v1/policy/list` | GET | 列出策略 |
| `/api/v1/policy/:id/activate` | POST | 激活策略 |
| `/api/v1/policy/:id/deactivate` | POST | 停用策略 |
| `/api/v1/project/:id/autonomy` | POST | 设置自治等级 |
| `/api/v1/metrics/dashboard` | GET | 指标仪表盘数据 |
| `/api/v1/experience/list` | GET | 查看经验记录（支持按 status/source_type 筛选） |

### 9.2 内部 API

| 路径 | 方法 | 说明 |
|------|------|------|
| `/api/v1/internal/agent/analyze_output` | POST | Analyze Agent 结果回调 |
| `/api/v1/internal/agent/chief_output` | POST | Chief Agent 结果回调 |
| `/api/v1/internal/analyze/trigger` | POST | 手动触发 Analyze Agent |

---

## 10. 关键设计决策

### 10.1 为什么 Skill 和 Policy 分开

- **Skill** 是知识（"这类任务该怎么做"），偏静态
- **Policy** 是决策（"这类任务该用什么配置"），偏动态
- 一个 Skill 可以衍生多个 Policy
- Policy 有命中率、有效率等运行时指标，Skill 没有

### 10.2 为什么先做 Observe 再做 Learn

没有结构化轨迹数据，Analyze Agent 就没有输入。先有数据，才能学习。

### 10.3 为什么 Chief Agent 最后做

如果 Observe/Learn/Act 还没建起来，Chief Agent 只是一个更贵的 prompt orchestrator，不会真正从历史中学习。

### 10.4 为什么标签不要追求完美

标签系统的目标是"对调度有用"，不是"学术完美"。先覆盖高价值场景（bugfix/feature/multi_file/high_risk），后续由 Analyze Agent 自动扩展。

### 10.5 为什么用 feedback 工具而不是事后分析原始日志

- **Agent 自己知道什么重要** — 它经历过整个过程，比事后分析更准确
- **天然压缩** — 几万 token → 几百 token 结构化经验，存储成本极低
- **即时性** — 不用等定时分析，经验在任务完成时就沉淀
- **可操作性** — Agent 提炼的 `key_insight` 和 `would_do_differently` 直接可用
- **反事实学习** — `would_do_differently` 是"如果重来我会怎么做"，比记录"做了什么"更有价值

### 10.6 为什么 Experience 是 Observe 层核心而不是 ToolCallTrace

ToolCallTrace 记录的是"做了什么"（行为），Experience 记录的是"学到了什么"（认知）。对自进化来说，认知比行为更有价值——同一个行为在不同上下文下可能是对的也可能是错的，但认知（key_insight）是跨场景可迁移的。

---

> **文档结束**。Phase 3 的核心是将 A3C 从"流程自动化平台"升级为"经验驱动的自进化平台"。关键原则：**Token 不浪费** — 让每个消耗了 token 的 Agent 在完成时主动做经验压缩，通过 feedback 工具和扩展 output 参数，将决策推理、踩坑过程、改进建议内化为 Experience 记录，再由 Analyze Agent 蒸馏为可复用的 Skill 和 Policy。
