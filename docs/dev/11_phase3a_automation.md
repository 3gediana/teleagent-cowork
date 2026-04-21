# Phase 3A：自动化 — 让系统自己跑起来

> **目标**: 人类只提需求，系统自动执行、审核、合并  
> **前提**: Phase 1 + Phase 2 已完成，Change 审核流和 PR 评审流已验证  
> **核心改变**: 从"人类在多个页面间跳转操作"到"人类在一个对话口完成一切"

---

## 1. 当前问题

| 问题 | 说明 |
|------|------|
| 人类要在多个页面间跳转 | 看板改方向 → 任务页创建 → PR页审批 → 里程碑页切换，碎片化操作 |
| 没有全局状态视图 | 人类需要自己拼凑项目全貌 |
| AutoMode 下缺审批决策者 | PR 到审批节点没人决策 |
| Session 不持久化 | 自动化运行出问题时无法追溯 |
| Agent 失败无重试 | Evaluate/Merge Agent 失败后 PR 卡住 |
| 心跳超时不释放 branch | Agent 断线后 branch occupant 不释放 |

---

## 2. 里程碑划分

```
M12 Session持久化 + ToolCallTrace ──┐
M13 FailureMode + TaskTag ──────────┤──→ M15 AutoMode审批决策 ──→ M17 Chief Agent
M14 心跳超时资源释放 ──────────────┤
M16 重试机制 ──────────────────────┘
```

---

## 3. M12: Session 持久化 + ToolCallTrace

### 3.1 目标

Agent 执行过程可追溯，重启后历史 Session 可查。

### 3.2 数据模型

#### AgentSession

```sql
CREATE TABLE agent_session (
    id VARCHAR(64) PRIMARY KEY,
    role VARCHAR(32) NOT NULL,
    project_id VARCHAR(64) NOT NULL,
    change_id VARCHAR(64) DEFAULT '',
    pr_id VARCHAR(64) DEFAULT '',
    trigger_reason VARCHAR(64) DEFAULT '',
    status VARCHAR(20) NOT NULL DEFAULT 'pending',  -- pending/running/completed/failed
    model_provider VARCHAR(64) DEFAULT '',
    model_id VARCHAR(128) DEFAULT '',
    opencode_session_id VARCHAR(128) DEFAULT '',
    output TEXT,
    prompt_hash VARCHAR(64) DEFAULT '',
    duration_ms INT DEFAULT 0,
    created_at DATETIME NOT NULL,
    completed_at DATETIME,
    INDEX idx_role (role),
    INDEX idx_project (project_id),
    INDEX idx_status (status),
    INDEX idx_created (created_at)
);
```

#### ToolCallTrace

```sql
CREATE TABLE tool_call_trace (
    id VARCHAR(64) PRIMARY KEY,
    session_id VARCHAR(64) NOT NULL,
    project_id VARCHAR(64) NOT NULL,
    tool_name VARCHAR(32) NOT NULL,
    args JSON,
    result_summary TEXT,       -- handler 返回结果摘要，最多 500 字
    success BOOLEAN NOT NULL,
    created_at DATETIME NOT NULL,
    INDEX idx_session (session_id),
    INDEX idx_project (project_id),
    INDEX idx_tool (tool_name),
    INDEX idx_created (created_at)
);
```

### 3.3 改动清单

| 文件 | 改动 | 验收标准 |
|------|------|---------|
| `model/agent_session.go` | 新建，定义 AgentSession 模型 + AutoMigrate | 表创建成功 |
| `model/tool_call_trace.go` | 新建，定义 ToolCallTrace 模型 + AutoMigrate | 表创建成功 |
| `model/init.go` | 在 InitDB 中注册新模型的 AutoMigrate | 启动时自动建表 |
| `agent/manager.go` | `CreateSession` / `RegisterSession` 同时写 DB；`UpdateSessionOutput` / `MarkSessionFailed` 同步更新 DB；`GetSession` 先查内存 miss 查 DB | 重启后 GetSession 能查到历史 |
| `opencode/scheduler.go` | `runAgentViaServe` 完成时记录 `DurationMs` + `CompletedAt`；`processToolCall` 后异步写 ToolCallTrace | ToolCallTrace 表有数据 |

