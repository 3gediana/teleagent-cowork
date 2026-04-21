# A3C 平台开发状态文档

> 生成时间：2026-04-20 18:30

---

## 一、当前正在做的事（最顶层）

**核心目标**：验证平台 agent（Maintain Agent）能否通过 dashboard 输入自动创建任务。

**当前卡点**：
- agent 独立运行时能正确调用 `create_task` 工具
- 但通过后端 scheduler 触发时，output 为空，没有工具调用
- 已改用 `--pure` + 独立 agent-config 目录禁用 MCP
- 已改用 `--file` 传递消息避免 Windows 命令行长度限制
- 需要排查为什么 scheduler 调用时 output 为空

---

## 二、本次会话完成的工作

### 2.1 广播逻辑修复
- **问题**：Login 不应该广播，只有 SelectProject 才广播
- **修复**：Login 时不启动 poller，SelectProject 时才启动
- **问题**：SelectProject 广播不应包含自己
- **修复**：改为逐个给其他 agent 发广播（排除自己），设置 `TargetAgentID`
- **问题**：广播 type 显示 "unknown"
- **修复**：MCP 读取 `msg.header?.type`（小写，匹配 Go JSON tag）

### 2.2 平台 agent 工具系统重构
- **核心问题**：平台 agent 通过 `--attach` 连接到 serve 时会加载全局 MCP 配置
- **尝试方案**：
  1. `--pure` 参数 — 不禁用用户配置的 MCP
  2. `OPENCODE_CONFIG_CONTENT` 环境变量 — 不生效
  3. 独立 agent-config 目录 + 不含 MCP 的 opencode.json — 部分生效
  4. agent 定义中 `tools: { a3c_*: false, tavily_*: false, context7_*: false }` — **生效**
- **最终方案**：
  - `opencode run --pure` 独立运行（不 attach）
  - 工作目录设为 `platform/backend/agent-config/`（无 MCP 配置）
  - agent 定义中禁用所有 MCP 工具
  - 消息通过 `--file` 参数传递（避免 Windows 命令行长度限制）
  - scheduler 中过滤已知 MCP 工具调用作为兜底

### 2.3 工具名前缀处理
- opencode 注册的工具名有前缀：`maintain_tools_delete_task`
- scheduler 的 `processToolCall` 中自动剥离 `maintain_tools_` 和 `tools_` 前缀

### 2.4 中文编码修复
- 数据库 DSN 已有 `charset=utf8mb4`
- 新增 `SET NAMES utf8mb4` 和 `ALTER TABLE ... CONVERT TO CHARACTER SET utf8mb4`
- PowerShell 写文件使用 UTF8 No BOM 编码

### 2.5 TriggerReason 修复
- 模板中 `{{.TriggerReason}}` 显示 `<no value>`
- 修复：`SessionContext` 增加 `TriggerReason` 字段，`BuildPrompt` 传入该字段

### 2.6 项目目录重组
```
D:\claude-code\coai\
├── platform/
│   ├── backend/          # Go 后端
│   │   ├── agent-config/ # 平台 agent 专用配置（无 MCP）
│   │   │   └── opencode.json
│   │   └── server.exe
│   └── data/             # 统一数据存储
├── client/
│   └── mcp/              # MCP 服务器
├── configs/
├── docs/
└── frontend/
```

---

## 三、广播系统设计

### 3.1 广播定位
- **受众**：客户端 agent（通过 MCP 连接的 opencode session）
- **目的**：对齐颗粒度，实时同步项目状态变化
- **平台 agent**：不需要广播，通过事件触发

### 3.2 保留的广播类型
| 类型 | 触发时机 | 目标 |
|------|---------|------|
| AGENT_ONLINE | SelectProject | 已在项目的其他 agent |
| DIRECTION_CHANGE | 方向确认 | 全员 |
| MILESTONE_UPDATE | 里程碑更新 | 全员 |
| MILESTONE_SWITCH | 里程碑切换 | 全员 |
| VERSION_UPDATE | 代码合并 | 全员 |
| VERSION_ROLLBACK | 代码回滚 | 全员 |

### 3.3 不广播的事件
- AUDIT_RESULT → change.submit 返回值
- CHANGE_SUBMITTED → change.submit 返回值
- LOCK_ACQUIRED → filelock 返回值
- TASK_CLAIMED/TASK_COMPLETED → status_sync 返回值

### 3.4 广播流程
```
人类操作看板/CLI → 平台更新状态 → 广播到 Redis
→ 客户端 poller 轮询（5秒） → 收到未 ack 广播
→ 注入 OpenCode session → agent 主动响应
```

---

## 四、Agent 角色与工具

### 4.1 平台 Agent（6 个角色）
| 角色 | 触发条件 | 可用工具 |
|------|---------|---------|
| Maintain | dashboard_input, milestone_complete, timer | create_task, delete_task, update_milestone, propose_direction, write_milestone |
| Audit1 | change_submitted | audit_output |
| Fix | fix_needed | fix_output |
| Audit2 | re_audit | audit2_output |
| Consult | project_info | 无平台工具 |
| Assess | project_import | assess_output |

### 4.2 客户端 Agent
- 通过 MCP 工具与平台交互
- 接收广播并注入 session
- 可调用所有 MCP 工具（a3c_*, tavily_*, context7_* 等）

