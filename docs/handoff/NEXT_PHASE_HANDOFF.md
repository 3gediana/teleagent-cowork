# A3C 项目交接文档 — 下一阶段开发规划

> **创建时间**: 2026-04-21  
> **版本**: revert-v1.3 分支最新提交  
> **用途**: 供下一阶段强力模型在不阅读代码的情况下，全面理解项目目标、架构、当前状态，并制定合理开发规划

---

## 1. 项目定位与核心目标

### 1.1 项目全称
**A3C (Agent Collaboration Command Center)** — 多 Agent 远程协作协调平台

### 1.2 解决什么问题

在多 Agent（AI 编程助手）协作开发场景中，存在三大痛点：

| 痛点 | 说明 | A3C 解决方案 |
|------|------|-------------|
| **对齐问题** | 多个 Agent 各自为战，不了解项目整体方向和进度 | 平台作为消息中枢，主动推送项目 direction、milestone、task 状态 |
| **冲突问题** | 多个 Agent 同时修改同一文件 | 文件锁机制（5分钟 TTL）+ 分支隔离 |
| **可见性问题** | 人类无法感知 Agent 的工作状态和意图 | Web Dashboard 实时展示 Agent 活动、任务进度、代码提交 |

### 1.3 核心理念

- **方向主权归人类**：项目整体 direction 仅由人类定义，Agent 只负责执行
- **不鼓励 AI 间讨论**：由平台统一维护状态，减少 Agent 间无效沟通（"傻子共振"）
- **所有改动经过审核**：代码提交必须经过审核 Agent 判定（L0/L1/L2）
- **远程协作**：不同用户的 Agent 运行在不同电脑上，通过网络连接平台

### 1.4 协作场景示意

```
用户 A 的电脑                      用户 B 的电脑
┌──────────────┐                  ┌──────────────┐
│ Agent Alice  │                  │ Agent Bob    │
│ (OpenCode    │                  │ (OpenCode    │
│  MCP Client) │                  │  MCP Client) │
└──────┬───────┘                  └──────┬───────┘
       │         HTTP (各自网络)          │
       └────────────────┬────────────────┘
                        │
                        ▼ Internet
              ┌─────────────────────┐
              │     A3C 平台        │
              │   (部署在公网)      │
              │  - 消息中枢         │
              │  - 任务管理         │
              │  - 审核仲裁         │
              │  - 文件锁管理       │
              └─────────────────────┘
                        │
                        ▼
              ┌─────────────────────┐
              │   Web Dashboard     │
              │   (人类监控界面)    │
              └─────────────────────┘
```

---

## 2. 技术架构

### 2.1 整体架构

```
┌─────────────────────────────────────────────────────────────┐
│                        A3C 平台                              │
├─────────────────────────────────────────────────────────────┤
│  Frontend (React + TS + Vite + Tailwind + Zustand)          │
│  ├── 页面: Overview / Tasks / Submissions / Activity / PRs / Settings
│  └── 端口: 33303 (dev)                                     │
├─────────────────────────────────────────────────────────────┤
│  Backend (Go + Gin + GORM + MySQL + Redis)                  │
│  ├── HTTP API 端口: 3003                                     │
│  ├── SSE 实时推送: /api/v1/events                            │
│  ├── OpenCode Scheduler: 启动 pure serve 处理 Agent 对话    │
│  └── 数据持久化: MySQL + 项目 Git Repo (worktree)           │
├─────────────────────────────────────────────────────────────┤
│  MCP Client (@modelcontextprotocol/sdk)                    │
│  ├── TypeScript 实现，作为 OpenCode 的 MCP Server         │
│  ├── 提供 tools: a3c_platform, task, filelock, change_submit│
│  │   file_sync, status_sync, project_info, select_branch    │
│  │   branch, pr_submit, pr_list                            │
│  └── 运行在用户本地，通过 HTTP 连接 A3C 平台             │
└─────────────────────────────────────────────────────────────┘
```

