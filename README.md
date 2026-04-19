# A3C - Agent Coordination Platform

Agent协作协调平台MVP，支持看板管理、MCP远程连接、自动版本追踪。

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
cd backend
go mod tidy
go run cmd/server/main.go

# 启动前端（新终端）
cd frontend
npm install
npm run dev
```

### 访问地址

- 前端看板: http://localhost:33303
- 后端API: http://localhost:3303
- 健康检查: http://localhost:3303/health

## 项目结构

```
coai/
├── backend/              # Go后端
│   ├── cmd/server/       # 入口
│   ├── internal/         # 核心代码
│   │   ├── config/       # 配置
│   │   ├── handler/      # HTTP处理器
│   │   ├── model/        # 数据模型
│   │   ├── repository/   # 数据访问
│   │   └── service/      # 业务逻辑
│   └── pkg/              # 公共包
│       ├── logger/       # 日志
│       └── response/     # 响应封装
├── frontend/             # React前端
│   ├── src/
│   │   ├── api/          # API客户端
│   │   ├── components/   # 组件
│   │   ├── hooks/        # Hooks
│   │   ├── pages/        # 页面
│   │   ├── stores/       # 状态管理
│   │   └── utils/        # 工具函数
│   └── public/           # 静态资源
├── configs/              # 配置文件
└── docs/                 # 设计文档
```

## 技术栈

- **后端**: Go + Gin + GORM + MySQL + Redis
- **前端**: React + TypeScript + Vite + Tailwind + Zustand
- **MCP**: TypeScript + @modelcontextprotocol/sdk