### 4.3 工具实现位置
| 工具 | 实现位置 | 说明 |
|------|---------|------|
| create_task | `.opencode/tools/create_task.ts` | 返回占位符，后端捕获执行 |
| maintain_tools/* | `.opencode/tools/maintain_tools.ts` | 返回占位符，后端捕获执行 |
| audit_output | `.opencode/tools/audit_output.ts` | 返回占位符 |
| fix_output | `.opencode/tools/fix_output.ts` | 返回占位符 |
| audit2_output | `.opencode/tools/audit2_output.ts` | 返回占位符 |
| assess_output | `.opencode/tools/assess_output.ts` | 返回占位符 |

### 4.4 工具执行流程
```
平台 agent 调用工具 → 工具返回占位符
→ opencode 输出 JSON 事件 → scheduler 解析 tool_use
→ 剥离前缀 → HandleToolCallResult → 执行实际操作
→ 写入数据库
```

---

## 五、关键文件清单

### 5.1 后端核心文件
| 文件 | 作用 |
|------|------|
| `platform/backend/cmd/server/main.go` | 入口，注册路由 |
| `platform/backend/internal/handler/auth.go` | 登录/登出/选项目 |
| `platform/backend/internal/handler/dashboard.go` | 看板输入/确认 |
| `platform/backend/internal/handler/sync.go` | 状态同步/Poll handler |
| `platform/backend/internal/service/broadcast.go` | 广播核心逻辑 |
| `platform/backend/internal/service/maintain.go` | Maintain agent 触发 |
| `platform/backend/internal/service/tool_handler.go` | 工具调用处理 |
| `platform/backend/internal/opencode/scheduler.go` | Agent 调度执行 |
| `platform/backend/internal/agent/manager.go` | Session 管理 |
| `platform/backend/internal/agent/role.go` | 角色定义 |
| `platform/backend/internal/agent/tools.go` | 工具定义 |
| `platform/backend/internal/agent/prompts/maintain.md` | Maintain agent prompt |

### 5.2 工具文件
| 文件 | 作用 |
|------|------|
| `.opencode/agents/maintain.md` | Maintain agent 定义（含 tools 限制） |
| `.opencode/tools/create_task.ts` | create_task 工具（返回占位符） |
| `.opencode/tools/maintain_tools.ts` | maintain 工具集（返回占位符） |
| `.opencode/tools/audit_output.ts` | audit 工具（返回占位符） |
| `.opencode/tools/fix_output.ts` | fix 工具（返回占位符） |
| `.opencode/tools/audit2_output.ts` | audit2 工具（返回占位符） |
| `.opencode/tools/assess_output.ts` | assess 工具（返回占位符） |

### 5.3 MCP 文件
| 文件 | 作用 |
|------|------|
| `client/mcp/src/index.ts` | MCP server 入口 |
| `client/mcp/src/api-client.ts` | API 客户端 |
| `client/mcp/src/poller.ts` | 广播轮询器 |
| `client/mcp/src/opencode-client.ts` | OpenCode session 管理 |
| `client/mcp/src/config.ts` | 配置读写 |

### 5.4 配置文件
| 文件 | 作用 |
|------|------|
| `configs/config.yaml` | 后端配置（端口、数据库、Redis） |
| `platform/backend/agent-config/opencode.json` | 平台 agent 配置（无 MCP） |
| `C:\Users\sans\.config\opencode\opencode.json` | 全局 opencode 配置 |
| `C:\Users\sans\.a3c\config.json` | MCP 缓存的 access_key 和 project_id |

---

## 六、待办事项

### 6.1 紧急（当前卡点）
- [ ] **排查 scheduler 调用时 output 为空的问题**
  - agent 独立运行正常，scheduler 调用时 output 为空
  - 可能原因：`--file` 参数格式、工作目录权限、消息编码
  - 建议：在 scheduler 中打印 stderr 输出，检查 opencode 是否报错

### 6.2 高优先级
- [ ] 按 agentID 定点广播 — `TargetAgentID` 字段已加，过滤逻辑已完成
- [ ] 广播持久化到 JSON（data/broadcasts/）— 当前只存 Redis，重启丢失
- [ ] 完善 agent prompt — 确保 TriggerReason 和 InputContent 正确传递

### 6.3 中优先级
- [ ] 文件同步到本地 — 客户端拉取文件放置到 client 目录
- [ ] 审核 agent 测试 — Audit1/Fix/Audit2 流程
- [ ] 多人协作测试 — 需要多个独立 session

### 6.4 低优先级
- [ ] 版本回滚测试
- [ ] 里程碑切换测试
- [ ] Git 集成测试

---

## 七、技术决策记录

### 7.1 为什么平台 agent 不使用 MCP
- MCP 是全局配置，所有 agent 共享
- 平台 agent 直接运行在服务器上，不需要通过网络调用 API
- 工具调用通过后端捕获 JSON 输出执行，更直接可靠

### 7.2 为什么工具返回占位符
- 工具定义在 `.opencode/tools/` 下，opencode 执行时会真正调用
- 但平台 agent 不需要真正连接 API，只需输出工具调用事件
- 后端通过解析 JSON 输出中的 `tool_use` 事件执行实际操作

### 7.3 为什么使用 --pure + 独立配置
- `--pure` 禁用内置插件，但不禁用用户全局 MCP
- 独立 agent-config 目录提供不含 MCP 的 opencode.json
- agent 定义中 `tools: { a3c_*: false, ... }` 进一步限制
- 三重保障确保平台 agent 不使用 MCP

---

## 八、已知问题

1. **PowerShell JSON 编码** — 必须使用 UTF8 No BOM，否则 Go JSON 解析失败
2. **Windows 命令行长度** — 消息过长时改用 `--file` 参数
3. **数据库重启丢失** — init.go 中 DROP TABLE agent，每次重启重建
4. **Redis 广播重启丢失** — 需要持久化到 JSON 文件
5. **opencode run output 为空** — 当前卡点，需要排查
