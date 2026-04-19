# 技术架构

## 1. 项目结构

```
a3c/
├── cmd/
│   └── server/
│       └── main.go                 # 入口
├── internal/
│   ├── config/                     # 配置加载
│   ├── handler/                    # HTTP handler
│   │   ├── auth.go                 # 登录/登出/心跳
│   │   ├── project.go              # 项目管理
│   │   ├── task.go                  # 任务CRUD
│   │   ├── filelock.go             # 文件锁
│   │   ├── change.go               # 改动提交/审核
│   │   ├── sync.go                  # 状态同步/文件同步
│   │   ├── broadcast.go            # SSE推送
│   │   ├── dashboard.go            # 看板API
│   │   ├── agent.go                # 咨询Agent
│   │   └── mcp.go                  # MCP Server端点
│   ├── service/                    # 业务逻辑
│   │   ├── auth.go
│   │   ├── task.go
│   │   ├── filelock.go
│   │   ├── change.go
│   │   ├── audit.go                # 审核流程编排
│   │   ├── broadcast.go            # 广播逻辑
│   │   ├── milestone.go            # 里程碑管理
│   │   └── git.go                  # Git操作
│   ├── agent/                      # OpenCode Agent角色管理
│   │   ├── manager.go              # Agent生命周期（统一OpenCode实例）
│   │   ├── prompts/                # 各角色Prompt模板
│   │   │   ├── audit.md            # 审核角色Prompt
│   │   │   ├── fix.md              # 修复角色Prompt
│   │   │   ├── maintain.md         # 维护角色Prompt
│   │   │   ├── consult.md          # 咨询角色Prompt
│   │   │   └── assess.md           # 评估角色Prompt
│   │   ├── role.go                 # 角色定义与切换
│   │   ├── tools.go                 # 各角色工具注册
│   │   └── session.go               # Session管理
│   ├── model/                      # 数据模型
│   ├── repo/                       # 数据访问层
│   ├── middleware/                  # 认证/日志/限流
│   └── util/                       # 工具函数
├── mcp/                            # 用户端MCP Server（TypeScript）
│   ├── src/
│   │   ├── index.ts                # MCP Server入口
│   │   ├── tools/                  # 7个MCP工具定义
│   │   │   ├── platform.ts         # a3c_platform
│   │   │   ├── task.ts             # task
│   │   │   ├── filelock.ts         # filelock
│   │   │   ├── change-submit.ts    # change.submit
│   │   │   ├── file-sync.ts        # file.sync
│   │   │   ├── status-sync.ts      # status.sync
│   │   │   └── project-info.ts     # project_info
│   │   ├── poller.ts              # 后台轮询线程（广播+心跳）
│   │   ├── api-client.ts          # 平台HTTP API客户端
│   │   └── utils.ts               # 工具函数
│   ├── package.json
│   └── tsconfig.json
├── web/                            # 前端
│   ├── src/
│   │   ├── components/             # React组件
│   │   ├── pages/                   # 页面
│   │   ├── hooks/                   # 自定义hooks
│   │   ├── api/                     # API调用
│   │   └── store/                   # 状态管理
│   ├── package.json
│   └── vite.config.ts
├── migrations/                     # 数据库迁移
├── configs/                        # 配置文件
├── scripts/                        # 运维脚本
├── go.mod
└── go.sum
```

## 2. 模块依赖关系

```
cmd/server/main.go
    │
    ├── handler/ ──────────► service/
    │                           │
    │                           ├── repo/ (数据访问)
    │                           ├── agent/ (Agent管理)
    │                           └── git/ (Git操作)
    │
    └── middleware/
```

## 3. 技术选型细节

### 3.1 后端 - Go

| 组件 | 选型 | 说明 |
|------|------|------|
| Web框架 | TODO: Gin vs Fiber | Gin生态成熟，Fiber性能更优 |
| ORM | TODO: GORM vs sqlx | GORM全功能，sqlx轻量灵活 |
| 配置 | Viper | Go标准配置库 |
| 日志 | Zap | 高性能结构化日志 |
| UUID | google/uuid | 生成唯一ID |

### 3.2 前端 - React + TS + Tailwind

| 组件 | 选型 | 说明 |
|------|------|------|
| 构建 | Vite | 快速构建 |
| 状态管理 | TODO: Zustand vs Jotai | 轻量级状态管理 |
| HTTP | Axios | API请求 |
| SSE | EventSource API | 广播推送 |
| UI组件库 | TODO: shadcn/ui vs Ant Design | |

### 3.3 数据存储

