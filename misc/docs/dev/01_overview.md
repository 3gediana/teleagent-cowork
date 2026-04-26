# A3C - Agent Collaboration Command Center

## 1. 项目定义

A3C 是一个多 Agent 协作协调平台，解决多 Agent 开发中的**对齐、冲突、可见性**问题。

**核心定位**：
- 平台作为**消息中枢**，主动推送项目状态
- 方向主权归**人类**，Agent 只负责执行与结构化表达
- 所有改动经过**审核**，确保符合项目方向

## 2. 目标规模

| 维度 | 值 |
|------|-----|
| MVP 代码量 | 1-2 万行 |
| 并发用户 | 5-6 人同时在线 |
| 项目隔离 | 单项目绑定，不支持多项目并行 |
| 部署方式 | 单机公网部署 |

## 3. 技术栈

| 层 | 技术 |
|-----|------|
| 后端 | Go (Gin/Fiber) |
| MCP Server | TypeScript + @modelcontextprotocol/sdk |
| 前端 | React + TypeScript + Tailwind CSS |
| 数据库 | MySQL 8 |
| 缓存/消息 | Redis |
| 版本控制 | Git (本地仓库) |
| AI 框架 | OpenCode (无头模式) |

## 4. 核心架构概览

```
用户端 (多人)                              平台端 (公网)
┌─────────────┐                           ┌─────────────────────────┐
│ OpenCode    │                           │ Go API Server           │
│ + MCP Tools │◄──HTTP──────────────────►│   ├─ 认证/项目管理       │
│ + Sidecar   │   (轮询5s/心跳5min)       │   ├─ 任务/锁/文件管理    │
└─────────────┘                           │   ├─ 审核/改动管理       │
                                          │   └─ 广播/SSE            │
                                          │                         │
                                          │ MySQL (持久化)           │
                                          │ Redis  (缓存/队列)       │
                                          │ Git    (代码仓库)        │
                                          │                         │
                                          │ OpenCode (无头模式)       │
                                          │   ├─ 审核 Agent          │
                                          │   ├─ 修复 Agent          │
                                          │   ├─ 维护 Agent          │
                                          │   └─ 咨询 Agent          │
                                          └─────────────────────────┘
```

## 5. MVP 功能范围

| 编号 | 模块 | 功能 |
|------|------|------|
| M01 | 登录与状态 | 注册、登录、登出、心跳、断线处理 |
| M02 | 任务管理 | 创建、领取、完成（仅维护Agent可创建） |
| M03 | 文件锁 | 锁定、释放、5分钟TTL、心跳续租 |
| M04 | 改动提交 | 提交、版本检查、审核队列 |
| M05 | 审核 Agent | 三级审核（L0/L1/L2）、双Agent协作 |
| M05b | 修复 Agent | L1问题验证与修复 |
| M06 | 维护 Agent | 任务创建、里程碑管理、方向对齐 |
| M07 | 广播机制 | 5种广播事件、SSE推送、离线队列 |
| M08 | 版本管理 | Git集成、版本号、代码回滚 |
| M09 | 看板界面 | Web仪表盘、对话区、块状态展示 |
| M10 | 咨询 Agent | 项目信息查询 |
| M11 | 评估 Agent | 项目导入时评估结构、生成ASSESS_DOC.md |

## 6. 文档索引

| 文档 | 内容 |
|------|------|
| [02_architecture.md](02_architecture.md) | 项目结构、模块划分、技术架构 |
| [03_api_spec.md](03_api_spec.md) | 所有API端点定义 |
| [04_platform_agents.md](04_platform_agents.md) | 平台端Agent详细设计 |
| [05_mcp_skill.md](05_mcp_skill.md) | MCP Skill内容（用户端工作流） |
| [06_dashboard.md](06_dashboard.md) | 看板UI设计 |
| [07_data_model.md](07_data_model.md) | 数据模型、索引、Redis key |
| [08_dev_plan.md](08_dev_plan.md) | 开发里程碑与验收标准 |
| [09_e2e_flows.md](09_e2e_flows.md) | 端到端流程图 |
| [10_error_handling.md](10_error_handling.md) | 错误处理规范 |