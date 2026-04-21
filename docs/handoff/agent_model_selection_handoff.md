# 工作交接文档：Agent Model Selection 与 Tool Call 参数解析问题

> 创建时间：2026-04-21
> 前一对话模型：Claude (标准版)
> 问题优先级：🔴 高 - 阻塞 Agent 工作流完整测试

---

## 1. 已完成的工作 ✅

### 1.1 功能实现
- **每 Agent 独立模型选择**：Backend 支持为每个 role (audit_1, fix, audit_2, maintain 等) 配置独立的 model provider/model ID
- **前端搜索 UI**：Settings 页面已添加可搜索的模型选择器（支持 120 providers / 4234 models）
- **MCP Tool 自动路由**：`change.submit` 和 `file/sync` 已根据 agent 是否在 branch 上自动路由
- **默认模型**：全部 agent 默认使用 `minimax-coding-plan/MiniMax-M2.7`（配置在 `config.yaml`）

### 1.2 关键 Bug 修复
| 文件 | 修复内容 |
|------|----------|
| `scheduler.go:451-462` | `sendServeMessage` 的 model 字段从字符串 `"provider/model"` 改为对象 `{"providerID":"...", "modelID":"..."}` |
| `scheduler.go:529` | `processServeToolCalls` 中 tool part type 从只匹配 `"tool-invocation"` 扩展到也匹配 `"tool"` |

---

## 2. 未完成的核心问题 🔴

### 2.1 问题描述
**Audit Agent 调用 `audit_output` tool 时，input 参数为空**，导致后端无法获取 audit 结果（level: L0/L1/L2）。

**现象**：
- Audit Agent 输出文本声称 "Submitted L1" 或 "L0 - no issues"
- 但 `audit_output` tool 被调用时 `State.Input` 为空对象 `{}`
- 后端 `ProcessAuditOutput` 收到空参数，无法判断审核级别
- Fix Agent 不会被触发（L1 流程中断）

**日志证据**（来自 `scheduler.go:536` 的调试日志）：
```
[OpenCode] Tool audit_output has no state input, using empty
[OpenCode] Found part type=tool tool=audit_output in session ses_xxx
```

### 2.2 根因假设
OpenCode serve 返回的 `tool` part 结构中，tool input 参数可能不在 `state.input` 字段，而可能在：
- `metadata` 字段
- `state` 的其他子字段
- 或需要调用其他 API 获取 tool call 的 input

需要调查 OpenCode SDK 中 `ToolPart` 的实际结构和数据流向。

---

## 3. 涉及的文件 📁

### 3.1 核心问题文件
| 文件路径 | 相关函数/行 | 说明 |
|----------|-------------|------|
| `platform/backend/internal/opencode/scheduler.go:527-541` | `processServeToolCalls` | 解析 tool part，提取 input 参数 |
| `platform/backend/internal/opencode/scheduler.go:254-262` | `servePart` struct | part 结构定义，可能需要扩展字段 |
| `platform/backend/internal/service/tool_handler.go:230-236` | `ProcessAuditOutput` | 处理 audit_output tool 调用 |

### 3.2 已修改的文件（可复查）
| 文件路径 | 修改内容 |
|----------|----------|
| `platform/backend/internal/agent/role.go` | 添加 ModelProvider/ModelID 字段 + DB override 逻辑 |
| `platform/backend/internal/opencode/scheduler.go:137-157` | `Dispatch` 使用 role 级别 model 配置 |
| `platform/backend/internal/opencode/client.go:170-241` | 添加 `GetProviders()` 和 `FlattenProviders()` |
| `platform/backend/internal/handler/role.go` | 新文件：role list/update/providers APIs |
| `platform/backend/internal/model/models.go:188-197` | 添加 `RoleOverride` 模型 |
| `platform/backend/cmd/server/main.go` | 注册 role API 路由 |
| `frontend/src/pages/SettingsPage.tsx` | 添加可搜索的模型选择 UI |
| `frontend/src/api/endpoints.ts` | 添加 `roleApi` 和 `providerApi` |

---

## 4. 测试方法 🧪

### 4.1 环境准备
```powershell
# 1. 确保后端在运行
cd d:\claude-code\coai\platform\backend
.\a3c-server.exe

# 2. 检查 OpenCode serve 在 4096 端口运行
# 3. 使用默认 minimax 模型（已配置）
```

### 4.2 快速测试步骤
```powershell
# 登录
$login = curl -s -X POST http://localhost:3003/api/v1/auth/login `
  -H "Content-Type: application/json" `
  -d '{"agent_id":"agent_b156eef3","access_key":"18ad9a111b8b90a50ef471a626fdc53f"}'

# 选择项目
curl -s -X POST http://localhost:3003/api/v1/auth/select-project `
  -H "Content-Type: application/json" `
  -H "Authorization: Bearer 18ad9a111b8b90a50ef471a626fdc53f" `
  -d '{"project_id":"proj_20309d86"}'