### 3.4 验收 E2E

1. 启动后端，触发一个 Audit Session
2. 重启后端
3. 通过 `/api/v1/internal/agent/sessions` 查到该 Session
4. ToolCallTrace 表中有 audit_output 的调用记录

---

## 4. M13: FailureMode + TaskTag

### 4.1 目标

Change 审核结果自动标注失败模式，任务支持场景标签。

### 4.2 数据模型

#### Change 新增字段

```sql
ALTER TABLE `change` ADD COLUMN failure_mode VARCHAR(64) DEFAULT '';
ALTER TABLE `change` ADD COLUMN retry_count INT DEFAULT 0;
```

#### TaskTag

```sql
CREATE TABLE task_tag (
    id VARCHAR(64) PRIMARY KEY,
    task_id VARCHAR(64) NOT NULL,
    tag VARCHAR(64) NOT NULL,
    source VARCHAR(20) NOT NULL DEFAULT 'human',  -- human/auto/chief
    created_at DATETIME NOT NULL,
    INDEX idx_task (task_id),
    INDEX idx_tag (tag),
    UNIQUE idx_task_tag (task_id, tag)
);
```

### 4.3 FailureMode 自动标注规则

| 审核结果 | 触发条件 | 标注 FailureMode |
|---------|---------|-----------------|
| L1 | issues 中有 type="wrong_assumption" | `wrong_assumption` |
| L1 | issues 中有 type="missing_context" | `missing_context` |
| L1 | issues 中有 type="tool_misuse" | `tool_misuse` |
| L1 | issues 中有 type="over_edit" | `over_edit` |
| L1 | issues 中有 type="invalid_output" | `invalid_output` |
| Fix → reject | Fix Agent 拒绝修复 | `incomplete_fix` |
| Fix → delegate + Audit2 → reject | 误判后终审仍拒绝 | `incomplete_fix` |

### 4.4 改动清单

| 文件 | 改动 | 验收标准 |
|------|------|---------|
| `model/models.go` | Change 加 `FailureMode` + `RetryCount` 字段 | 字段存在 |
| `model/task_tag.go` | 新建，定义 TaskTag 模型 + AutoMigrate | 表创建成功 |
| `model/init.go` | 注册 TaskTag AutoMigrate | 启动时建表 |
| `service/audit.go` | `ProcessAuditOutput` L1 时根据 issues 类型标 FailureMode | L1 change 有 failure_mode 值 |
| `service/audit.go` | `ProcessFixOutput` fix 失败时标 FailureMode | fix reject 有 failure_mode |
| `handler/change.go` | 同一 task 重复提交时 RetryCount++ | 重试 change 有 retry_count > 0 |
| `handler/task.go` | Create/Claim 时接受 `tags` 参数 | 标签写入 task_tag 表 |
| `agent/tools.go` | `create_task` 工具加 `tags` 参数 | Maintain Agent 可创建带标签的任务 |
| `service/tool_handler.go` | `handleCreateTask` 处理 tags | 标签写入 task_tag 表 |
| `repo/task.go` | 新增 `GetTagsByTask` / `AddTaskTag` 方法 | 查询/写入正常 |

### 4.5 验收 E2E

1. 提交一个会被 L1 审核的 change
2. 审核后 change.failure_mode 不为空
3. 同一 task 重新提交，retry_count = 1
4. 创建任务时传 tags: ["bugfix", "backend"]
5. 查询 task_tag 表有对应记录

---

## 5. M14: 心跳超时资源释放

### 5.1 目标

Agent 断线后自动释放所有占用资源（filelock + branch occupant + claimed task）。

### 5.2 当前状态

