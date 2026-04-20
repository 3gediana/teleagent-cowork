# A3C 平台开发状态

> 更新时间：2026-04-20 21:35

---

## 一、当前完成状态

### ✅ 已验证通过

| 模块 | 功能 | 说明 |
|------|------|------|
| **后端服务** | Go API Server | 端口 3003，已启动运行 |
| **Agent 调度** | OpenCode 集成 | 端口 15000+，stdin 传消息 |
| **维护 Agent** | create_task | Dashboard input → 创建任务 → 写 DB + 文件 |
| **咨询 Agent** | project/info | 回答项目里程碑和任务问题 |
| **审核 Agent** | audit_1 | 审核改动提交，返回 L0/L1/L2 |
| **数据持久化** | MySQL + 文件 | 任务同时写数据库和 TASKS.md |

### 🔧 本次修复的问题

1. **stdin 传消息** - `--file` 改为 stdin，解决 "must provide a message" 错误
2. **绝对路径配置** - `data_dir` 改为绝对路径，解决文件写入失败
3. **create_task 写数据库** - 任务可被 task/list API 查询
4. **change submit 触发审核** - 添加 `StartAuditWorkflow` 异步调用

---

## 二、已验证的完整流程

### 流程 1：任务创建
```
POST /dashboard/input (target_block=task)
  → TriggerMaintainAgent
  → opencode run (agent=maintain)
  → create_task 工具调用
  → 写入 TASKS.md + DB
  → 任务出现在 task/list
```

### 流程 2：咨询查询
```
POST /project/info (query=...)
  → TriggerConsultAgent
  → opencode run (agent=consult)
  → 读取项目文件
  → 返回文本回答
```

### 流程 3：改动审核
```
POST /task/claim → POST /change/submit
  → StartAuditWorkflow (异步)
  → opencode run (agent=audit_1)
  → 审核文件内容
  → audit_output (L0/L1/L2)
  → 更新 change 状态 (approved/pending_fix/rejected)
```

---

## 三、待办事项

### 🔴 高优先级

- [ ] **MCP 客户端对接测试**
  - MCP Server 是否能连接后端 API
  - 7 个 MCP 工具是否正常工作
  - 轮询和心跳机制是否生效

- [ ] **数据流通性验证**
  - 前端是否能读取后端数据
  - SSE 事件是否正确推送
  - 文件同步是否工作

- [ ] **项目隔离验证**
  - 多项目数据是否隔离
  - 文件锁是否防止冲突
  - 任务领取是否互斥

### 🟡 中优先级

- [ ] **广播机制测试**
  - SSE 连接是否稳定
  - 广播事件是否正确分发
  - 离线消息是否缓存

- [ ] **前端可用性测试**
  - Dashboard 页面是否正常显示
  - 任务列表是否实时更新
  - 对话交互是否流畅

- [ ] **修复 Agent 测试**
  - L1 级审核是否触发修复 Agent
  - 修复后是否正确合并

### 🟢 低优先级

- [ ] **评估 Agent 测试**
  - 项目导入时是否触发
  - assess_output 是否正确

- [ ] **里程碑切换测试**
  - 所有任务完成时是否提议切换
  - 归档和新建流程是否正确

- [ ] **Git 操作测试**
  - commit 是否正确执行
  - 版本号是否正确递增
  - rollback 是否工作

---

## 四、关键文件清单

### 后端核心
| 文件 | 作用 |
|------|------|
| `platform/backend/cmd/server/main.go` | 服务入口，路由注册 |
| `platform/backend/internal/opencode/scheduler.go` | Agent 调度器 |
| `platform/backend/internal/service/tool_handler.go` | 平台工具执行 |
| `platform/backend/internal/service/audit.go` | 审核工作流 |
| `platform/backend/internal/handler/change.go` | 改动提交处理 |

### 配置
| 文件 | 作用 |
|------|------|
| `configs/config.yaml` | 后端配置 |
| `platform/backend/agent-config/opencode.json` | Agent 配置（禁用 MCP） |

### 项目数据
| 目录 | 作用 |
|------|------|
| `platform/data/projects/{project_id}/` | 项目数据根目录 |
| `platform/data/projects/{project_id}/TASKS.md` | 任务列表 |
| `platform/data/projects/{project_id}/MILESTONE.md` | 里程碑 |
| `platform/data/projects/{project_id}/DIRECTION.md` | 方向块 |

---

## 五、测试命令速查

### 启动服务
```powershell
# 启动 MySQL (D:/mysql)
D:/mysql/bin/mysqld.exe --console

# 启动 Redis (D:/redis)
D:/redis/redis-server.exe redis.windows.conf

# 启动后端
cd D:/claude-code/coai/platform/backend
./server.exe
```

### API 测试
```powershell
# 注册 Agent
curl -X POST http://localhost:3003/api/v1/agent/register -H "Content-Type: application/json" -d '{"name":"TestAgent"}'

# 登录
curl -X POST http://localhost:3003/api/v1/auth/login -H "Content-Type: application/json" -d '{"key":"ACCESS_KEY","project":"proj_20309d86"}'

# 创建任务
curl -X POST "http://localhost:3003/api/v1/dashboard/input?project_id=proj_20309d86" -H "Authorization: Bearer ACCESS_KEY" -H "Content-Type: application/json" -d '{"target_block":"task","content":"Create a task named X with high priority"}'

# 查看任务
curl "http://localhost:3003/api/v1/task/list?project_id=proj_20309d86" -H "Authorization: Bearer ACCESS_KEY"
```

---

## 六、已知问题

1. **文件提交路径** - 提交的文件实际不存在时审核会 L2 拒绝（预期行为）
2. **长消息处理** - stdin 方式已解决，但超长消息可能仍有问题
3. **审核结果格式** - audit_output 的 issues 格式需要前端适配

---

## 七、下一步计划

1. 优先测试 MCP 客户端对接
2. 验证 SSE 广播机制
3. 启动前端测试 UI 交互
4. 完成多项目隔离验证