### 2.2 技术栈明细

| 层 | 技术 | 说明 |
|----|------|------|
| 前端 | React 18 + TypeScript + Vite + Tailwind CSS + Zustand | SPA，6 个主要页面 |
| 后端 | Go 1.22 + Gin + GORM + MySQL 8.0 + Redis 7 | REST API + SSE |
| AI 对话 | OpenCode (本地 TUI) + 纯 serve (API 模式) | MiniMax-M2.7 默认模型 |
| MCP 客户端 | TypeScript + @modelcontextprotocol/sdk | 作为 OpenCode MCP Server |
| 部署 | Docker Compose (MySQL + Redis) | 手动启动 Go + React |

### 2.3 项目目录结构

```
coai/
├── frontend/              # React 前端
│   ├── src/pages/         # OverviewPage, TaskPage, SubmissionPage, ActivityPage, PRPage, SettingsPage
│   ├── src/components/    # Layout, ActivityStream, 等
│   ├── src/api/           # endpoints.ts (所有 API 封装)
│   └── src/stores/        # Zustand 状态管理
├── platform/
│   ├── backend/           # Go 后端
│   │   ├── cmd/server/    # main.go (入口，路由注册)
│   │   ├── internal/
│   │   │   ├── handler/   # HTTP handler (auth, task, change, branch, pr, ...)
│   │   │   ├── service/   # 业务逻辑 (audit, fix, maintain, evaluate, merge agents)
│   │   │   ├── model/     # GORM 数据模型 (11 个实体)
│   │   │   ├── agent/     # Agent 角色定义 + Session 管理
│   │   │   ├── opencode/  # OpenCode Scheduler (启动 pure serve, 会话调度)
│   │   │   ├── middleware/# Auth, CORS, RateLimit, Recovery
│   │   │   └── repo/      # Git 操作 + ContentBlock 管理
│   │   └── bin/           # 编译后的 server.exe
│   └── data/              # 运行时数据
│       ├── projects/      # 项目 Git 仓库 (main + branches/worktree)
│       ├── logs/          # 运行日志
│       └── broadcasts/    # SSE 广播队列
├── client/mcp/src/        # MCP 客户端源码
│   ├── index.ts           # MCP Server 主入口，所有 tool 定义
│   ├── api-client.ts      # 调用 A3C 后端 API
│   └── opencode-client.ts # OpenCode serve 会话管理
├── .opencode/             # OpenCode 项目配置
│   ├── agents/            # Agent 定义文件 (maintain.md, audit_1.md, fix.md, ...)
│   ├── tools/             # OpenCode 工具 (evaluate_output.ts, merge_output.ts, ...)
│   └── package.json       # @opencode-ai/plugin 依赖
├── configs/config.yaml    # 后端配置 (数据库、Redis、OpenCode)
├── docker-compose.yml     # MySQL + Redis
└── start.ps1              # Windows 一键启动脚本
```

---

## 3. 数据模型（11 个实体）

| 实体 | 核心字段 | 作用 |
|------|----------|------|
| **Project** | id, name, status(initializing/ready/idle), auto_mode | 项目容器，auto_mode 控制是否自动审核 |
| **Agent** | id, name, access_key, status(online/offline), current_project_id, current_branch_id | 用户端代理，Bearer Token 认证 |
| **Task** | id, project_id, milestone_id, status(pending/claimed/completed/deleted), assignee_id | 可领取的任务 |
| **Milestone** | id, project_id, name, status(in_progress/completed) | 项目阶段里程碑 |
| **MilestoneArchive** | milestone 归档快照 + direction_snapshot + tasks | 历史记录 |
| **FileLock** | id, project_id, branch_id, task_id, agent_id, files(JSON), expires_at | 文件锁，5分钟 TTL |
| **Change** | id, project_id, branch_id, agent_id, task_id, version, diff, status, audit_level(L0/L1/L2) | main 上的改动提交 |
| **Branch** | id, project_id, name, base_commit, base_version, status(active/merged/closed), occupant_id | 特性分支（worktree 实现） |
| **PullRequest** | id, project_id, branch_id, title, self_review, diff_stat, diff_full, status, tech_review, biz_review | PR + 多层评审 |
| **RoleOverride** | role, model_provider, model_id | 按角色覆盖 LLM 模型 |
| **ContentBlock** | project_id, block_type(direction/milestone/version), content | 项目内容块 |

