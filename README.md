# A3C 平台 - 项目设计文档

> 本文档整合所有讨论结果，作为项目的核心设计参考。
> 项目定位：**个人项目**，但功能齐全（5-10万行代码规模）

---


## 文档导航

### 第一阶段：MVP 核心协作流 (docs/phase1)
- [1. 项目概述](docs/phase1/01_overview.md)
  - 项目目的、效果预期、开发规划
- [2. 架构设计](docs/phase1/02_architecture.md)
  - 技术突破、系统架构、核心功能模块
- [3. MCP 工具集](docs/phase1/03_mcp_tools.md)
  - 用户端工具详细定义、平台端 Agent 交互
- [4. 消息与状态](docs/phase1/04_message_and_state.md)
  - 内容分块、广播推送机制
- [5. 数据模型](docs/phase1/05_data_model.md)
  - MySQL 表结构、Redis 缓存设计
- [6. 运维监控](docs/phase1/06_operations.md)
  - 监控维度、告警规则
- [7. 待讨论事项](docs/phase1/07_discussions.md)
  - 目前遗留的开放性问题
- [8. 附录与日志](docs/phase1/08_appendix_and_changelog.md)
  - 参考文档、更新历史

### 第二阶段：Git 原生分支流 (docs/phase2)
- [1. AI 分支工作流](docs/phase2/01_ai_branching_workflow.md)
  - 分支沙盒概念、PR 机制、工具扩展、Agent 职责进阶
