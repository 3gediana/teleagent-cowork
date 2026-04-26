# Phase 3B：自演进 — 让系统越跑越好

> **目标**: 从历史中学习，降低 AI 犯错概率  
> **前提**: Phase 3A 已完成，系统可全自动运行，有足够的执行轨迹数据  
> **核心改变**: 从"每次都像第一次"到"带着经验执行"

---

## 1. 当前问题（Phase 3A 后）

| 问题 | 说明 |
|------|------|
| Agent 每次执行没有历史参考 | 相同类型的错误反复出现 |
| Token 消耗后信息丢弃 | Agent 消耗几万 token，只留下 diff |
| 审核推理未捕获 | Audit 为什么判 L1 而不是 L0？Fix 用了什么策略？ |
| 无经验复用机制 | 成功经验无法自动应用到相似任务 |
| 策略全靠人定 | 模型选择、审核强度、流程路由全靠手动配置 |

---

## 2. 里程碑划分

```
M18 Experience + feedback 工具 ────────┐
M19 内部 Agent output 扩展 ──────────┤──→ M21 Analyze Agent ──→ M22 SkillCandidate + Policy ──→ M23 TaskProfiler + PolicyEngine
M20 Experience 查询 + Dashboard ──────┘
```

---

## 3. M18: Experience 模型 + feedback 工具

### 3.1 目标

客户端 Agent 完成任务后，通过 feedback 工具主动提交结构化经验。

### 3.2 数据模型

```sql
CREATE TABLE experience (
    id VARCHAR(64) PRIMARY KEY,
    project_id VARCHAR(64) NOT NULL,
    source_type VARCHAR(32) NOT NULL,     -- agent_feedback / audit_observation / fix_strategy / eval_pattern / maintain_rationale
    source_id VARCHAR(64) DEFAULT '',     -- session ID or task ID
    agent_role VARCHAR(32) NOT NULL,
    task_id VARCHAR(64) DEFAULT '',
    outcome VARCHAR(20) DEFAULT '',       -- success / partial / failed

    -- 核心经验内容
    approach TEXT,
    pitfalls TEXT,
    key_insight TEXT,                     -- 最核心：一条关键洞察
    missing_context TEXT,
    do_differently TEXT,                  -- 反事实学习：下次怎么做

    -- 结构化补充
    pattern_observed TEXT,
    fix_strategy TEXT,
    quality_patterns JSON,
    false_positive BOOLEAN DEFAULT FALSE,

    -- 上下文
    tags JSON,                            -- 自动标注的标签
    files_involved JSON,                  -- 涉及的文件路径

    status VARCHAR(20) DEFAULT 'raw',    -- raw / distilled / skill / deprecated
    created_at DATETIME NOT NULL,
    INDEX idx_project (project_id),
    INDEX idx_source_type (source_type),
    INDEX idx_role (agent_role),
    INDEX idx_task (task_id),
    INDEX idx_outcome (outcome),
    INDEX idx_status (status),
    INDEX idx_created (created_at)
);
```

### 3.3 MCP feedback 工具

