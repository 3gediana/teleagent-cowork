# 项目盘点 · PROJECT-MAP

> 截至 2026-04-23。此文档不改代码，只是把"**手里现在到底有啥、哪些真在用、哪些是半成品、哪些是废的**"盘清楚。
>
> 图例：✅ 生产（必须的）· 🧪 实验/工具（留着有用但不是主体）· ⚠️ 可疑（像僵尸）· 🧹 建议清理

---

## 一、一句话全貌

```
coai2/
├── platform/backend/     ← Go 服务端（主体）
│   ├── cmd/              ← 14 个命令，只有 1 个是生产 server
│   └── internal/         ← 业务代码（handler / service / runner / ...）
├── platform/embedder/    ← Python sidecar（bge 嵌入，小）
├── frontend/             ← React 前端（dashboard）
├── client/               ← 客户端 SDK（目前只有 mcp 骨架）
├── docs/                 ← 文档 3 类：规范、阶段报告、evolvable 草稿
└── 根目录散落           ← 几个 mockup.html、测试 .ps1、两份 roleaudit 输出
```

## 二、`platform/backend/cmd/` ——14 个命令，只有 1 个是"生产"

| 命令 | 分类 | 干啥用 | 建议 |
|---|---|---|---|
| `server` | ✅ **生产** | 你真正要启动的 HTTP server | 保留，放 `cmd/` 唯一入口 |
| `platformlive` | 🧪 集成测试 | 13 阶段端到端测试（真 LLM + 真 DB） | 搬 `experiments/` 或 `testing/` |
| `planninglive` | 🧪 集成测试 | 同上，但覆盖 planning 流程 | 同上 |
| `planningsmoke` | 🧪 集成测试 | 不走真 LLM 的 smoke 版 | 同上 |
| `nativesmoke` | 🧪 实验 | 零依赖验证 native runtime | 同上 |
| `nativesmokereal` | 🧪 实验 | `nativesmoke` + 真 LLM | 同上 |
| `roleaudit` | 🧪 实验 | 5 个 scenario 打分每个 role 的工具使用 | 同上 |
| `shadowdiff` | 🧪 实验 | 对比 opencode vs native 两个 runtime | 同上 |
| `evobench` | 🧪 实验 | 合成数据跑 retrieval 质量（含我加的 trainer/judge） | 同上 |
| `embedcheck` | 🧪 实验 | 肉眼验证 embedding 检索 | 同上 |
| `e2erun` | 🧪 实验 | 库级别 end-to-end 走查 | 同上 |
| `dbcheck` | ⚠️ 工具（只有 59 行） | 查 DB 状态 | 并入 `tools/db/` 或删 |
| `dbfix` | ⚠️ 工具（只有 36 行） | 修 DB | 同上 |
| `dbquery` | ⚠️ 工具（只有 21 行） | 查 sessions 表 | 同上（甚至可以直接用 mysql CLI 替代）|

**盘完的真相**：`cmd/` 里 **93% 都不是生产命令**。这就是"为啥看起来乱"的首要原因。

### 顺带提一嘴：backend 根目录下有 5 个编译产物

```
platform/backend/
  evobench.exe           (18 MB)
  planninglive.exe       (18 MB)
  planningsmoke.exe      (19 MB)
  platformlive.exe       (23 MB)
  roleaudit.exe          (21 MB)
  shadowdiff.exe         (9 MB)
  planninglive.log       (30 KB)  ← 某次运行留下的日志
  platformlive.log       (74 KB)  ← 同上
```

**~110 MB 编译产物没进 `.gitignore`**。这些按约定不应该在 repo 里。

---

## 三、`platform/backend/internal/` ——业务代码

| 包 | 角色 | 状态 | 备注 |
|---|---|---|---|
| `config` | ✅ 生产 | 稳 | 配置加载 |
| `model` | ✅ 生产 | 稳 | GORM 数据模型 |
| `middleware` | ✅ 生产 | 稳 | 认证/日志中间件 |
| `llm` | ✅ 生产 | 稳 | Provider 适配（Anthropic + OpenAI-compat） |
| `agent` | ✅ 生产 | 稳 | Agent 管理（manager / role / tools） |
| `agentpool` | ✅ 生产 | 稳 | Agent 池 |
| `runner` | ✅ 生产 | 稳 | Native runtime + 工具派发 + compaction |
| `handler` | ✅ 生产 | **庞大**（28 个文件） | HTTP 路由 handlers |
| `service` | ✅ 生产 | **庞大**（35 个文件） | 业务逻辑层 |
| `repo` | ✅ 生产 | 稳 | 仓库操作 |

**不乱的地方**：`internal/` 实际分层清晰。真正问题是：

**`handler/` 和 `service/` 太大了**。28 + 35 = 63 个文件扁平堆在一起。例如：
- `handler/change.go` 有 23 KB（非常大）
- `handler/llm_endpoint.go` 14 KB
- `handler/dashboard.go` 13 KB
- `service/tool_handler.go` 21 KB
- `service/artifact_context.go` 22 KB（我刚动过的）
- `service/branch.go` 19 KB

**建议**（不是现在做）：
- `handler/` 按域分子包：`handler/task/`, `handler/agent/`, `handler/admin/`
- `service/` 按域分子包：`service/retrieval/`, `service/feedback/`, `service/branch/`
- 但这是**大动作**，等前面两步收敛完再说

---

## 四、`docs/` ——19 份 Markdown，分 3 类