`service/scheduler.go` 的 `StartHeartbeatChecker` 已实现：
- 5 分钟无心跳 → Agent 状态变 offline
- 释放 filelock（released_at = now）
- 释放 claimed task（status = pending, assignee_id = nil）

**缺失**：未释放 branch occupant。

### 5.3 改动清单

| 文件 | 改动 | 验收标准 |
|------|------|---------|
| `service/scheduler.go` | `StartHeartbeatChecker` 事务中增加释放 branch occupant | Agent 断线后 branch.occupant_id = nil |
| `handler/branch.go` | 新增 `ForceLeave` 方法（管理员强制释放） | 可通过 API 强制释放 |

### 5.4 释放逻辑

```go
// 在 StartHeartbeatChecker 的事务中增加：
if err := tx.Model(&model.Branch{}).
    Where("occupant_id = ? AND status = 'active'", a.ID).
    Update("occupant_id", nil).Error; err != nil {
    return err
}
```

### 5.5 验收 E2E

1. Agent 进入 branch
2. 停止 Agent 心跳
3. 5 分钟后 branch.occupant_id = nil
4. 其他 Agent 可进入该 branch

---

## 6. M15: AutoMode 审批决策

### 6.1 目标

复用现有 `AutoMode` 开关。AutoMode=true 时，PR 审批节点由 Chief Agent 决策（替代人类点按钮）。

### 6.2 现有 AutoMode

当前 `Project.AutoMode` 只控制 Change 提交后是否自动进审核：
- `true`：Change 自动进入审核流
- `false`：Change 需人类确认后才审核

**扩展**：AutoMode=true 时，PR 审批节点也自动处理。

### 6.3 AutoMode=true 时的审批流程

| 节点 | AutoMode=false | AutoMode=true |
|------|---------------|---------------|
| Change 提交 | 需人类确认 → 审核流 | 自动进审核流（现有） |
| PR approve_review | 需人类点击 | Chief Agent 决策 |
| PR approve_merge | 需人类点击 | Chief Agent 决策 |
| 里程碑切换 | 需人类操作 | Chief Agent 可建议/执行 |

### 6.4 改动清单

| 文件 | 改动 | 验收标准 |
|------|------|---------|
| `handler/pr.go` | `ApproveReview` 检查 AutoMode，true 时触发 Chief Agent 决策 | AutoMode 下 PR 自动进入 evaluating |
| `handler/pr.go` | `ApproveMerge` 检查 AutoMode，true 时触发 Chief Agent 决策 | AutoMode 下 PR 自动合并 |
| `service/maintain.go` | 新增 `TriggerChiefDecision` 函数 | 可触发 Chief 做审批决策 |

### 6.5 审批决策触发逻辑

```go
// handler/pr.go - PR 审批节点
func autoApproveIfNeeded(pr *model.PullRequest, project *model.Project) {
    if !project.AutoMode {
        return // 等人类
    }
    // AutoMode=true: 触发 Chief Agent 做审批决策
    go service.TriggerChiefDecision(pr.ProjectID, "pr_approval", prContext)
}
```

**注意**：Chief Agent 做审批决策时，看到的信息和人类一样（PR diff、tech_review、biz_review），用的也是同样的审批 API。只是由 AI 替代人类点按钮。

### 6.6 验收 E2E

1. 设置项目 AutoMode = true
2. 提交 PR
3. PR 到审批节点时，Chief Agent 自动决策
4. 审批通过 → PR 自动合并
5. 审批拒绝 → PR 被关闭，通知提交者

---

## 7. M16: 重试机制

### 7.1 目标

Agent 失败后自动重试，不卡住流程。

### 7.2 重试策略