```typescript
// client/mcp/src/index.ts 新增
{
  name: "feedback",
  description: "Submit task completion feedback with lessons learned. " +
    "Call this after completing or failing a task. " +
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

### 3.4 改动清单

| 文件 | 改动 | 验收标准 |
|------|------|---------|
| `model/experience.go` | 新建，定义 Experience 模型 + AutoMigrate | 表创建成功 |
| `model/init.go` | 注册 Experience AutoMigrate | 启动时建表 |
| `handler/feedback.go` | 新建，接收 feedback 请求，写入 Experience | API 可调用 |
| `service/experience.go` | 新建，Experience 写入 + 查询逻辑 | 写入后可查询 |
| `client/mcp/src/index.ts` | 新增 feedback 工具定义 | OpenCode Agent 可调用 |
| `cmd/server/main.go` | 注册 `/api/v1/feedback/submit` 路由 | 路由可访问 |

### 3.5 验收 E2E

1. Agent 完成任务后调用 feedback 工具
2. Experience 表有记录，source_type=agent_feedback, status=raw
3. key_insight 和 do_differently 有内容
4. 通过 `/api/v1/experience/list?status=raw` 可查到

---

## 4. M19: 内部 Agent output 参数扩展

### 4.1 目标

内部 Agent 的 output 工具增加推理捕获参数，审核/修复/评审的推理过程不再丢弃。

### 4.2 扩展参数

#### audit_output 新增

| 参数 | 类型 | 说明 |
|------|------|------|
| `pattern_observed` | string | 本次提交中发现的重复模式 |
| `suggestion_for_submitter` | string | 提交者下次如何避免此问题 |

#### fix_output 新增

| 参数 | 类型 | 说明 |
|------|------|------|
| `fix_strategy` | string | 修复策略（什么方法有效/为什么修不了） |
| `false_positive` | boolean | 是否为 Audit 误判 |

#### evaluate_output 新增

| 参数 | 类型 | 说明 |
|------|------|------|
| `quality_patterns` | array | 代码质量模式（好或坏） |
| `common_mistakes` | array | 该项目 PR 中反复出现的错误 |

#### biz_review_output 新增

| 参数 | 类型 | 说明 |
|------|------|------|
| `alignment_rationale` | string | 为什么此 PR 与项目方向一致/不一致 |

### 4.3 改动清单

| 文件 | 改动 | 验收标准 |
|------|------|---------|
| `agent/tools.go` | audit_output/fix_output/evaluate_output 工具定义加新参数 | 工具 schema 更新 |
| `.opencode/tools/audit_output.ts` | 新增 pattern_observed, suggestion_for_submitter | OpenCode 注册更新 |
| `.opencode/tools/fix_output.ts` | 新增 fix_strategy, false_positive | OpenCode 注册更新 |
| `.opencode/tools/evaluate_output.ts` | 新增 quality_patterns, common_mistakes | OpenCode 注册更新 |
| `.opencode/tools/biz_review_output.ts` | 新增 alignment_rationale | OpenCode 注册更新 |
| `service/tool_handler.go` | `HandleToolCallResult` 提取扩展参数写入 Experience | 扩展参数存入 Experience |

### 4.4 验收 E2E

1. Audit Agent 审核一个 L1 change
2. audit_output 调用带 pattern_observed 参数
3. Experience 表有 source_type=audit_observation 记录
4. pattern_observed 有内容

---

## 5. M20: Experience 查询 + Dashboard

### 5.1 目标

人类可在 Dashboard 查看 Experience 记录，审批 distilled 经验。

### 5.2 改动清单

| 文件 | 改动 | 验收标准 |
|------|------|---------|
| `handler/experience.go` | 新建，Experience 列表查询 API | 可按 status/source_type 筛选 |
| `cmd/server/main.go` | 注册 `/api/v1/experience/list` 路由 | 路由可访问 |
| `frontend/src/components/ExperiencePanel.tsx` | 新建，经验记录展示组件 | 可查看 raw/distilled 经验 |

### 5.3 API

| 路径 | 方法 | 参数 | 说明 |
|------|------|------|------|
| `/api/v1/experience/list` | GET | project_id, status, source_type, limit | 查询经验列表 |

---

## 6. M21: Analyze Agent

### 6.1 目标

定时蒸馏 raw Experience，产出 SkillCandidate 和 Policy 建议。

### 6.2 角色定义

```go
RoleAnalyze Role = "analyze"
```

| 属性 | 值 |
|------|-----|
| Name | Analyze Agent |
| Description | Distills raw experiences into reusable skills and policies |
| PromptTemplate | analyze.md |
| PlatformTools | analyze_output |
| OpenCodeTools | read |

### 6.3 工具定义

#### analyze_output

```typescript
tool({
  name: "analyze_output",
  description: "Output analysis result: distilled experiences, skill candidates, and policy suggestions",
  parameters: z.object({
    distilled_experience_ids: z.array(z.string()).describe("Experience IDs that have been distilled"),
    skill_candidates: z.array(z.object({
      name: z.string(),
      type: z.enum(["process", "prompt", "routing", "guard"]),
      applicable_tags: z.array(z.string()),
      precondition: z.string(),
      action: z.string(),
      prohibition: z.string(),
      evidence: z.string()
    })),
    policy_suggestions: z.array(z.object({
      name: z.string(),
      match_condition: z.record(z.any()),
      actions: z.record(z.any()),
      priority: z.number()
    })),
    tag_suggestions: z.array(z.object({
      task_id: z.string(),
      suggested_tags: z.array(z.string())
    })),
    model_suggestions: z.array(z.object({
      role: z.string(),
      recommended_model: z.string(),
      reason: z.string()
    }))
  })
})
```

### 6.4 触发方式

```go
// service/scheduler.go 新增
func StartAnalyzeTimer() {
    go func() {
        ticker := time.NewTicker(24 * time.Hour)  // 每日运行
        defer ticker.Stop()
        for range ticker.C {
            var projects []model.Project
            model.DB.Where("autonomy_level IN ?", []string{"semi_auto", "full_auto"}).Find(&projects)
            for _, project := range projects {
                TriggerAnalyzeAgent(project.ID)
            }
        }
    }()
}
```

### 6.5 改动清单

| 文件 | 改动 | 验收标准 |
|------|------|---------|
| `agent/role.go` | 新增 `RoleAnalyze` + RoleConfig | 角色注册成功 |
| `agent/tools.go` | 新增 analyze_output 工具定义 | 工具可被调度 |
| `.opencode/agents/analyze.md` | 新建，Analyze Agent prompt | OpenCode serve 注册 |
| `.opencode/tools/analyze_output.ts` | 新建 | OpenCode serve 注册 |
| `service/analyze.go` | 新建，`TriggerAnalyzeAgent` + `HandleAnalyzeOutput` | 可触发 + 可处理输出 |
| `service/scheduler.go` | 新增 `StartAnalyzeTimer` | 定时触发 |
| `cmd/server/main.go` | 启动时调用 `StartAnalyzeTimer` | 定时器运行 |

### 6.6 Analyze Agent Prompt 核心输入

```markdown
## Raw Experiences (last 100)
{{range .RawExperiences}}
- [{{.SourceType}}] {{.AgentRole}} task={{.TaskID}} outcome={{.Outcome}}
  insight: {{.KeyInsight}}
  pitfalls: {{.Pitfalls}}
  do_differently: {{.DoDifferently}}
  pattern: {{.PatternObserved}}
{{end}}

