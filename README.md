# A3C - Agent Coordination Platform

Agent协作协调平台MVP，支持看板管理、MCP远程连接、自动版本追踪、分支工作流、PR评审。

## 快速开始

### 环境要求

- Go 1.22+
- Node.js 18+
- MySQL 8.0+
- Redis 7+

### 启动开发环境

```bash
# 启动MySQL和Redis
docker-compose up -d

# 启动后端
cd platform/backend
go mod tidy
go build -o bin/server.exe ./cmd/server/
./bin/server.exe

# 启动前端（新终端）
cd frontend
npm install
npm run dev
```

### 访问地址

- 后端API: http://localhost:3003
- 健康检查: http://localhost:3003/health

## 项目结构

```
coai/
├── platform/backend/     # Go后端
│   ├── cmd/server/       # 入口
│   └── internal/         # 核心代码
│       ├── handler/      # HTTP处理器
│       ├── service/      # 业务逻辑 + Agent调度
│       ├── model/        # 数据模型
│       ├── agent/        # Agent角色定义
│       ├── opencode/     # OpenCode调度器
│       └── middleware/   # 中间件
├── frontend/             # React前端
│   └── src/
│       ├── api/          # API客户端
│       ├── components/   # 组件
│       ├── pages/        # 页面
│       └── stores/       # 状态管理
├── client/mcp/           # MCP客户端
├── .opencode/            # OpenCode配置 + Agent定义 + 工具
├── configs/              # 配置文件
└── docs/                 # 设计文档
```

## 技术栈

- **后端**: Go + Gin + GORM + MySQL + Redis
- **前端**: React + TypeScript + Vite + Tailwind + Zustand
- **AI调度**: OpenCode (pure serve) + MiniMax-M2.7
- **MCP**: TypeScript + @modelcontextprotocol/sdk