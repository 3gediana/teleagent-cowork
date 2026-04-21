# A3C 平台开发状态

> 更新时间：2026-04-21 00:15

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
| **MCP 客户端** | 9个工具 | 全部测试通过（含check_locks） |
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
13. **锁冲突预检工具** - 新增 filelock check 检查文件是否被锁定
14. **Agent上下文注入** - 所有Agent现在都有完整的方向块、里程碑、任务列表上下文

---

## 二、Agent上下文注入情况

| Agent | DirectionBlock | MilestoneBlock | TaskList | LockList | ProjectPath |
|-------|:--------------:|:--------------:|:--------:|:--------:|:-----------:|
| Maintain | ✅ | ✅(含描述) | ✅ | ✅ | ❌ |
| Consult | ✅ | ✅(含描述) | ✅(完整) | ✅ | ✅ |
| Audit1 | ✅ | ✅(含描述) | ❌ | ❌ | ❌ |
| Audit2 | ✅ | ✅(含描述) | ✅ | ❌ | ❌ |
| Fix | ✅ | ✅(含描述) | ✅ | ❌ | ❌ |

---

## 三、前端状态

### 新增组件
- `TaskKanban.tsx` - 任务看板（三列：pending/claimed/completed）
- `ActivityStream.tsx` - 实时活动流
- `BroadcastPanel.tsx` - SSE事件显示面板
- `ChatPanel.tsx` - 与维护Agent对话
- `InfoCards.tsx` - 信息卡片组件
- `Modal.tsx` - Modal系统

### 待改进
- UI美观度需要优化
- 多轮对话功能未实现（需要OpenCode serve持久化session）
- 聊天交互流程需要改进

---

## 四、待完成事项

### 高优先级
1. **多轮对话实现** - 改用OpenCode serve API替代run --pure
2. **前端UI优化** - 当前UI不够美观

### 中优先级
3. **自动创建任务开关** - 文档有设计，代码未实现
4. **Audit2 Agent测试** - 尚未完整测试
5. **里程碑切换测试** - 尚未完整测试

---

## 五、关键文件清单

### 后端核心
| 文件 | 作用 |
|------|------|
| `platform/backend/cmd/server/main.go` | 服务入口，路由注册 |
| `platform/backend/internal/opencode/scheduler.go` | Agent 调度器 |
| `platform/backend/internal/service/tool_handler.go` | 平台工具执行 |
| `platform/backend/internal/service/audit.go` | 审核工作流 |
| `platform/backend/internal/service/maintain.go` | Maintain/Consult Agent触发 |
| `platform/backend/internal/handler/change.go` | 改动提交处理 |
| `platform/backend/internal/handler/filelock.go` | 文件锁处理（含check接口） |

### Agent提示词
| 文件 | 作用 |
|------|------|
| `platform/backend/internal/agent/prompts/maintain.md` | 维护Agent提示词 |
| `platform/backend/internal/agent/prompts/consult.md` | 咨询Agent提示词 |
| `platform/backend/internal/agent/prompts/audit_1.md` | 审核Agent1提示词 |
| `platform/backend/internal/agent/prompts/fix.md` | 修复Agent提示词 |

### 前端
| 目录 | 作用 |
|------|------|
| `frontend/src/pages/Dashboard.tsx` | 主页面 |
| `frontend/src/components/` | 组件目录 |
| `frontend/src/stores/appStore.ts` | 状态管理 |
| `frontend/src/hooks/useSSE.ts` | SSE事件处理 |

---

## 六、测试命令速查

### 启动服务
```powershell
# 启动 MySQL (D:/mysql)
D:/mysql/bin/mysqld.exe --console

# 启动 Redis (D:/redis)
D:/redis/redis-server.exe redis.windows.conf

# 启动后端
cd D:/claude-code/coai/platform/backend
./server.exe

# 启动前端
cd D:/claude-code/coai/frontend
npm run dev
```

### API 测试
```powershell
# 检查文件锁状态
curl -X POST http://localhost:3003/api/v1/filelock/check \
  -H "Authorization: Bearer ACCESS_KEY" \
  -H "Content-Type: application/json" \
  -d '{"files": ["src/auth.ts", "src/api.ts"]}'
```

---

## 七、已知问题

1. **多轮对话** - 当前使用run --pure，无会话持续能力
2. **前端UI** - 美观度不足，需要进一步优化
3. **聊天交互** - 应该是维护Agent引导需求后输出结构化提议让人确认
