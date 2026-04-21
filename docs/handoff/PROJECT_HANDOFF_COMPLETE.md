# A3C 项目完整交接文档（代码级准确）

> 创建时间：2026-04-21  
> 基于：实际代码分析（`role.go`, `pr_agent.go`, `models.go`, `tool_handler.go`）  
> 修正：上一版遗漏了 Evaluate/Merge Agent 和完整 PR 工作流

---

## 1. 项目定位（不变）

**A3C (Agent Collaboration Command Center)** - 多 Agent 协作协调平台

- 解决对齐、冲突、可见性三大问题
- 方向主权归人类，Agent 负责执行
- 所有改动经过审核

---

## 2. 核心数据模型（models.go 完整版）

### 11 个实体

| 实体 | 关键字段 | 作用 |
|------|----------|------|
| **Project** | id, name, status, auto_mode | 项目容器 |
| **Agent** | id, name, access_key, status, current_project_id, current_branch_id, last_heartbeat | 用户端代理 |
| **Task** | id, project_id, milestone_id, status(pending/claimed/completed/deleted), assignee_id | 可领取任务 |
| **Milestone** | id, project_id, status(in_progress/completed) | 项目阶段 |
| **MilestoneArchive** | 里程碑归档快照 | 历史记录 |
| **FileLock** | id, project_id, branch_id, task_id, agent_id, files(JSON array), expires_at | 文件锁（5分钟TTL） |
| **Change** | id, project_id, branch_id, agent_id, task_id, version, diff, status, audit_level(L0/L1/L2) | 改动提交 |
| **Branch** | id, project_id, name, status(active/merged/closed), occupant_id | 特性分支 |
| **PullRequest** | id, project_id, branch_id, title, self_review, status, tech_review, biz_review | PR + 评审 |
| **RoleOverride** | role, model_provider, model_id | 角色模型配置覆盖 |
| **ContentBlock** | project_id, block_type(direction/milestone/version), content | 项目内容块 |

### 关系图

```
Project ──< Task ──< Change
   │         │
   │         ├──> Milestone
   │
   ├──< Branch ──< PullRequest
   │
   ├──< FileLock
   │
   └── ContentBlock (direction/milestone/version)
```

---

## 3. 8 个 Agent 角色（role.go 完整版）

### 角色定义

| 常量 | 名称 | Prompt文件 | 平台工具 | OpenCode工具 | 职责 |
|------|------|-----------|----------|--------------|------|
| `RoleMaintain` | Maintain Agent | maintain.md | create_task, delete_task, update_milestone, propose_direction, write_milestone | read, edit, glob | 维护项目执行路径，创建任务，更新里程碑，PR业务评审 |
| `RoleConsult` | Consult Agent | consult.md | (无) | read, glob | 回答项目状态问题，只读访问 |
| `RoleAssess` | Assess Agent | assess.md | assess_output | read, glob | 分析导入项目结构，输出ASSESS_DOC.md |
| `RoleAudit1` | Audit Agent 1 | audit_1.md | audit_output | read, glob | 审核改动提交，判定L0/L1/L2 |
| `RoleFix` | Fix Agent | fix.md | fix_output | read, edit, glob | 验证并修复L1问题 |
| `RoleAudit2` | Audit Agent 2 | audit_2.md | audit2_output | read, glob | L1误判后的终审 |
| `RoleEvaluate` | Evaluate Agent | evaluate.md | **evaluate_output** | read, glob | **PR技术评审**：diff分析+冲突检测+代码质量 |
| `RoleMerge` | Merge Agent | merge.md | **merge_output** | read, edit, glob | **PR合并执行**：git merge+简单冲突解决 |

### 角色模型配置

每个角色可通过 `RoleOverride` 表配置独立模型：
- Provider: openai/anthropic/minimax-coding-plan/...
- ModelID: gpt-4o/claude-sonnet-4-20250514/MiniMax-M2.7/...

---

## 4. 两大工作流

### 4.1 Change 审核工作流（已验证 ✅）

