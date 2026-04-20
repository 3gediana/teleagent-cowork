# A3C 平台开发状态文档

> 生成时间：2026-04-20 21:00

---

## 一、当前正在做的事

**核心目标**：验证 Maintain Agent 通过 dashboard 输入能正确调用 create_task 工具创建任务。

**当前卡点**：
- 使用 `--file` 参数传递消息时，opencode 报 `exit status 0xc0000409`（Windows 栈溢出）
- 短消息直接传递能正常工作，长消息（即使通过 --file）也会栈溢出
- 需要进一步排查是消息内容问题还是 opencode 的 bug

**已验证成功的**：
- 直接运行 `opencode run --pure --agent maintain ... "短消息"` 能正确调用 create_task
- 工具调用被后端正确捕获并执行（写入 TASKS.md）
- 数据库不再重启丢失

---

## 二、本次会话完成的工作

### 2.1 数据持久化改为文件存储
**目录结构**：
```
D:\claude-code\coai\platform\data\projects\{project_id}\
├── DIRECTION.md      # 方向块
├── MILESTONE.md      # 里程碑块
├── TASKS.md          # 任务列表
└── project.json      # 项目元信息
```

**已创建的测试项目数据**：
- `D:\claude-code\coai\platform\data\projects\proj_20309d86\`

### 2.2 工具处理改为文件操作
- `tool_handler.go` 重写，create_task 直接写入 TASKS.md
- delete_task 从 TASKS.md 删除对应行
- update_milestone/write_milestone 写入 MILESTONE.md

### 2.3 数据库重启不再丢失
- 移除了 `init.go` 中的 `DROP TABLE agent`
- 处理了索引迁移问题

### 2.4 平台只捕获平台工具
- `scheduler.go` 的 `processToolCall` 只处理：create_task, delete_task, update_milestone, propose_direction, write_milestone, audit_output, fix_output, audit2_output, assess_output
- 其他原生工具（read, glob, grep 等）被忽略，让 opencode 自己执行

### 2.5 Prompt 简化
- 移除了重复的上下文信息
- 移除了模板中的 `{{.InputContent}}`（改为 scheduler 拼接）
- 当前 prompt 模板：`platform/backend/internal/agent/prompts/maintain.md`

---

## 三、关键发现

### 3.1 模型行为分析
通过打印完整输出发现模型思考过程：
1. 看到上下文信息（如 "Milestone block: Milestone 1"）会尝试验证
2. 用 read 工具查找 `.milestone.md` 文件
3. 文件不存在就停下来问用户，忽略任务请求
4. **解决方案**：把任务放在消息开头，上下文放后面

### 3.2 Windows 栈溢出问题
- 错误码 `0xc0000409` 是 Windows 栈溢出
- 长消息（约 1KB 以上）会触发
- `--file` 参数也触发（可能是 opencode 读取文件时的处理问题）
- 短消息（< 200 字符）正常工作

### 3.3 `--file` 参数用途
- opencode 的 `--file` 是"附加文件到消息"，不是"从文件读取消息"
- 需要同时提供消息和文件路径

---

## 四、测试命令

### 4.1 启动后端服务器
```powershell
cd D:\claude-code\coai\platform\backend
Start-Process -FilePath ".\server.exe" -RedirectStandardOutput "stdout.log" -RedirectStandardError "stderr.log" -WindowStyle Hidden
```

### 4.2 注册并登录 Agent
```powershell
# 注册
$body = @{name='TestAgent'} | ConvertTo-Json
$result = Invoke-RestMethod -Uri 'http://localhost:3003/api/v1/agent/register' -Method POST -Body $body -ContentType 'application/json'
$key = $result.data.access_key