| Agent | 失败场景 | 重试策略 |
|-------|---------|---------|
| Evaluate | OpenCode serve 连接失败 | 最多重试 2 次，间隔 30s |
| Evaluate | evaluate_output 返回 needs_work | 不重试，通知 Agent 修改 |
| Evaluate | evaluate_output 返回 conflicts | 不重试，通知人类 |
| Merge | Merge 失败（简单冲突） | 最多重试 1 次 |
| Merge | Merge 失败（复杂冲突） | 不重试，通知人类 |
| Audit1 | Session 超时 | 最多重试 1 次 |
| Fix | Session 超时 | 最多重试 1 次 |
| Maintain | Session 超时 | 不重试，等下次定时触发 |

### 7.3 改动清单

| 文件 | 改动 | 验收标准 |
|------|------|---------|
| `opencode/scheduler.go` | `Dispatch` 返回 error 时记录到 AgentSession | 失败 session 有记录 |
| `service/pr_agent.go` | `TriggerEvaluateAgent` 加重试逻辑 | serve 连接失败时自动重试 |
| `service/audit.go` | `StartAuditWorkflow` 加重试逻辑 | audit 超时后自动重试 |
| `service/maintain.go` | `TriggerMaintainAgent` 失败时广播通知 | 失败时 Dashboard 有提示 |
| `model/agent_session.go` | 新增 `RetryCount` + `LastError` 字段 | 重试次数可查 |

### 7.4 验收 E2E

1. 关闭 OpenCode serve
2. 提交 change 触发 Audit
3. Audit 失败后自动重试
4. AgentSession 记录 retry_count = 1
5. 2 次都失败后，Session 状态 = failed，Dashboard 有通知

---

## 8. M17: Chief Agent — 平台语音界面

### 8.1 定位

**Chief Agent 是平台的"语音界面"**——把分散在多个页面的 UI 操作收拢到一个对话口。

| 没有 Chief | 有 Chief |
|-----------|--------|
| 人类在看板改方向 → 任务页创建 → PR页审批 → 里程碑页切换 | 人类只在一个对话口 |
| 人类自己拼凑项目全貌 | Chief 对平台全局一清二楚 |
| AutoMode=false 时人类逐个点按钮 | AutoMode=true 时 Chief 替人类决策 |

### 8.2 和 Maintain Agent 的差异

| | Maintain Agent | Chief Agent |
|---|---|---|
| **服务对象** | 项目（自动维护项目状态） | 人类（替人类操作平台） |
| **交互方式** | 人类在看板上写信息，Maintain 读取执行 | 人类直接跟 Chief 说话，Chief 执行 |
| **主动性** | 定时巡检，自动补缺 | 被动响应人类，不主动改东西 |
| **信息输出** | 通过工具修改项目状态 | 向人类汇报项目状态 + 执行人类指令 |
| **全局认知** | 只看当前触发相关的上下文 | 对平台全局有完整认知 |
| **同样工具的动机** | create_task 因为发现任务空了 | create_task 因为人类说"我要做登录" |

**核心差异**：Maintain 是规则驱动的执行者（管家），Chief 是人类驱动的操作代理（管家+翻译）。
Chief 最核心的能力是"说"——把平台状态翻译成人类能理解的话。

### 8.3 角色定义

```go
RoleChief Role = "chief"
```

| 属性 | 值 |
|------|-----|
| Name | Chief Agent |
| Description | Platform voice interface: reports global status, executes human instructions, makes approval decisions in AutoMode |
| PromptTemplate | chief.md |
| PlatformTools | create_task, delete_task, update_milestone, propose_direction, write_milestone, approve_pr, reject_pr, switch_milestone, chief_output |
| OpenCodeTools | read, glob |
| Model | 人类通过 RoleOverride 自行配置（建议用高级模型） |

### 8.4 工具定义

#### 复用 Maintain 的工具

| 工具 | 说明 |
|------|------|
| `create_task` | 创建任务（带 tags） |
| `delete_task` | 删除任务 |
| `update_milestone` | 更新里程碑 |
| `write_milestone` | 写里程碑 |
| `propose_direction` | 提议方向 |

#### 新增工具