| 存储 | 用途 |
|------|------|
| MySQL 8 | 持久化数据（项目、Agent、任务、锁、改动、里程碑） |
| Redis | 缓存、消息队列、在线状态、锁TTL |
| Git (本地) | 代码版本管理 |

### 3.4 AI 集成

| 组件 | 说明 |
|------|------|
| OpenCode | 无头模式运行平台端Agent |
| Session管理 | 同一OpenCode实例，不同角色通过不同Prompt切换 |
| 自定义工具 | 平台注册OpenCode工具供Agent调用 |

### 3.5 Agent角色架构

**核心思路**：所有"Agent"本质是同一个OpenCode实例，通过不同Prompt模板切换角色。

| 角色 | 激活条件 | 上下文 | 工具 |
|------|----------|--------|------|
| 审核角色 | 有change进审核队列 | 完整注入 | audit_output |
| 修复角色 | 审核角色判定L1 | 部分（diff+提交文件） | fix_output |
| 维护角色 | 20分钟定时/里程碑完成/看板输入 | 完整注入 | create_task, update_milestone, propose_direction, read/edit/glob |
| 咨询角色 | 收到project_info请求 | 注入项目概览 | read/glob |
| 评估角色 | 项目导入时（一次性） | 完整项目文件 | assess_output（格式化文档输出） |

**角色切换**：平台根据触发条件选择对应Prompt模板，注入上下文，调用OpenCode。

### 3.6 用户端架构：MCP Server + 内置轮询

用户端不使用独立侧车进程，而是将所有逻辑集成到MCP Server中。

```
用户本机
┌──────────────────────────────────────────────────┐
│  OpenCode                                         │
│     ↓ ↑ MCP协议（阻塞式工具调用）                    │
│  A3C MCP Server (TypeScript)                      │
│     ├── 7个MCP工具（a3c_platform, task, filelock,  │
│     │   change.submit, file.sync, status.sync,    │
│     │   project_info）                             │
│     │   └── 工具调用 → HTTP → 平台API               │
│     │       （阻塞式，Agent必须等响应才能继续）        │
│     ├── 后台轮询线程                                │
│     │   ├── 每5秒轮询平台获取广播                    │
│     │   ├── 每5分钟发送心跳续租                      │
│     │   └── 每5秒检查OpenCode进程存活                │
│     └── 广播投递                                   │
│         └── 收到广播 → OpenCode session API注入TUI  │
└──────────────────────────────────────────────────┘
         │
    HTTP 通过 localhost 或 隧道公网URL
         │
         ↓
    A3C 平台
```

**MCP工具阻塞机制**：MCP协议天然是请求-响应模式，AI调用工具后必须等响应才能继续操作。这规范了Agent行为——提交改动时必须等确认才能做其他事。

| 工具 | 阻塞行为 |
|------|---------|
| a3c_platform | 阻塞直到登录/登出完成 |
| task.claim | 阻塞直到确认领取成功或失败 |
| task.complete | 阻塞直到确认完成 |
| filelock.acquire | 阻塞直到确认锁定成功或冲突 |
| filelock.release | 阻塞直到确认释放 |
| change.submit | 阻塞直到确认提交进入审核队列（不等审核结果） |
| file.sync | 阻塞直到文件下载完成 |
| status.sync | 阻塞直到返回状态数据 |
| project_info | 阻塞直到返回回答 |

**审核结果不阻塞**：change.submit返回"已提交等待审核"后Agent可继续其他操作，审核结果通过广播通知。

## 4. 通信架构

### 4.1 整体网络拓扑

```
用户A（平台部署者）                      用户B/C/...（远程协作者）
┌─────────────────────────┐            ┌──────────────────────────┐
│  A3C平台（用户A本机）    │            │  Agent（用户B本机）       │
│  ┌─────────────────────┐│            │  ┌──────────────────────┐│
│  │ Go API Server       ││            │  │ OpenCode             ││
│  │ MySQL/Redis/Git     ││            │  │ + A3C MCP Server     ││
│  │ 平台端Agent(OpenCode)││            │  │   ├── MCP工具调用     ││
│  │ Web前端(看板)        ││            │  │   ├── 后台轮询        ││
│  │ MCP Server端点      ││            │  │   └── 心跳续租        ││
│  └─────────────────────┘│            │  └──────────────────────┘│
└───────────┬─────────────┘            └────────────┬─────────────┘
            │                                        │
     localhost:8080                          通过隧道公网URL
            │                                        │
            │                            ┌───────────▼───────────┐
            │                            │  隧道/公网 (frp/ngrok) │
            │                            └───────────┬───────────┘
            │                                        │
            └────────────────────────────────────────┘
```