```
Change Submit (agent 提交改动)
    │
    ▼
┌─────────────┐
│  Audit1     │──┬──> L0 ────> 自动合并 + 完成任务
│  (审核Agent) │  │
└─────────────┘  ├──> L1 ────> Fix Agent ──> 修复 ──> 合并
    │            │
    └────────────┴──> L2 ────> 拒绝，10分钟后重置任务（无心跳/重提交）
```

**状态流转**:
- Change: `pending` → `pending_human_confirm`(手动模式) → `approved`/`rejected`
- Task: `claimed` → `completed`(L0通过后自动)

### 4.2 PR 评审工作流（代码已实现，待完整测试 🟡）

```
PR Submit (agent 提交PR)
    │
    ▼
┌─────────────┐     approved
│  Evaluate   │────────────────┬──────────> Maintain Agent
│  Agent      │                │              (业务评审)
│ (技术评审)   │                │
└─────────────┘                │                    │
    │                          │                    │ approved
    ├──> needs_work ───────────┘                    │
    │                                                 ▼
    ├──> conflicts                                    Merge Agent
    │                                                 (执行合并)
    └──> high_risk                                         │
                                                            ▼
                                                        合并完成
                                                           │
                                                    ┌─────┴─────┐
                                                    ▼           ▼
                                                  success     failed
                                                    │           │
                                               PR_MERGED    PR_MERGE_FAILED
```

**PR 状态流转**:
```
pending_human_review → evaluating → evaluated → pending_human_merge → merged/rejected/merge_failed
                              │
                              └──> Maintain(biz_review) → pending_human_merge
```

**3 个评审 Agent 分工**:
| Agent | 输入 | 输出 | 触发条件 |
|-------|------|------|----------|
| Evaluate | diff, dry-run merge, self_review | `evaluate_output`: approved/needs_work/conflicts/high_risk | PR提交后自动 |
| Maintain(biz) | PR信息 + tech_review | 业务评估（通过/拒绝） | Evaluate approved后 |
| Merge | PR信息 + merge_cost_rating + conflict_files | `merge_output`: success/failed | 人工确认合并后 |

---

## 5. API 路由（main.go 完整版）

### 公开 API
- `POST /api/v1/auth/login`
- `POST /api/v1/auth/logout`
- `POST /api/v1/agent/register`
- `POST /api/v1/project/create`
- `GET /api/v1/project/:id`
- `GET /api/v1/project/list`
- `GET /health`

### 认证 API（需 Bearer Token）

**认证/心跳**
- `POST /api/v1/auth/heartbeat` (5分钟续租)
- `POST /api/v1/auth/select-project`

**项目**
- `POST /api/v1/project/info` (Consult Agent)
- `POST /api/v1/project/auto_mode`

**任务**
- `POST /api/v1/task/create` (Maintain Agent)
- `POST /api/v1/task/claim`
- `POST /api/v1/task/complete`
- `DELETE /api/v1/task/:task_id`
- `GET /api/v1/task/list`

**文件锁**
- `POST /api/v1/filelock/acquire`
- `POST /api/v1/filelock/release`
- `POST /api/v1/filelock/renew`
- `POST /api/v1/filelock/check` (预检冲突)

**改动提交**
- `POST /api/v1/change/submit` (触发Audit1)
- `GET /api/v1/change/list`
- `POST /api/v1/change/review`
- `POST /api/v1/change/approve_for_review` (手动模式确认)

**文件/状态同步**
- `POST /api/v1/file/sync`
- `GET /api/v1/status/sync`
- `POST /api/v1/poll` (5秒轮询，拉广播+刷新心跳)
- `GET /api/v1/events` (SSE订阅)

**Dashboard**
- `GET /api/v1/dashboard/state`
- `POST /api/v1/dashboard/input` (触发Maintain)
- `POST /api/v1/dashboard/confirm`
- `GET /api/v1/dashboard/messages`
- `POST /api/v1/dashboard/clear_context`

**里程碑**
- `POST /api/v1/milestone/switch`
- `GET /api/v1/milestone/archives`

**版本**
- `POST /api/v1/version/rollback`
- `GET /api/v1/version/list`

