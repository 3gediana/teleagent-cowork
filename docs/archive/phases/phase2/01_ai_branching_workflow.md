# Phase 2: AI 分支工作流 (AI Branching Workflow)

## 1. 核心概念：引入"沙盒（Branch）"

为了支持大规模重构和复杂特性开发，A3C 平台引入与 Git 原生对应的分支机制。

- **主干（`main`）**：公共大厅，适合微小修复和日常迭代。
- **特性分支（Feature Branch）**：独立沙盒空间，AI 可以自由修改大量文件，不阻塞 main。

### 分支规则

1. 任何 agent 都可以开分支，但项目活跃分支上限 3 个
2. 一个分支同时只能有一个 agent（其他 agent 尝试进入 → 报错）
3. 任何 agent 都可以进入任何空闲分支（不限于创建者）
4. 分支持久存在，不设自动超时关闭，由人类或 AI 手动关闭
5. 分支内无审核，agent 自由修改
6. 分支 filelock 不影响 main 和其他分支

## 2. MCP 工具集扩展

### 新增工具

| 工具名称 | 功能说明 | 输入参数 |
| :--- | :--- | :--- |
| `select_branch` | 选择进入分支（进入后才能用分支工具） | `branch_id` |
| `branch.change_submit` | 分支内提交改动（无审核） | `writes, deletes, description` |
| `branch.file_sync` | 同步分支文件 | 无 |
| `pr.submit` | 提交 PR（含自评） | `title, description, self_review` |

### select_project 返回值变更

选择项目后，返回值新增分支列表，显示每个分支的状态和占用者。

### 工具行为变化

- `filelock`：锁的生效范围从全局变为**分支内（Per-Branch）**
- `change.submit`：agent 在分支上时调用会报错，需用 `branch.change_submit`
- `file.sync`：agent 在分支上时自动返回分支文件

## 3. PR 流程

### 三层审核

1. **人类确认评估**：PR 提交后闲置，人类同意后才触发评估
2. **评估 agent（技术）**：diff 统计、dry-run merge 冲突检测、代码质量审查
3. **维护 agent（业务）**：里程碑完成度、方向一致性、版本号建议
4. **人类确认合并**：看到两个 agent 的评估报告后最终决定

### PR 自评要求

agent 提交 PR 时必须自评，精确到函数级别：
- 哪个文件的哪个函数改了（added/modified/removed）
- 对其他文件的影响
- 整体影响评估
- 合并信心（high/medium/low）

### 合并方式

快照合并（`--no-commit` 试合并，成功才提交，失败就 abort）：
- 无冲突 → 直接提交
- 简单冲突 → 合并 agent 自动解决
- 复杂冲突 → abort，通知人类

## 4. 平台 Agent 职责进阶

### 4.1 评估 Agent（新增）
- **触发条件**：人类确认评估 PR 后
- **职责**：技术评估（diff + dry-run merge + 代码审查）
- **输出**：评估报告（合并成本评级 + 冲突文件 + 风险提示）

### 4.2 合并 Agent（新增）
- **触发条件**：人类最终确认合并后
- **职责**：执行 git merge，解决简单冲突，复杂冲突回退
- **安全机制**：`--no-commit` 试合并，失败则 `--abort`

### 4.3 维护 Agent（职责变更）
- **监听范围收窄**：不再关心分支内的变动，仅监听 main
- **新增 PR 业务评估**：判断 PR 是否完成里程碑任务，建议版本号升级方式
- **触发条件**：PR 合并成功后更新方向/里程碑

### 4.4 审核 Agent（不变）
- 继续负责 main 上的 `change.submit` 常规审核

## 5. main 更新时的分支同步

- main 每次版本更新时，现有 `VERSION_UPDATE` 广播已覆盖
- 分支上的 agent 通过 poll 收到广播
- Agent 自主决定是否 `branch.sync_main`（将 main 合入分支）
- PR 提交时平台强制做 dry-run merge 检测冲突
- 如有冲突，agent 必须先 sync_main 解决冲突才能重新提交 PR