### 关系图

```
Project ──< Task ──< Change
   │         │
   │         ├──> Milestone ──> MilestoneArchive
   │
   ├──< Branch ──< PullRequest
   │
   ├──< FileLock
   │
   └── ContentBlock (direction/milestone/version)
```

---

## 4. 8 个 Agent 角色

所有 Agent 通过 OpenCode pure serve 运行，由后端 Scheduler 异步调度。

| 角色常量 | 名称 | Prompt 文件 | 平台工具 | OpenCode 工具 | 职责 |
|----------|------|------------|----------|--------------|------|
| `RoleMaintain` | Maintain Agent | maintain.md | create_task, delete_task, update_milestone, propose_direction, write_milestone | read, edit, glob | 维护项目执行路径，创建任务，更新里程碑，PR 业务评审 |
| `RoleConsult` | Consult Agent | consult.md | (无) | read, glob | 只读咨询，回答项目状态问题 |
| `RoleAssess` | Assess Agent | assess.md | assess_output | read, glob | 分析导入项目结构，输出 ASSESS_DOC.md |
| `RoleAudit1` | Audit Agent 1 | audit_1.md | audit_output | read, glob | 审核 main 上的 change 提交，判定 L0/L1/L2 |
| `RoleFix` | Fix Agent | fix.md | fix_output | read, edit, glob | 验证并修复 Audit1 标记的 L1 问题 |
| `RoleAudit2` | Audit Agent 2 | audit_2.md | audit2_output | read, glob | L1 误判后的终审 |
| `RoleEvaluate` | Evaluate Agent | evaluate.md | **evaluate_output** | read, glob | **PR 技术评审**：diff 分析 + dry-run merge + 代码质量 |
| `RoleMerge` | Merge Agent | merge.md | **merge_output** | read, edit, glob | **PR 合并执行**：git merge + 简单冲突解决 |

### 模型配置
- 默认模型：`minimax-coding-plan/MiniMax-M2.7`
- 每个角色可通过 `RoleOverride` 表独立配置模型（支持 openai/anthropic/minimax 等）

---

## 5. 两大核心工作流

### 5.1 Change 审核工作流（main 上的改动）— 已验证 ✅

```
Agent 在 main 上工作
  ↓
change.submit (提交改动，含 version 检查)
  ↓
PR 状态: pending (自动模式) / pending_human_confirm (手动模式)
  ↓
Audit1 Agent 审核 → L0 (直接通过) / L1 (Fix Agent 修复) / L2 (拒绝)
  ↓
L0: 自动合并 + 完成任务 + Maintain Agent 广播更新
L1: Fix Agent 修复 → 重新审核 → 合并
L2: 拒绝，10分钟后重置任务
```

### 5.2 PR 评审工作流（分支 → main）— 完整验证 ✅

```
Agent 创建并进入分支
  ↓
分支内自由修改（branch/change_submit，无审核，自动 commit）
  ↓
pr.submit (含 self_review 必填)
  ↓
PR 状态: pending_human_review（闲置，等人类）
  ↓
人类在 Dashboard 查看 PR 摘要 + self_review，点击"同意评估"
  ↓
PR 状态: evaluating
  ↓
Evaluate Agent 技术评估（diff 分析 + dry-run merge + 代码审查）
  → evaluate_output: result=approved/needs_work/conflicts/high_risk
  ↓
PR 状态: evaluated + tech_review JSON
  ↓ (if approved)
Maintain Agent 业务评估（里程碑完成度 + 方向一致性 + 版本建议）
  → biz_review_output: result=approved/rejected/needs_changes
  ↓
PR 状态: pending_human_merge + biz_review JSON
  ↓
人类查看两份评估报告，点击"确认合并"
  ↓
ExecuteMerge 同步执行 git merge
  → 成功: 版本升级 + 分支关闭 + PR merged
  → 失败: PR merge_failed，通知人类
```