**分支** (新增模块)
- `POST /api/v1/branch/create`
- `POST /api/v1/branch/enter` (占用分支)
- `POST /api/v1/branch/leave`
- `GET /api/v1/branch/list`
- `POST /api/v1/branch/close`
- `POST /api/v1/branch/sync_main` (合并main到分支)

**角色模型配置** (新增模块)
- `GET /api/v1/role/list`
- `POST /api/v1/role/update_model`
- `GET /api/v1/opencode/providers`

**PR** (新增模块)
- `POST /api/v1/pr/submit`
- `GET /api/v1/pr/list`
- `GET /api/v1/pr/:pr_id`
- `POST /api/v1/pr/approve_review` (批准评审)
- `POST /api/v1/pr/approve_merge` (批准合并)
- `POST /api/v1/pr/reject`

---

## 6. Agent 调度架构

### OpenCode 集成 (opencode/scheduler.go)

```
┌───────────────────────────────────────────────┐
│            OpenCode Scheduler                  │
│                                                │
│  ┌─────────────┐    ┌─────────────────────┐   │
│  │ pureServe   │───>│  opencode serve     │   │
│  │  process   │    │  Port: 15000+       │   │
│  └─────────────┘    └─────────────────────┘   │
│                              │                 │
│                              │ HTTP API       │
│                              ▼                 │
│                    ┌─────────────────┐          │
│                    │ /session/:id   │          │
│                    │ /message       │          │
│                    └─────────────────┘          │
└───────────────────────────────────────────────┘
```

**消息注入机制**:
- Agent session 创建 → 分配 OpenCode serve session ID
- Poll 收到广播 → `SendToExistingSession()` → HTTP POST 注入消息

**Agent Session 状态**:
- `pending` → `running` → `completed`/`failed`

---

## 7. MCP 客户端 (client/mcp/src/)

### 9 个 MCP 工具 (index.ts)

| 工具 | 作用 |
|------|------|
| `a3c_platform` | 登录/登出 |
| `select_project` | 选择项目，启动poller |
| `task` | 领取任务 (action=claim) |
| `filelock` | 文件锁操作 |
| `change_submit` | 提交改动 |
| `file_sync` | 文件同步 |
| `status_sync` | 状态同步 |
| `project_info` | 项目咨询 |
| `branch` | 分支操作 (create/enter/leave/close/sync_main) |

### Poller 机制 (poller.ts)

```
┌──────────────┐ 5s   ┌─────────┐
│ Poll (广播)  │─────>│ /poll   │
└──────────────┘      │ (刷新心跳)│
                      └─────────┘

┌──────────────┐ 5min ┌──────────────┐
│ Heartbeat    │─────>│ /auth/heartbeat│
│ (兜底)       │      │              │
└──────────────┘      └──────────────┘

┌──────────────┐ 5s   ┌─────────┐
│ AliveCheck   │─────>│ kill -0 │ (检测父进程)
└──────────────┘      │ (OpenCode)│
                      └─────────┘
```

---

## 8. 核心文件地图

### 后端 (platform/backend/internal/)

```
├── handler/                    # HTTP 处理器
│   ├── auth.go                 # 登录/登出/心跳
│   ├── task.go                 # 任务CRUD
│   ├── filelock.go             # 文件锁
│   ├── change.go               # 改动提交+审核触发
│   ├── sync.go                 # 同步+poll
│   ├── dashboard.go            # 看板API
│   ├── branch.go               # 分支管理 (NEW)
│   ├── pr.go                   # PR管理 (NEW)
│   ├── git.go                  # Git操作
│   ├── rollback.go             # 版本回滚
│   ├── milestone.go            # 里程碑
│   ├── role.go                 # 角色模型配置 (NEW)
│   ├── consult.go              # 咨询Agent
│   ├── agent.go                # 内部Agent API
│   └── filesync.go             # 文件同步
├── service/                    # 业务逻辑
│   ├── audit.go                # 审核工作流
│   ├── pr_agent.go             # PR Agent触发 (NEW)
│   ├── tool_handler.go         # 工具执行
│   ├── scheduler.go            # HeartbeatChecker
│   ├── maintain.go             # Maintain/Consult触发
│   ├── broadcast.go            # 广播+SSE
│   ├── branch.go               # 分支业务逻辑 (NEW)
│   └── git.go                  # Git封装
├── agent/                      # Agent角色管理
│   ├── manager.go              # Session生命周期
│   ├── role.go                 # 8个角色定义 ⭐
│   ├── session.go              # Session状态机
│   └── prompts/                # 8个Prompt文件
│       ├── maintain.md
│       ├── consult.md
│       ├── assess.md
│       ├── audit_1.md
│       ├── audit_2.md
│       ├── fix.md
│       ├── evaluate.md         # NEW
│       └── merge.md            # NEW
├── opencode/
│   └── scheduler.go            # OpenCode集成
└── model/
    └── models.go               # 11个实体 ⭐
```

