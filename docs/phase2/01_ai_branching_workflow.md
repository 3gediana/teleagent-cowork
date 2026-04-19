# Phase 2: AI 分支工作流 (AI Branching Workflow)

## 1. 核心概念：引入“沙盒（Branch）”
为了支持大规模重构和复杂特性开发，A3C 平台将在二期引入与 Git 原生对应的分支机制。
- **主干（`main`）**：依然作为公共大厅，适合微小修复和日常迭代。
- **特性分支（Feature Branch）**：独立的沙盒空间。AI 可以在独立的沙盒中修改大量文件，而不阻塞 `main` 分支的其他协作者。

## 2. MCP 工具集扩展

为了支持分支操作，扩展以下 MCP 工具：

| 工具名称 | 功能说明 | 输入参数 |
| :--- | :--- | :--- |
| `branch.create` | 基于 `main` 创建一个新分支（如 `feature-login`）。 | `branch_name` |
| `branch.switch` | 切换当前 Agent 的工作区到指定分支。切换后，`file.sync` 仅同步该分支的代码。 | `branch_name` |
| `pr.submit` | 提交合并请求（Pull Request），申请将当前分支合入 `main`。 | `title`, `description` |

**工具行为变化**：
- `filelock`：锁的生效范围从全局变为**分支内（Per-Branch）**。在分支 A 锁定文件，不影响主干或其他分支锁定同一文件。
- `change.submit`：改动只合入当前 Agent 所在的活跃分支。

## 3. 平台 Agent 职责进阶

### 3.1 审核 Agent（“架构师”）
- **日常审核**：继续负责分支内 `change.submit` 的常规代码审核，防止语法破坏。
- **PR 审核（新增）**：响应 `pr.submit`，对比特性分支与 `main` 分支的差异，进行全局架构级别的代码审查。如果存在严重冲突（`main` 已被其他人大幅修改），将拒绝 PR，要求 Agent 先去同步 `main` 解决冲突后再提交。

### 3.2 维护 Agent（“产品经理”）
- **监听范围收窄**：不再关心各个 Feature 分支内部细碎的 `change.submit`。
- **触发条件变更**：仅监听 `main` 分支的变动。当巨型 PR 被成功合并入 `main` 时被唤醒，负责更新全局的【方向块】和【里程碑块】，并向全服发送广播通知大版本升级。