> **注意**: 当前 approve_merge 直接调用 `ExecuteMerge` 同步执行 git merge，不经过 Merge Agent。简单合并场景下这是合理的。复杂冲突场景才需要 Merge Agent 介入。

---

## 6. API 路由总览

### 6.1 公开 API（无需认证）

| 路径 | 方法 | 说明 |
|------|------|------|
| `/health` | GET | 健康检查 |
| `/api/v1/auth/login` | POST | Agent 登录（access_key） |
| `/api/v1/auth/logout` | POST | 登出 |
| `/api/v1/agent/register` | POST | 注册新 Agent |
| `/api/v1/project/create` | POST | 创建项目 |
| `/api/v1/project/:id` | GET | 获取项目详情 |
| `/api/v1/project/list` | GET | 项目列表 |

### 6.2 认证 API（Bearer Token）

| 路径 | 方法 | 说明 |
|------|------|------|
| `/api/v1/auth/heartbeat` | POST | 心跳 |
| `/api/v1/auth/select-project` | POST | 选择项目 |
| `/api/v1/task/*` | - | 创建/领取/完成/删除/列表 |
| `/api/v1/filelock/*` | - | 申请/释放/续期/检查 |
| `/api/v1/change/*` | - | 提交/列表/审核/批准 |
| `/api/v1/file/sync` | POST | 文件同步 |
| `/api/v1/status/*` | - | 状态同步/轮询 |
| `/api/v1/events` | GET | SSE 实时事件流 |
| `/api/v1/dashboard/*` | - | 状态/输入/确认/消息 |
| `/api/v1/project/info` | POST | 咨询项目信息 |
| `/api/v1/project/auto_mode` | POST | 切换自动/手动模式 |
| `/api/v1/milestone/*` | - | 切换/归档 |
| `/api/v1/version/*` | - | 回滚/列表 |
| `/api/v1/branch/*` | - | 创建/进入/离开/列表/关闭/sync_main/change_submit/file_sync |
| `/api/v1/role/*` | - | 列表/更新模型 |
| `/api/v1/opencode/providers` | GET | 模型提供商列表 |
| `/api/v1/pr/*` | - | 提交/列表/获取/approve_review/approve_merge/reject |

### 6.3 内部 API（无需认证，Scheduler 调用）

| 路径 | 方法 | 说明 |
|------|------|------|
| `/api/v1/internal/agent/audit_output` | POST | Audit1 结果回调 |
| `/api/v1/internal/agent/fix_output` | POST | Fix 结果回调 |
| `/api/v1/internal/agent/audit2_output` | POST | Audit2 结果回调 |
| `/api/v1/internal/agent/session/*` | GET/POST | Session 管理 |
| `/api/v1/internal/project/:id/import-assess` | POST | 项目导入评估 |
| `/api/v1/internal/git/*` | POST | diff/commit/revert/push/add-remote |

---

## 7. MCP 客户端工具清单

MCP 客户端运行在用户本地，作为 OpenCode 的 MCP Server 连接 A3C 平台。

### 7.1 已有工具（Phase 1）

| 工具 | 功能 |
|------|------|
| `a3c_platform` | 登录/登出平台 |
| `select_project` | 选择项目（返回项目上下文 + 分支列表） |
| `task` | 领取/完成任务 |
| `filelock` | 锁定/解锁文件（自动限定到当前分支） |
| `change_submit` | 提交 main 上的改动（含 version 检查 + 审核流程） |
| `file_sync` | 同步文件到本地 |
| `status_sync` | 获取当前项目状态（任务/锁/方向） |
| `project_info` | 咨询项目信息 |

