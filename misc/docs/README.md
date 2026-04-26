# Documentation Index

本目录组织方式：`dev/` 是**权威规范**，`archive/` 是历史阶段材料，`design/` 是设计稿，根目录几份文档是**当前仍在生效的专题**。

## 我该从哪读起？

**第一次看这个项目** → [`dev/01_overview.md`](dev/01_overview.md) 起步，顺序看 01 → 13。

**想搞清项目当前文件布局** → [`PROJECT-MAP.md`](PROJECT-MAP.md)（盘点报告，每个目录/命令/包都标了生产/实验/归档）。

**运维迁移操作** → [`migration-runbook.md`](migration-runbook.md)。

**理解 refinery 知识蒸馏** → [`refinery.md`](refinery.md)。

---

## 权威规范 · `dev/`

按编号顺序逐篇读。后来的文档默认假设你看过前面的。

| 文件 | 主题 |
|---|---|
| `01_overview.md` | 项目总览 + 术语 |
| `02_architecture.md` | 系统架构 |
| `03_api_spec.md` | HTTP API |
| `04_platform_agents.md` | 平台内置 agent 角色 |
| `05_mcp_skill.md` | MCP 客户端接入 |
| `06_dashboard.md` | 前端 dashboard |
| `07_data_model.md` | 数据库 schema |
| `08_dev_plan.md` | 开发路线 |
| `09_e2e_flows.md` | 端到端业务流程 |
| `10_error_handling.md` | 错误处理约定 |
| `11_phase3a_automation.md` | Phase 3a 自动化 |
| `12_phase3b_self_evolution.md` | Phase 3b 自进化 |
| `13_frontend_chief_governance_ui.md` | 前端 chief 治理 UI |

## 专题文档（根目录）

| 文件 | 主题 | 状态 |
|---|---|---|
| `PROJECT-MAP.md` | 项目盘点 / 整理建议 | 2026-04 新增，动态更新 |
| `migration-runbook.md` | opencode → native 迁移操作手册 | 稳定 |
| `refinery.md` | 知识蒸馏流水线 | 稳定 |

## 设计稿 · `design/`

- `mockups/frontend-mockup-*.html` —— 前端设计稿（4 个版本，从早期 current → office → refined → premium 的演进记录）。不是当前实现，是可视化参考。

## 归档 · `archive/`

历史材料。正常不需要读。保留用途：可溯源、处理历史 bug 时回查。

```
archive/
├── phases/       ← phase1/2/3 的阶段文档与阶段完成报告
├── handoff/      ← 多次交接时留下的 handoff markdown
├── evolvable/    ← "自进化" 特性的草稿与参考
└── agent-replacement-assessment.md  ← 老 agent 替换评估（已过时）
```

---

## 文档写作约定

**新加文档的位置**：
- 权威规范（长期有效）→ `dev/` 下按编号接续
- 专题操作手册（运维/开发流程）→ 根目录
- 阶段性产物（一次性报告）→ `archive/phases/`
- 设计/视觉资产 → `design/`

**不再接受**：handoff 风格的一次性总结塞在 `docs/` 根部。这类内容进 `archive/` 或直接写 CHANGELOG。