# 创建任务
curl -s -X POST "http://localhost:3003/api/v1/task/create?project_id=proj_20309d86" `
  -H "Content-Type: application/json" `
  -H "Authorization: Bearer 18ad9a111b8b90a50ef471a626fdc53f" `
  -d '{"name":"DebugTest","description":"Debug audit tool input","priority":"high"}'

# 认领 + 提交变更（代码见 4.3）
```

### 4.3 测试用例：触发 L1 审核
创建文件 `test_submit_debug.json`：
```json
{
  "task_id": "<上一步创建的任务ID>",
  "version": "v1.3",
  "writes": [{
    "path": "buggy.py",
    "content": "def divide(a, b):\n    return a / b  # 没有除零检查\n"
  }],
  "description": "Add buggy divide function"
}
```

提交：
```bash
curl -s -X POST "http://localhost:3003/api/v1/change/submit?project_id=proj_20309d86" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer 18ad9a111b8b90a50ef471a626fdc53f" \
  -d @test_submit_debug.json
```

### 4.4 观察日志
关键日志位置：
1. `[OpenCode] Tool audit_output has ...` - 看是否有 input
2. `[Audit] Change xxx needs fix (L1)` - 看是否触发 L1 流程
3. `[OpenCode] Dispatching session ... agent=fix` - 看 Fix Agent 是否启动

---

## 5. 调试线索 🔍

### 5.1 OpenCode SDK 类型参考
从 `packages/sdk/js/src/gen/types.gen.ts`：
```typescript
export type ToolPart = {
  id: string
  sessionID: string
  messageID: string
  type: "tool"
  callID: string
  tool: string
  state: ToolState  // <-- 可能在这里
  metadata?: { [key: string]: unknown }  // <-- 或这里
}
```

`ToolState` 是 union 类型：
```typescript
ToolStatePending | ToolStateRunning | ToolStateCompleted | ToolStateError
```

`ToolStateCompleted` 可能有 `output`，但 input 可能在调用前就确定了。

### 5.2 可能的解决方案
1. **扩展 `servePart` struct**：添加 `Metadata` 和 `CallID` 字段
2. **直接读取 `part.State`**：如果 `ToolStateCompleted` 包含 `output`，可能需要用 output 而不是 input
3. **使用 `callID` 查询**：调用 OpenCode `/tool/:callID` API 获取完整 tool call 信息
4. **检查消息顺序**：OpenCode 可能在 tool 调用消息之后，再发一条消息包含结果

### 5.3 当前代码假设（可能错误）
```go
// scheduler.go:531-537 - 当前逻辑
if part.State != nil && len(part.State.Input) > 0 {
    inputRaw = part.State.Input
} else {
    inputRaw = json.RawMessage("{}")  // <-- 问题在这里！
}
```

**假设**：`audit_output` tool 的 input 参数可能根本不在 `State.Input` 中，而需要在其他地方获取。

---

## 6. 当前状态 📊

| 组件 | 状态 | 备注 |
|------|------|------|
| Model 选择 UI | ✅ 完成 | 可搜索，能保存到 DB |
| Model 配置后端 | ✅ 完成 | API 正常，Scheduler 使用 role 配置 |
| Audit Agent 启动 | ✅ 完成 | 使用 minimax，返回文本输出 |
| Tool Call 识别 | ✅ 完成 | 能识别 `audit_output` tool |
| **Tool Input 解析** | 🔴 **阻塞** | input 为空，需要修复 |
| Fix Agent 触发 | 🔴 阻塞 | 依赖 audit_output 正常工作 |
| Git 提交 | ✅ 完成 | 已 push 到 `revert-v1.3` |

---

## 7. 下一步行动建议 🎯

1. **调查 OpenCode serve 的 tool part 结构**：
   - 打印完整的 `part` JSON 看看实际字段
   - 对比 SDK 的 `ToolPart` 类型

2. **尝试替代获取 input 的方式**：
   - 检查 `metadata` 字段
   - 或检查是否需要用 `callID` 查询详情

3. **临时 workaround（如果需要快速测试）**：
   - 让 agent 在 text output 中输出 JSON，从文本解析结果（不推荐，但可验证流程）

4. **修复后测试完整流程**：
   - Audit (L1) → Fix → Audit2 (L0) → Approve

---

## 8. 相关文档 📚

- OpenCode SDK Types: `https://raw.githubusercontent.com/anomalyco/opencode/dev/packages/sdk/js/src/gen/types.gen.ts`
- OpenCode API: `GET /session/:id/message` 返回的消息结构
- Backend: `platform/backend/internal/opencode/scheduler.go` - `processServeToolCalls` 函数

---

**注意**：此文档专为高价模型快速接手设计，请优先解决 "Tool Input 为空" 问题，以解锁完整 Agent 工作流测试。