### 7.2 Phase 2 新增工具

| 工具 | 功能 |
|------|------|
| `select_branch` | 进入特性分支 |
| `branch` | 分支操作：create/leave/list/close/sync_main |
| `pr_submit` | 提交 PR（需 self_review） |
| `pr_list` | 列出项目 PR |

---

## 8. 前端页面

| 页面 | 路径 | 功能 |
|------|------|------|
| Overview | `/` | 项目概览、方向、里程碑、实时活动流 |
| Tasks | `/tasks` | 任务列表、领取状态 |
| Submissions | `/submissions` | 改动提交列表、审核状态 |
| Activity | `/activity` | Agent 活动日志 |
| PRs | `/prs` | PR 列表、评审状态、人类审批按钮 |
| Settings | `/settings` | 项目设置、Agent 模型配置 |

---

## 9. 当前状态（截至 2026-04-21）

### 9.1 已完成 ✅

| 模块 | 状态 | 说明 |
|------|------|------|
| 后端基础架构 | ✅ | Go + Gin + GORM + MySQL + Redis 全部就绪 |
| 认证系统 | ✅ | Agent 注册/登录/心跳/项目选择 |
| 任务系统 | ✅ | 创建/领取/完成/删除 |
| 文件锁 | ✅ | 申请/释放/续期/检查，5分钟 TTL |
| Change 审核 | ✅ | 提交 → Audit1 → Fix → Audit2 → 合并/拒绝 |
| Maintain Agent | ✅ | 创建任务、更新里程碑、广播更新 |
| Dashboard | ✅ | 状态查看、人类输入/确认、消息历史 |
| SSE 实时推送 | ✅ | Agent 文本输出、工具调用、广播事件 |
| 项目导入评估 | ✅ | Assess Agent 分析项目结构 |
| Git 版本管理 | ✅ | Commit、Tag、Rollback、Diff |
| 分支系统 | ✅ | 创建/进入/离开/关闭/sync_main，worktree 实现 |
| PR 提交 | ✅ | Agent 在分支内提交 PR（含 self_review） |
| PR 技术评审 | ✅ | Evaluate Agent → evaluate_output 工具 → 后端处理 ✅ **已验证** |
| PR 业务评审 | ✅ | Maintain Agent → biz_review_output 工具 → 后端处理 ✅ **新增并验证** |
| PR 合并 | ✅ | approve_merge → ExecuteMerge 同步合并 ✅ **已验证** |
| 分支内自由修改 | ✅ | branch/change_submit API + branch/file_sync ✅ **新增** |
| MCP 客户端 | ✅ | 所有 Phase 1 + Phase 2 工具已实现 |
| 前端页面 | ✅ | 6 个页面全部可用，PR 页面含审批按钮 |

### 9.2 已修复的关键 Bug ✅