| 工具 | 说明 |
|------|------|
| `approve_pr` | 审批通过 PR（AutoMode 下替代人类点击） |
| `reject_pr` | 拒绝 PR |
| `switch_milestone` | 切换里程碑 |
| `chief_output` | 输出会话结果 |

#### approve_pr

```typescript
tool({
  name: "approve_pr",
  description: "Approve a PR (review or merge). Use this in AutoMode to replace human approval.",
  parameters: z.object({
    pr_id: z.string().describe("PR ID"),
    action: z.enum(["approve_review", "approve_merge"]).describe("Which approval step"),
    reason: z.string().describe("Why you approve this")
  })
})
```

#### reject_pr

```typescript
tool({
  name: "reject_pr",
  description: "Reject a PR with reason",
  parameters: z.object({
    pr_id: z.string().describe("PR ID"),
    reason: z.string().describe("Why you reject this")
  })
})
```

#### switch_milestone

```typescript
tool({
  name: "switch_milestone",
  description: "Switch to a different milestone",
  parameters: z.object({
    milestone_id: z.string().describe("Target milestone ID"),
    reason: z.string().describe("Why switching")
  })
})
```

#### chief_output

```typescript
tool({
  name: "chief_output",
  description: "Output session result",
  parameters: z.object({
    result: z.string().describe("Result: reported / executed / approved / rejected / needs_clarification"),
    summary: z.string().describe("What you did or reported")
  })
})
```

### 8.5 全局状态快照（核心）

Chief 最核心的能力是"说"——需要完整的平台状态作为上下文：

```markdown
## 平台全局状态

### 项目
- 方向: {{.DirectionBlock}}
- 当前里程碑: {{.MilestoneBlock}}
- 版本: {{.Version}}
- AutoMode: {{.AutoMode}}

### 任务概览
- 待领取: {{.PendingTaskCount}} 个
- 进行中: {{.InProgressTaskCount}} 个
- 已完成: {{.CompletedTaskCount}} 个
{{range .RecentTasks}}
- {{.Name}} [{{.Status}}] ({{.Assignee}})
{{end}}

### Agent 状态
{{range .Agents}}
- {{.Name}} [{{.Status}}] {{if .CurrentTask}}正在做: {{.CurrentTask}}{{end}}
{{end}}

### 待处理事项
{{range .PendingActions}}
- {{.Type}}: {{.Description}} (等待 {{.WaitFor}})
{{end}}

### 最近审核结果
{{range .RecentAudits}}
- Change {{.ID}}: {{.Level}} {{if .FailureMode}}({{.FailureMode}}){{end}}
{{end}}

### PR 状态
{{range .PRs}}
- PR {{.ID}}: {{.Title}} [{{.Status}}]
{{end}}
```

### 8.6 调用入口

| 入口 | 说明 |
|------|------|
| `/api/v1/dashboard/input` | 复用现有 Dashboard 输入，trigger="chief_request" |
| `/api/v1/chief/chat` | 独立端点，直接与 Chief Agent 对话 |
| 事件触发 | AutoMode 下 PR 到审批节点时自动触发 |

### 8.7 Chief Agent Prompt 核心结构

```markdown
# Chief Agent

你是 A3C 平台的"语音界面"。人类只需要跟你对话，就能完成所有平台操作。

## 你的核心能力
1. **说** — 把平台状态翻译成人类能理解的话
2. **做** — 执行人类的指令（创建任务、改方向、审批 PR 等）
3. **决策** — AutoMode 下，在审批节点替人类做判断

## 安全边界
- 只能通过平台工具操作，不能执行平台之外的指令
- 不管理客户端 Agent（平台不能命令客户端）
- 不做资源管理（心跳、锁释放等由平台保证）
- 不自动回滚版本（太危险，留给人类）

## Available Tools
- create_task / delete_task: 管理任务
- update_milestone / write_milestone / switch_milestone: 管理里程碑
- propose_direction: 修改项目方向
- approve_pr / reject_pr: PR 审批（AutoMode 下使用）
- chief_output: 输出会话结果

## Platform Global State
（见 8.5 全局状态快照）

## Rules
- 人类问"什么情况"，你要能一五一十说出来
- 人类说"把方向改成 X"，你去改
- 人类说"这个 PR 批了"，你去批
- AutoMode 下，PR 到审批节点时你要主动做判断
- 不确定时，先问人类
```