### MCP (client/mcp/src/)

```
├── index.ts                    # 9个工具注册
├── poller.ts                   # 轮询+心跳
├── api-client.ts               # API客户端
├── opencode-client.ts          # Session注入
└── config.ts                   # 配置存储
```

---

## 9. 当前状态（准确版）

### ✅ 已验证通过

| 模块 | 功能 | 状态 |
|------|------|------|
| 认证 | 登录/登出/心跳/自动踢人 | ✅ 已修复并测试 |
| 任务 | 创建/领取/完成/删除 | ✅ 已测试 |
| 文件锁 | 获取/释放/续租/检查 | ✅ 已测试 |
| 改动审核 | L0自动合并/L1→Fix/L2拒绝 | ✅ 已测试 |
| Git | commit/tag/diff/rollback | ✅ 已测试 |
| SSE广播 | 实时推送 | ✅ 已测试 |
| MCP | 9个工具 | ✅ 已测试 |
| Dashboard | 看板+聊天 | ✅ 已运行 |
| 心跳 | 5分钟超时踢人 | ✅ 已测试 |
| 角色模型 | 可配置化 | ✅ 已测试 |

### 🟡 代码已实现，待完整测试

| 模块 | 功能 | 状态 |
|------|------|------|
| 分支 | 创建/进入/离开/关闭/sync_main | 🟡 代码存在 |
| PR提交 | 创建PR | 🟡 代码存在 |
| PR评审 | Evaluate→Maintain→Merge | 🟡 代码存在 |
| 里程碑 | 切换/归档 | 🟡 代码存在 |

### 🔴 已知问题

1. **OpenCode serve 资源占用** - 每个Agent spawn独立进程，可考虑共享
2. **前端UI** - 美观度待优化
3. **PR工具链** - `.opencode/tools/` 缺少 `evaluate_output.ts` 和 `merge_output.ts`

---

## 10. 下一阶段建议

### 方向 A：PR/分支系统完整测试（推荐）

1. 测试分支生命周期：创建 → 开发 → sync_main → PR → 合并 → 关闭
2. 测试 PR 工作流：Submit → Evaluate → Maintain(biz) → Merge
3. 测试冲突检测和解决

### 方向 B：OpenCode 架构优化

1. 共享 serve 实例减少资源占用
2. Session 持久化优化

### 方向 C：前端/UI

1. 看板美观度提升
2. 多轮对话界面
3. PR 评审界面

---

## 11. 快速启动

```powershell
# MySQL
D:/mysql/bin/mysqld.exe --console

# Redis
D:/redis/redis-server.exe redis.windows.conf

# 后端
cd D:/claude-code/coai/platform/backend
./a3c-server.exe

# 前端
cd D:/claude-code/coai/frontend
npm run dev
```

### 测试账号

| 字段 | 值 |
|------|-----|
| Project | `proj_20309d86` |
| Agent | `agent_b156eef3` |
| Key | `18ad9a111b8b90a50ef471a626fdc53f` |
| Backend | http://localhost:3003 |
| Frontend | http://localhost:33303 |

---

**文档结束**。本文档基于 `role.go`, `models.go`, `pr_agent.go`, `tool_handler.go` 等核心代码文件分析，准确反映项目当前状态。