| Bug | 影响 | 修复 |
|-----|------|------|
| `.opencode/agents/` 缺少 evaluate.md / merge.md | OpenCode serve 无法注册 Evaluate/Merge Agent | 创建了两个 agent 定义文件 |
| `.opencode/tools/` 缺少 evaluate_output.ts / merge_output.ts | Agent 无法调用输出工具 | 创建了两个工具定义文件 |
| PR Agent Session 未注册到 DefaultManager | `HandleEvaluateOutput` 无法找到 PR，PR 永远卡在 `evaluating` | 在 `TriggerEvaluateAgent` / `TriggerMergeAgent` / `TriggerMaintainBizReview` 中添加 `RegisterSession()` |
| 大小写不匹配 | LLM 返回 `"APPROVED"` 但 switch 匹配 `"approved"` | 添加 `strings.ToLower(result)` |
| LLM 返回非标准 result 值 | Evaluate Agent 返回 `"Merge feasible"` 而非 `"approved"`，导致 Maintain Agent 不触发 | 添加 result 归一化 fallback（含关键词匹配） |
| Agent prompt 未指定 result 枚举值 | LLM 自由发挥，不遵循工具参数约束 | 在 evaluate.md / merge.md / maintain.md 中添加 CRITICAL: Result Values 段落 |
| Maintain Agent 无 biz_review_output 工具 | PR 业务评审无法输出结果 | 创建 `.opencode/tools/biz_review_output.ts`，添加后端处理逻辑 |
| Maintain Agent prompt 无 PR 评审指引 | Agent 不知道如何做 PR 业务评审 | 在 maintain.md 中添加 PR Business Review 段落 + PR 信息模板变量 |
| `branch.change_submit` API 缺失 | Agent 无法在分支内写入文件 | 新增 handler + 路由，调用已有 `WriteBranchFiles` + `BranchCommit` |
| `start.ps1` 路径过时 | 脚本引用 `backend/` 而非 `platform/backend/` | 更新路径和可执行文件名 |
| README 端口/结构过时 | 端口写 3303/33303，目录结构不准确 | 更新为实际端口 3003 和目录结构 |

### 9.3 已知问题 / 待完善 🟡

| 问题 | 严重程度 | 说明 |
|------|----------|------|
| 前端 PR 审批交互 | 🟡 | PRPage 已存在，但 approve_review/approve_merge 的 UI 反馈可优化 |
| Change 审核流与 PR 流的边界 | 🟡 | 两者都涉及代码提交，需明确使用场景（main 小改动 vs 分支大改动） |
| MCP 客户端未适配 branch.change_submit | 🟡 | 后端 API 已实现，但 MCP 客户端 `index.ts` 中 branch 工具未调用新 API |
| biz_review_output 未加入 .opencode/agents/maintain.md 工具列表 | 🟡 | OpenCode agent 定义文件中需声明该工具 |

### 9.4 未开始 🔴

| 功能 | 说明 |
|------|------|
| PR 合并后的版本升级广播 | Merge 成功后需广播 VERSION_UPDATE |
| 复杂冲突处理 | 当前 Merge Agent 遇到复杂冲突直接 abort |
| Agent 心跳超时自动释放资源 | 心跳断开后应自动释放 filelock 和 branch 占用 |
| 多项目隔离测试 | 当前主要测试单项目场景 |
| 生产部署配置 | Docker 仅包含 MySQL+Redis，Go/React 需手动启动 |

---

## 10. 配置说明

`configs/config.yaml`:

```yaml
server:
  port: 3003
  mode: debug

data_dir: "D:/claude-code/coai/platform/data"

database:
  host: localhost
  port: 3306
  user: root
  password: ""
  dbname: a3c

redis:
  host: localhost
  port: 6379
  password: ""
  db: 0
  prefix: "a3c:"

git:
  repo_path: "./data/repos"

opencode:
  serve_url: "http://127.0.0.1:4096"      # 本地 OpenCode serve
  project_path: "D:\\claude-code\\coai"   # OpenCode 项目目录（.opencode/ 所在）
  default_model_provider: "minimax-coding-plan"
  default_model_id: "MiniMax-M2.7"
```

---

## 11. 开发规划建议

基于当前状态，下一阶段（可视为 Phase 2.5 或 Phase 3）建议按以下优先级开发：

### P0 — 补齐 MCP 客户端适配

1. **MCP 客户端适配 `branch/change_submit`**
   - 后端 API 已实现，MCP 客户端 `index.ts` 中 branch 工具需调用新 API
   - 当前 MCP 客户端的 `branch` 工具只支持 create/leave/list/close/sync_main

2. **OpenCode agent 定义文件更新**
   - `.opencode/agents/maintain.md` 需声明 `biz_review_output` 工具

### P1 — 稳定性与体验

