# A3C 平台开发状态

> 更新时间：2026-04-20 22:28

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
| **修复 Agent** | fix | L1 级自动修复 |
| **数据持久化** | MySQL + 文件 | 任务同时写数据库和 TASKS.md |
| **MCP 客户端** | 8个工具 | 全部测试通过 |
| **任务限制** | 一Agent一任务 | 领取前检查是否已有任务 |
| **状态同步** | my_task 字段 | status_sync 返回当前已领取任务 |
| **同步审核** | 等待审核结果 | change_submit 阻塞等待审核完成 |
| **Git 能力** | 版本管理 | init, commit, tag, diff, rollback |
| **版本回滚** | Rollback API | 可回滚到任意版本 |
| **SSE 广播** | 实时推送 | VERSION_ROLLBACK, AGENT_ONLINE 事件 |
| **项目隔离** | 数据隔离 | 多项目数据独立存储 |

### 🔧 本次修复的问题

1. **stdin 传消息** - `--file` 改为 stdin，解决 "must provide a message" 错误
2. **绝对路径配置** - `data_dir` 改为绝对路径，解决文件写入失败
3. **create_task 写数据库** - 任务可被 task/list API 查询
4. **change submit 触发审核** - 添加 `StartAuditWorkflowAndWait` 同步等待
5. **任务领取限制** - 一个 Agent 只能领取一个任务
6. **status_sync 增强** - 添加 my_task 字段返回已领取任务
7. **L2 拒绝后重置** - 10分钟无心跳/无重提交则重置任务状态
8. **MCP timeout 增加** - change_submit timeout 改为 2 分钟
9. **Git 路径修复** - 使用 DataPath 而非相对路径
10. **Diff 字段填充** - 提交时填充文件内容供审核使用
11. **pending 目录路径** - 修复为绝对路径
12. **任务自动完成** - L0 审核通过后自动完成任务

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

### 流程 3：改动审核（同步）
```
POST /task/claim → POST /change/submit
  → StartAuditWorkflowAndWait (同步等待)
  → opencode run (agent=audit_1)
  → 审核文件内容
  → audit_output (L0/L1/L2)
  → L0: approved, L1: pending_fix + fix agent, L2: rejected
  → 返回审核结果给 MCP 客户端
```

### 流程 4：MCP 客户端测试
```
1. a3c_platform action=login → 登录成功
2. select_project project_id=... → 选择项目
3. status_sync → 返回任务列表 + my_task
4. task action=claim → 领取任务（有任务则拒绝）
5. change_submit → 等待审核结果返回
```

### 流程 5：Git 版本管理
```
1. GitInit → 初始化项目 repo
2. GitAddAndCommit → 提交改动
3. GitTagVersion → 创建版本标签
4. GitListVersions → 获取版本列表
5. GitRevertToVersion → 回滚到指定版本
```

---

## 三、测试结果汇总

### ✅ 已测试通过

| 测试项 | 结果 | 备注 |
|--------|------|------|
| MCP 客户端登录 | ✅ | a3c_platform |
| MCP 选择项目 | ✅ | select_project |
| MCP 状态同步 | ✅ | status_sync 返回 my_task |
| MCP 任务领取 | ✅ | 已有任务时拒绝 |
| MCP 提交改动 | ✅ | 同步等待审核结果 |
| L0 审核通过 | ✅ | 干净代码直接批准 |
| L1 审核修复 | ✅ | 触发 fix agent |
| L2 审核拒绝 | ✅ | 返回拒绝原因 |
| Git 版本列表 | ✅ | /version/list |
| Git 版本回滚 | ✅ | /version/rollback |
| SSE 广播 | ✅ | VERSION_ROLLBACK 事件 |
| 项目隔离 | ✅ | 新项目无任务 |

### 📋 待测试项

- [ ] 前端可用性测试
- [ ] Audit2 Agent 测试
- [ ] 里程碑切换测试
- [ ] 评估 Agent 测试

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