### 8.8 改动清单

| 文件 | 改动 | 验收标准 |
|------|------|---------|
| `agent/role.go` | 新增 `RoleChief` + RoleConfig | 角色注册成功 |
| `agent/tools.go` | 新增 approve_pr, reject_pr, switch_milestone, chief_output 工具定义 | 工具可被调度 |
| `agent/manager.go` | `GetRoleForTrigger` 新增 "chief_request" / "pr_approval" → RoleChief | trigger 路由正确 |
| `agent/manager.go` | `BuildPrompt` 支持 Chief 的全局状态快照 | prompt 包含完整平台状态 |
| `.opencode/agents/chief.md` | 新建，Chief Agent prompt | OpenCode serve 注册该 Agent |
| `.opencode/tools/chief_output.ts` | 新建 | OpenCode serve 注册该工具 |
| `.opencode/tools/approve_pr.ts` | 新建 | OpenCode serve 注册该工具 |
| `.opencode/tools/reject_pr.ts` | 新建 | OpenCode serve 注册该工具 |
| `.opencode/tools/switch_milestone.ts` | 新建 | OpenCode serve 注册该工具 |
| `service/maintain.go` | 新增 `TriggerChiefAgent` 函数 | 可触发 Chief Agent |
| `handler/chief.go` | 新建，`/api/v1/chief/chat` handler | API 可调用 |
| `handler/dashboard.go` | `Input` 支持 trigger="chief_request" | Dashboard 可触发 Chief |
| `cmd/server/main.go` | 注册 chief 路由 | 路由可访问 |
| `service/tool_handler.go` | 新增 handleApprovePR, handleRejectPR, handleSwitchMilestone | 工具调用可处理 |

### 8.9 SessionContext 扩展

```go
// agent/manager.go - SessionContext 新增字段
type SessionContext struct {
    // ... 现有字段 ...
    GlobalState   string // 平台全局状态快照
    AutoMode      bool   // 项目 AutoMode 开关
}
```

### 8.10 验收 E2E

1. 通过 Dashboard 输入："现在什么情况"
2. Chief Agent 完整汇报：方向、里程碑进度、任务状态、Agent 状态、待处理 PR
3. 人类说："创建一个登录功能的任务"
4. Chief 创建任务（带 tags: feature, backend）
5. AutoMode=true 时，PR 到审批节点，Chief 自动决策
6. 人类说："这个 PR 不要了"
7. Chief 调用 reject_pr 关闭 PR

---

## 9. API 新增清单

| 路径 | 方法 | 说明 | 认证 |
|------|------|------|------|
| `/api/v1/chief/chat` | POST | 与 Chief Agent 对话 | Bearer |
| `/api/v1/task/:task_id/tags` | GET | 获取任务标签 | Bearer |
| `/api/v1/task/:task_id/tags` | POST | 添加任务标签 | Bearer |
| `/api/v1/branch/:branch_id/force_leave` | POST | 强制释放 branch | Bearer |
| `/api/v1/internal/agent/chief_output` | POST | Chief Agent 结果回调 | Internal |
| `/api/v1/internal/agent/approve_pr` | POST | Chief PR 审批回调 | Internal |
| `/api/v1/internal/agent/reject_pr` | POST | Chief PR 拒绝回调 | Internal |
| `/api/v1/internal/agent/switch_milestone` | POST | Chief 里程碑切换回调 | Internal |
| `/api/v1/internal/agent/session/:session_id` | GET | 查询 Session（含持久化数据） | Internal |

---

## 10. 开发顺序