3. **Agent 心跳超时自动清理**
   - 心跳断开 5 分钟后自动释放 filelock、branch occupant
   - 避免资源死锁

4. **Dashboard PR 审批体验优化**
   - PR 详情页展示 tech_review + biz_review 结构化数据
   - 审批按钮状态实时反馈（加载中/成功/失败）

5. **错误处理与重试机制**
   - Evaluate/Merge Agent 失败时自动重试或通知人类
   - OpenCode serve 连接断开的恢复逻辑

6. **PR 合并后版本升级广播**
   - Merge 成功后广播 VERSION_UPDATE
   - 当前 merge 后版本更新但未广播

### P2 — 功能扩展

7. **多模型支持增强**
   - 允许不同角色使用不同模型提供商
   - 模型切换 UI（SettingsPage 已有雏形）

8. **GitHub 集成**
   - 项目导入时自动 clone GitHub repo
   - PR 合并后自动 push 到 remote
   - 当前有 `git/add-remote` API 但未完整集成

9. **分支 sync_main 自动化**
   - Agent 在分支工作一段时间后自动提醒 sync_main
   - 减少 PR 提交时的冲突概率

### P3 — 生产准备

10. **容器化部署**
    - 后端 Dockerfile
    - 前端 Nginx 配置
    - 一键 `docker-compose up` 启动全部服务

11. **测试覆盖**
    - 单元测试（Agent 调度、工具处理、Git 操作）
    - 集成测试（完整 PR 流程）

12. **监控与日志**
    - Agent 执行成功率统计
    - LLM API 调用延迟/失败监控
    - Dashboard 性能指标

---

## 12. 关键文件速查

| 文件 | 作用 |
|------|------|
| `platform/backend/cmd/server/main.go` | 入口，所有路由注册 |
| `platform/backend/internal/model/models.go` | 全部 11 个数据模型 |
| `platform/backend/internal/agent/role.go` | 8 个 Agent 角色定义（含 biz_review_output） |
| `platform/backend/internal/service/pr_agent.go` | PR 相关 Agent 触发 + 工具处理（含 result 归一化 fallback） |
| `platform/backend/internal/service/tool_handler.go` | 所有平台工具调用的中央路由 |
| `platform/backend/internal/opencode/scheduler.go` | OpenCode serve 调度器 |
| `platform/backend/internal/handler/branch.go` | 分支 handler（含 change_submit / file_sync） |
| `platform/backend/internal/service/branch.go` | 分支 service（WriteBranchFiles / BranchCommit / ExecuteMerge） |
| `frontend/src/api/endpoints.ts` | 前端所有 API 调用 |
| `frontend/src/App.tsx` | 前端路由 |
| `client/mcp/src/index.ts` | MCP Server 所有 tool 定义 |
| `.opencode/agents/evaluate.md` | Evaluate Agent 定义（含 CRITICAL: Result Values） |
| `.opencode/agents/merge.md` | Merge Agent 定义（含 CRITICAL: Result Values） |
| `.opencode/tools/evaluate_output.ts` | Evaluate 输出工具 |
| `.opencode/tools/merge_output.ts` | Merge 输出工具 |
| `.opencode/tools/biz_review_output.ts` | Maintain Agent PR 业务评审输出工具 |
| `configs/config.yaml` | 后端配置 |

---

## 13. 启动命令

```bash
# 1. 启动基础设施
docker-compose up -d

# 2. 启动后端
cd platform/backend
go build -o bin/server.exe ./cmd/server/
./bin/server.exe

# 3. 启动前端（新终端）
cd frontend
npm install
npm run dev

# 4. 启动 MCP 客户端（用户本地）
cd client/mcp
npm install
npx ts-node src/index.ts
```

---

> **文档结束**。本交接文档基于实际代码分析编写，覆盖了项目目标、架构、数据模型、Agent 角色、工作流、API、当前状态和未来规划。下一个开发者应以此文档为起点，结合具体需求进行下一阶段开发。