**关键点**：
- 平台部署在用户A本机，同时暴露HTTP API和MCP Server端点
- 远程协作者通过隧道公网URL访问平台
- MCP Server端点供OpenCode远程连接（type: "remote"）
- 平台不区分来源，统一处理所有请求
- 每个用户的MCP Server独立运行，内置轮询线程

### 4.2 双通道通信

```
用户端 MCP Server
    │
    ├── MCP Remote 连接 ──────────► 平台 MCP 端点
    │   （工具调用，阻塞式）            （7个工具API）
    │
    ├── HTTP 心跳/轮询 ──────────► 平台 HTTP API
    │   （后台线程）                    （轮询+心跳）
    │
    └── 广播投递
        └── 收到广播 → OpenCode session API → TUI

看板（Web前端）
    │
    ├── HTTP POST /api/*         # 看板操作
    └── SSE GET /api/events      # 实时推送（看板专用）
```

**MCP连接负责工具调用（阻塞式），HTTP负责轮询和心跳（后台线程）。**

### 4.3 广播推送机制

**核心原则**：平台跟踪每条广播已推送给哪些Agent，不重复推送。新上线Agent只推送当前最新状态，不推历史。

```
事件源触发广播
    ↓
Service层生成广播消息
    ↓
写入Redis消息队列 + 记录广播内容
    ↓
┌──────────────────────────────────────────┐
│           推送到两种通道                    │
│                                          │
│   通道1: SSE → 看板Web前端（实时）          │
│   通道2: MCP后台轮询 → Agent（5秒间隔）      │
│                                          │
│   每条广播维护:                            │
│   broadcast:{project_id}:{msg_id}:acked  │
│   = Set(已收到该广播的Agent ID集合)        │
└──────────────────────────────────────────┘
    ↓
MCP后台轮询时:
    1. 查询该Agent未收到的广播（不在acked集合中的）
    2. 返回未收到的广播消息
    3. 将该Agent ID加入每条广播的acked集合
    ↓
新Agent首次轮询:
    1. 返回当前最新状态（方向块+里程碑块+版本号）
    2. 不返回历史广播事件
```

### 4.4 广播内容与不广播内容

**广播的5种事件**：

| 事件 | 触发 | 内容 |
|------|------|------|
| DIRECTION_CHANGE | 方向块变更 | reason + 完整方向块 |
| MILESTONE_UPDATE | 里程碑块更新 | reason + 完整里程碑块 |
| MILESTONE_SWITCH | 里程碑切换 | 旧里程碑归档 + 新里程碑开始 |
| VERSION_UPDATE | 审核通过版本新增 | 新版本号 |
| VERSION_ROLLBACK | 代码回滚 | 回滚原因 + 回退版本号 |

**不广播的事件**：

| 事件 | 处理方式 |
|------|----------|
| 任务领取冲突 | API直接返回错误 |
| 任务完成 | status.sync查询 |
| 锁变更 | status.sync查询 |
| 审核结果 | AUDIT_RESULT私信通知提交者 |

**5秒轮询合并**：如果同一类型广播在5秒内连续发生（如方向块快速变更两次），轮询只返回最新一条，忽略中间状态。

## 5. 部署架构

```
用户A本机
├── Go API Server (:8080)
│   ├── HTTP API端点（看板+轮询+心跳）
│   └── MCP Server端点（供OpenCode连接）
├── MySQL (:3306)
├── Redis (:6379)
├── Git仓库 (本地存储)
├── 前端静态资源 (Go内嵌或Nginx)
├── OpenCode (平台端Agent)
└── 隧道客户端 (frpc/ngrok) → 公网
```

用户端MCP Server配置示例（opencode.json）：

```jsonc
{
  "mcp": {
    "a3c": {
      "type": "remote",
      "url": "https://your-tunnel-url/mcp",
      "headers": {
        "Authorization": "Bearer your-access-key"
      },
      "environment": {
        "A3C_PROJECT": "my-project"
      },
      "enabled": true
    }
  }
}
```

**MVP单机部署**：用户A本机运行所有服务，通过隧道暴露给远程协作者。

## 6. 安全设计

| 层 | 机制 |
|-----|------|
| 认证 | 密钥认证（注册时生成access_key） |
| 传输 | HTTPS（生产环境） |
| 项目隔离 | 所有查询强制带project_id过滤 |
| 锁机制 | 文件锁防止并发写入 |

**密钥存储**：明文存储，10+位随机字符串。

**TODO**: CORS策略、限流规则