```
M12 Session持久化 ────────────┐
M13 FailureMode + TaskTag ────┤──→ M15 AutoMode审批决策 ──→ M17 Chief Agent
M14 心跳资源释放 ────────────┤
M16 重试机制 ────────────────┘
```

| 阶段 | 内容 | 预计 | 交付物 |
|------|------|------|--------|
| Step 1 | M12 + M14 | 1-2 天 | Session 可追溯 + 资源自动释放 |
| Step 2 | M13 | 1 天 | FailureMode 自动标注 + TaskTag |
| Step 3 | M15 + M16 | 1-2 天 | AutoMode 审批决策 + 重试机制 |
| Step 4 | M17 | 2-3 天 | Chief Agent 可对话 + 全局状态汇报 + 审批决策 |

---

## 11. 验收总标准

Phase 3A 完成后，以下场景应可端到端运行：

1. **Chief Agent 全局汇报**：人类问"什么情况" → Chief 完整汇报方向/里程碑/任务/Agent/PR 状态
2. **Chief Agent 执行指令**：人类说"创建任务"/"改方向"/"批PR" → Chief 通过平台工具执行
3. **AutoMode 审批决策**：AutoMode=true → PR 到审批节点 → Chief 自动决策 → 批准/拒绝
4. **故障自恢复**：Agent 断线 → 5 分钟后资源释放 → 任务重置为 pending → 其他 Agent 可领取
5. **审核失败自标注**：L1 change → FailureMode 自动填充 → 重试 change → RetryCount 递增
6. **可追溯**：任何 Session 可通过 API 查询历史，ToolCallTrace 可查工具调用链

---

> Phase 3A 完成后，A3C 将成为"人类在一个对话口完成一切"的平台。Phase 3B 再在此基础上加入自演进能力。

---

## 12. 实现状态（已完成 ✅）

| 模块 | 文件 | 状态 |
|------|------|------|
| M12 AgentSession | `model/agent_session.go` | ✅ 持久化到 DB，重启安全 |
| M12 ToolCallTrace | `model/tool_call_trace.go` | ✅ 每次工具调用可追溯 |
| M12 Session 持久化 | `agent/manager.go` | ✅ Create/Register/Update/Mark 同步 DB；GetSession DB fallback |
| M12 Trace 记录 | `opencode/scheduler.go` | ✅ DurationMs + 异步 ToolCallTrace |
| M13 FailureMode | `model/models.go` | ✅ Change 新增 FailureMode + RetryCount |
| M13 TaskTag | `model/task_tag.go` | ✅ 任务分类标签 |
| M13 自动标注 | `service/audit.go` | ✅ classifyFailureMode 基于 issue type |
| M13 RetryCount | `handler/change.go` | ✅ 同 task 历史 rejected change 计数 |
| M14 心跳释放 | `service/scheduler.go` | ✅ 心跳超时释放 branch occupant |
| M15 AutoMode 决策 | `handler/pr.go` | ✅ AutoMode 触发 Chief 风险评估 |
| M15 Policy | `model/policy.go` | ✅ 策略表，RAG-ready |
| M16 重试 | `opencode/scheduler.go` | ✅ maybeRetry + per-role retry policy |
| M17 Chief 角色 | `agent/role.go` | ✅ RoleChief + 10 个平台工具 |
| M17 Chief Prompt | `agent/prompts/chief.md` | ✅ 决策流程含策略匹配 |
| M17 Chief Service | `service/chief.go` | ✅ TriggerChiefDecision/Chat + 全局状态快照 |
| M17 Chief Handlers | `service/tool_handler.go` | ✅ approve_pr/reject_pr/switch_milestone/create_policy/chief_output |
| M17 Chief OpenCode | `.opencode/agents/chief.md` + 5 tools | ✅ |
| Chief Chat API | `handler/chief.go` | ✅ Chat/Sessions/ToolTraces/Policies |
| Chief Routes | `cmd/server/main.go` | ✅ /chief/chat, /chief/sessions, /chief/traces, /chief/policies |