## Current Skills
{{range .CurrentSkills}}
- [{{.Status}}] {{.Name}} ({{.Type}}): {{.Action}}
{{end}}

## Current Policies
{{range .CurrentPolicies}}
- [{{.Status}}] {{.Name}}: match={{.MatchCondition}} actions={{.Actions}}
{{end}}

## Statistics
- Total sessions: {{.TotalSessions}}
- L0 rate: {{.L0Rate}}%
- L1 rate: {{.L1Rate}}%
- L2 rate: {{.L2Rate}}%
- Top failure modes: {{.TopFailureModes}}
```

### 6.7 验收 E2E

1. 系统运行一段时间，Experience 表有 raw 记录
2. Analyze Agent 定时触发
3. raw experience 被标记为 distilled
4. SkillCandidate 表有候选记录
5. Policy 表有候选记录

---

## 7. M22: SkillCandidate + Policy 库

### 7.1 目标

技能和策略可 CRUD，人类可审批/拒绝候选。

### 7.2 数据模型

#### SkillCandidate

```sql
CREATE TABLE skill_candidate (
    id VARCHAR(64) PRIMARY KEY,
    name VARCHAR(128) NOT NULL,
    type VARCHAR(32) NOT NULL,           -- process / prompt / routing / guard
    applicable_tags JSON,
    precondition TEXT,
    action TEXT NOT NULL,
    prohibition TEXT,
    source_case_ids JSON,
    evidence TEXT,
    status VARCHAR(20) DEFAULT 'candidate',  -- candidate / approved / active / deprecated / rejected
    version INT DEFAULT 1,
    approved_by VARCHAR(64) DEFAULT '',
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    INDEX idx_type (type),
    INDEX idx_status (status)
);
```

#### Policy

```sql
CREATE TABLE policy (
    id VARCHAR(64) PRIMARY KEY,
    name VARCHAR(128) NOT NULL,
    match_condition JSON NOT NULL,       -- {"tags":["multi_file"],"role":"audit_1"}
    actions JSON NOT NULL,               -- {"model":"...","guard_prompt":"..."}
    priority INT DEFAULT 0,
    status VARCHAR(20) DEFAULT 'candidate',  -- candidate / active / deprecated
    source_skill_id VARCHAR(64) DEFAULT '',
    hit_count INT DEFAULT 0,
    success_rate FLOAT DEFAULT 0,
    version INT DEFAULT 1,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    INDEX idx_status (status),
    INDEX idx_priority (priority DESC)
);
```

### 7.3 改动清单

| 文件 | 改动 | 验收标准 |
|------|------|---------|
| `model/skill_candidate.go` | 新建模型 + AutoMigrate | 表创建成功 |
| `model/policy.go` | 新建模型 + AutoMigrate | 表创建成功 |
| `handler/skill.go` | 新建，Skill CRUD + approve/reject | API 可操作 |
| `handler/policy.go` | 新建，Policy CRUD + activate/deactivate | API 可操作 |
| `cmd/server/main.go` | 注册 skill + policy 路由 | 路由可访问 |
| `frontend/src/components/SkillPanel.tsx` | 新建，技能查看/审批 | 可查看候选技能 |
| `frontend/src/components/PolicyPanel.tsx` | 新建，策略查看/启停 | 可查看/启停策略 |

### 7.4 API

| 路径 | 方法 | 说明 |
|------|------|------|
| `/api/v1/skill/list` | GET | 列出技能（支持 status 筛选） |
| `/api/v1/skill/:id` | GET | 获取技能详情 |
| `/api/v1/skill/:id/approve` | POST | 审批技能 → status=active |
| `/api/v1/skill/:id/reject` | POST | 拒绝技能 → status=rejected |
| `/api/v1/policy/list` | GET | 列出策略（支持 status 筛选） |
| `/api/v1/policy/:id` | GET | 获取策略详情 |
| `/api/v1/policy/:id/activate` | POST | 激活策略 → status=active |
| `/api/v1/policy/:id/deactivate` | POST | 停用策略 → status=deprecated |

### 7.5 验收 E2E

1. Analyze Agent 产出 SkillCandidate（status=candidate）
2. 人类通过 Dashboard 审批 → status=active
3. 人类拒绝某 Policy → status=deprecated
4. Skill 列表 API 返回正确筛选结果

---

## 8. M23: TaskProfiler + PolicyEngine

### 8.1 目标

运行时匹配经验，新任务自动获得画像和策略注入。

### 8.2 TaskProfiler

```go
type TaskProfile struct {
    TaskID         string   `json:"task_id"`
    Tags           []string `json:"tags"`
    SimilarPast    []string `json:"similar_past"`     // 相似历史 session ID
    RiskLevel      string   `json:"risk_level"`       // low / medium / high
    SuggestedFlow  string   `json:"suggested_flow"`   // change / pr / pr_with_review
    SuggestedModel string   `json:"suggested_model"`
    GuardRails     []string `json:"guard_rails"`      // 防错约束
    RelevantSkills []string `json:"relevant_skills"`  // 相关 skill ID
}
```

**画像逻辑**：
1. 查 TaskTag 获取标签
2. 查 Experience 中同标签的 key_insight（取 top 3）
3. 查 Policy 中匹配的 active policy（按 priority 排序）
4. 汇总返回 TaskProfile

### 8.3 PolicyEngine

```go
func MatchPolicies(tags []string, role agent.Role) []*model.Policy
func ApplyPolicy(session *agent.Session, policy *model.Policy)
```

**ApplyPolicy 效果**：

| Policy Action | 注入方式 |
|---------------|---------|
| `model` | 覆盖 session 使用的模型（通过 RoleOverride） |
| `audit_level` | SessionContext 中标记最低审核等级 |
| `require_pr` | 强制走 PR 流程 |
| `guard_prompt` | 追加到 prompt 末尾 |
| `require_context` | prompt 中注入"必须先读取以下文件" |
| `max_file_changes` | prompt 中注入文件数限制 |

### 8.4 改动清单

| 文件 | 改动 | 验收标准 |
|------|------|---------|
| `service/task_profiler.go` | 新建，TaskProfile 生成逻辑 | 领任务时返回 profile |
| `service/policy_engine.go` | 新建，Policy 匹配 + 应用逻辑 | Policy 可影响运行时 |
| `handler/task.go` | `Claim` 返回时附带 TaskProfile | API 返回 profile |
| `opencode/scheduler.go` | `Dispatch` 前调 PolicyEngine | Policy 生效 |
| `agent/manager.go` | `BuildPrompt` 支持追加 guard_prompt | prompt 末尾有防错提示 |
| `service/audit.go` | 检查 policy 要求的最低审核等级 | audit_level 约束生效 |

### 8.5 验收 E2E

1. 创建一个 Policy：tags=["multi_file"], actions={"guard_prompt":"多文件改动必须走 PR 流程"}
2. 激活该 Policy
3. 创建一个带 multi_file 标签的任务
4. Agent 领取该任务时，TaskProfile 包含该 guard_rail
5. Agent Session 的 prompt 末尾有该防错提示
6. Policy.hit_count +1

---

## 9. 开发顺序

```
M18 Experience + feedback ──┐
M19 output 扩展 ────────────┤──→ M21 Analyze Agent ──→ M22 Skill + Policy ──→ M23 TaskProfiler + PolicyEngine
M20 Experience Dashboard ──┘
```

| 阶段 | 内容 | 预计 | 交付物 |
|------|------|------|--------|
| Step 1 | M18 + M19 | 2-3 天 | feedback 工具 + output 扩展 + Experience 写入 |
| Step 2 | M20 | 1 天 | Experience 查询 API + Dashboard |
| Step 3 | M21 | 2-3 天 | Analyze Agent 可运行，蒸馏 raw experience |
| Step 4 | M22 | 1-2 天 | SkillCandidate + Policy CRUD + 审批 |
| Step 5 | M23 | 2-3 天 | TaskProfiler + PolicyEngine 运行时生效 |

---

## 10. 验收总标准

Phase 3B 完成后，以下场景应可端到端运行：

1. **经验内化**：Agent 完成任务后调用 feedback → Experience 表有 raw 记录
2. **推理捕获**：Audit Agent 审核时输出 pattern_observed → Experience 表有 audit_observation
3. **经验蒸馏**：Analyze Agent 定时运行 → raw experience → distilled → SkillCandidate
4. **策略生效**：人类审批 Skill → 生成 Policy → 新任务自动匹配 → prompt 注入防错提示
5. **效果可量化**：Policy.hit_count 和 success_rate 可查询，首次通过率有提升

---

## 11. 核心指标

| 指标 | 计算方式 | Phase 3B 目标 |
|------|---------|-------------|
| 首次通过率 | L0 / (L0+L1+L2) | > 70% |
| 人工介入率 | 需人类操作 / 总操作 | < 20% |
| 技能命中率 | 命中 Policy 的 session / 总 session | > 30% |
| 技能有效率 | 命中后 L0 / 命中后总数 | > 基线 +10% |
| Experience 覆盖率 | 有 feedback 的 task / 总完成 task | > 60% |

---

> Phase 3B 完成后，A3C 将成为"从历史中学习、越跑越好"的自进化平台。与 Phase 3A 的自动化结合，实现**全自动 + 自演进**的 Agent 开发操作系统。