| 类别 | 文件 | 状态 |
|---|---|---|
| 📘 **正式规范**（`docs/dev/`） | `01_overview` → `13_frontend` | ✅ 保留，是你的项目圣经 |
| 📋 **阶段报告** | `phase1-complete.md`, `phase2-streaming.md` + `phase1/`, `phase2/`, `phase3/` 子目录 | 🧹 合并成 CHANGELOG 或归档到 `docs/archive/` |
| 📝 **草稿** | `evolvable/evolution-plan.md` (37 KB!), `evolvable/industry-reference.md`, `handoff/NEXT_PHASE_HANDOFF.md`, `handoff/PROJECT_HANDOFF_COMPLETE.md`, `agent-replacement-assessment.md`, `migration-runbook.md`, `refinery.md` | ⚠️ 混在一起看不出来哪个是权威的 |

**最关键的问题**：外部贡献者/用户读你文档的**入口不清楚**。没有一句"如果你是第一次看这个项目，从 `docs/dev/01_overview.md` 开始"。

---

## 五、`frontend/` ——Dashboard

```
frontend/
├── src/           ← React + TypeScript + Tailwind
├── dist/          ← 构建产物
├── package.json
└── vite.config.ts
```

状态：✅ **技术栈正规**（React + Vite + TS + Tailwind）。没有深入看代码，但结构像模像样。

**根目录散落着 4 个 mockup HTML**：
```
coai2/
  frontend-mockup-current.html   (11 KB)
  frontend-mockup-office.html    (16 KB)
  frontend-mockup-premium.html   (49 KB)
  frontend-mockup-refined.html   (44 KB)
```

⚠️ 这些是设计稿。**应该搬到 `docs/design/mockups/` 或删掉**——它们躺在根目录让项目看起来杂。

---

## 六、`client/` ——客户端 SDK，**最重要的空洞**

```
client/
├── mcp/       ← MCP 客户端（Node.js，有 package.json、send-msg.js）
└── skill/     ← 空目录
```

**现状**：只有 MCP 客户端的骨架，两个极小的 `send-msg.js/.mjs`（每个 < 1 KB）。

**这是你说的"客户端只需接上就自动领任务"这件事的**实际归宿**。但现在几乎不存在。**

建议：
- 重命名 `client/` → `sdks/`
- 设计 `sdks/python/`、`sdks/go/`、`sdks/mcp/`
- 先定义**客户端接入契约**（3-5 个 HTTP endpoint），再写 SDK

---

## 七、根目录 ——散落杂物

| 文件 | 分类 | 建议 |
|---|---|---|
| `README.md`, `README.zh.md`, `CHANGELOG.md`, `LICENSE` | ✅ | 保留 |
| `docker-compose.yml` | ✅ | 保留 |
| `frontend-mockup-*.html` (4 个) | 🧹 | 搬 `docs/design/` 或删 |
| `test_full_flow.ps1`, `test_phase3_e2e.ps1`, `test_phase3b_behavior.ps1` | ⚠️ | 搬 `scripts/` 或 `tests/shell/` |
| `roleaudit-output-v2.txt` (24 KB), `roleaudit-output.txt` (23 KB) | 🧹 | 单次运行的输出，应该删 / 归档 |
| `start.ps1` | ✅ | 保留（启动脚本） |

---

## 八、不在列但值得注意

- `platform/embedder/` —— Python BGE 嵌入服务，**单文件 `app.py` + requirements.txt**，很干净，不乱
- `platform/data/` —— 数据目录
- `platform/backend/tests/` —— **32 个 `test_*.json`**，看起来是手动 curl 测试用的请求体。**应该归到一个 fixture 子目录**

---

## 九、总结 ——杂乱感的来源排行

1. **🔴 最严重**：`cmd/` 里 13 个非生产命令跟生产 `server` 挤一起，看起来像 14 个平级产品
2. **🟠 次严重**：根目录散落 4 个 mockup.html + 2 个 roleaudit 输出 txt + 5 个 .exe + 2 个 .log
3. **🟡 中等**：文档没有入口，`docs/evolvable/` 这种草稿和 `docs/dev/` 权威规范混着放
4. **🟡 中等**：`client/` 作为"客户端 SDK"目录名不清晰，且几乎是空的
5. **🟢 不急**：`handler/` 和 `service/` 文件太多太大，但不影响外部观感

---

## 十、建议的下一步（不做，只列）

这份盘点不写结论，**下一步怎么走你选**。但如果让我按价值×工作量排序：

| 行动 | 工作量 | 产出 |
|---|---|---|
| **A. 搬 cmd/**：把 13 个非生产命令搬到 `cmd/tools/` 或顶层 `experiments/`，`cmd/` 只留 `server` | 1 小时 | 外部观感立刻清爽 |
| **B. 清根目录**：mockups → `docs/design/`；`.exe`、`.log` 加 gitignore 删除；roleaudit 输出 txt 归档 | 30 分钟 | 根目录立刻清爽 |
| **C. docs 分家**：把 `phase*`、`handoff/`、`evolvable/` 都塞进 `docs/archive/`，只留 `docs/dev/` 和一个 `docs/README.md` 做入口 | 1 小时 | 文档有了清晰入口 |
| **D. 写客户端契约**：`docs/client-protocol.md` 一页纸写明 3-5 个 endpoint，客户端 SDK 就照这个写 | 半天（你和我一起想） | 最终端用户接入的基石 |
| **E. 砍 dbcheck/dbfix/dbquery**：这 3 个小工具（总共 116 行）直接删或合并进一个 `cmd/tools/db` | 30 分钟 | 少 3 个伪命令 |

**ABC 是纯整理，没有风险。D 是战略决策，需要你一起想。E 是小决定。**

---

*盘点完。不改代码。你想从哪条先开刀告诉我。*