# 登录（带项目）
$projectId = 'proj_20309d86'
$body = @{key=$key; project=$projectId} | ConvertTo-Json
Invoke-RestMethod -Uri 'http://localhost:3003/api/v1/auth/login' -Method POST -Body $body -ContentType 'application/json'
```

当前已注册的 Agent：
- access_key: `3550311bb2e6d0a8f8e8b42c354d8ba1`
- project_id: `proj_20309d86`

### 4.3 触发 Dashboard Input
```powershell
$key = '3550311bb2e6d0a8f8e8b42c354d8ba1'
$body = @{target_block='task'; content='Create a task named "Test Task" with high priority'} | ConvertTo-Json
Invoke-RestMethod -Uri "http://localhost:3003/api/v1/dashboard/input?project_id=proj_20309d86" -Method POST -Body $body -ContentType 'application/json' -Headers @{'Authorization'="Bearer $key"}
```

### 4.4 检查任务列表
```powershell
$key = '3550311bb2e6d0a8f8e8b42c354d8ba1'
Invoke-RestMethod -Uri "http://localhost:3003/api/v1/task/list?project_id=proj_20309d86" -Method GET -Headers @{'Authorization'="Bearer $key"} | ConvertTo-Json -Depth 5
```

### 4.5 直接测试 opencode（短消息，能工作）
```powershell
cd D:\claude-code\coai\platform\backend\agent-config
opencode run --pure --agent maintain --model minimax-coding-plan/MiniMax-M2.7 --format json --dangerously-skip-permissions "Create a task named 'Direct Test' with high priority"
```

### 4.6 查看日志
```powershell
Get-Content D:\claude-code\coai\platform\backend\stderr.log -Tail 50
```

---

## 五、关键文件清单

### 5.1 后端核心
| 文件 | 作用 |
|------|------|
| `platform/backend/cmd/server/main.go` | 入口，路由注册 |
| `platform/backend/internal/opencode/scheduler.go` | Agent 调度，消息构建，工具调用捕获 |
| `platform/backend/internal/service/tool_handler.go` | 工具执行（写文件） |
| `platform/backend/internal/agent/prompts/maintain.md` | Maintain Agent prompt |
| `platform/backend/internal/model/init.go` | 数据库初始化（已移除 DROP TABLE） |

### 5.2 项目数据
| 文件 | 作用 |
|------|------|
| `platform/data/projects/proj_20309d86/DIRECTION.md` | 方向块 |
| `platform/data/projects/proj_20309d86/MILESTONE.md` | 里程碑块 |
| `platform/data/projects/proj_20309d86/TASKS.md` | 任务列表 |
| `platform/data/projects/proj_20309d86/project.json` | 项目元信息 |

### 5.3 配置
| 文件 | 作用 |
|------|------|
| `configs/config.yaml` | 后端配置（端口 3003） |
| `platform/backend/agent-config/opencode.json` | 平台 agent 配置（无 MCP） |

---

## 六、待办事项

### 6.1 紧急
- [ ] **解决长消息栈溢出问题**
  - 可能方案：拆分消息、用 stdin 传递、检查 opencode 源码
  - 或者：简化消息内容，只保留必要信息

### 6.2 高优先级
- [ ] 前端从文件读取项目数据（DIRECTION.md、MILESTONE.md、TASKS.md）
- [ ] 广播持久化到 JSON（当前只存 Redis）
- [ ] 按 agentID 定点广播（代码已就绪）

### 6.3 中优先级
- [ ] 审核 agent 测试（Audit1/Fix/Audit2 流程）
- [ ] 文件同步到本地
- [ ] 多人协作测试

---

## 七、技术决策记录

### 7.1 为什么数据改为文件存储
- AI 自然地会用 read 工具探索文件
- 文件对人类可读，便于调试
- 避免数据库和文件状态不一致

### 7.2 为什么平台只捕获平台工具
- 原生工具（read、glob 等）由 opencode 执行
- 平台工具（create_task 等）需要写入数据库/文件
- 不拦截原生工具，让 AI 自由探索代码库

### 7.3 消息构建顺序
```
1. Project Data Location（项目路径）
2. TASK TO COMPLETE（任务内容）
3. 上下文信息（Milestone、Version 等）
4. Role Prompt（角色定义和工具说明）
```

---

## 八、已知问题

1. **Windows 栈溢出** — 长消息触发 `0xc0000409`
2. **`--file` 参数不解决问题** — 可能 opencode 读取文件时也有问题
3. **模型注意力分散** — 上下文信息会让模型偏离任务（已通过调整消息顺序缓解）
