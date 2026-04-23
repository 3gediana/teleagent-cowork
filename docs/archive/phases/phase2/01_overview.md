## 一、Phase 2 概述

### 1.1 核心目标

在 MVP（Phase 1）基础上引入 **Git 分支工作流**，支持大规模重构和复杂特性开发。

### 1.2 Phase 1 的局限

| 问题 | 说明 |
|------|------|
| 所有改动走 main | 大改动会阻塞其他协作者 |
| filelock 全局生效 | 锁了文件其他人就不能改，即使改的是不同功能 |
| 审核 agent 压力大 | 大改动和小改动走同样的审核流程，效率低 |
| maintain agent 被过度唤醒 | 每个 change 都触发，不管多小 |

### 1.3 Phase 2 解决思路

引入 **分支（Branch）** 作为独立沙盒：
- 大改动在分支上做，不阻塞 main
- 分支内自由修改，不需要审核
- 完成后通过 PR 提交，经多层审核后合入 main
- filelock 范围限定到分支内，不影响 main 和其他分支

---

## 二、核心概念

### 2.1 主干 vs 分支

| | 主干 (main) | 分支 (feature/xxx) |
|---|---|---|
| 定位 | 公共大厅，小改动直合 | 独立沙盒，大改动专用 |
| 审核 | change.submit → 审核 agent | 无审核，自由修改 |
| filelock | 全局生效 | 分支内生效，不影响其他 |
| maintain agent | 监听所有变动 | 不关心分支内变动 |
| 版本号 | 每次审核通过 +1 | 无独立版本号 |

### 2.2 分支规则

1. **谁可以开分支**：任何 agent，但需要平台校验（活跃分支上限 3 个）
2. **一个分支同时只能有一个 agent**：其他 agent 尝试进入 → 报错
3. **任何 agent 都可以进入任何空闲分支**：不限于创建者
4. **分支持久存在**：不设自动超时关闭，由人类或 AI 手动关闭
5. **分支内无审核**：agent 自由修改，不需要审核 agent 介入
6. **分支 filelock 不影响 main**：锁范围限定在分支内

### 2.3 PR 规则

1. **PR 提交时 agent 必须自评**：精确到哪个文件的哪个函数改了，对其他文件的影响
2. **PR 不直接送审核 agent**：先闲置，等人类同意后才触发评估
3. **三层审核**：人类确认 → 评估 agent（技术）→ 维护 agent（业务）→ 人类最终确认合并
4. **合并方式**：快照合并（`--no-commit` 试合并，成功才提交，失败就 abort）

---

## 三、用户端 MCP 工具变更

### 3.1 现有工具（不变）

| 工具 | 说明 | 备注 |
|------|------|------|
| `a3c_platform` | 登录/登出 | 不变 |
| `task` | 领取/完成任务 | 不变 |
| `filelock` | 锁定/解锁文件 | 不变，但锁范围自动限定到当前分支 |
| `change.submit` | 提交改动（main） | 不变，仅 main 上可用 |
| `file.sync` | 同步文件（main） | 不变，仅同步 main 文件 |
| `status.sync` | 获取状态 | 不变 |
| `project_info` | 咨询项目信息 | 不变 |

### 3.2 新增工具

| 序号 | 工具名称 | 功能 | AI 填写参数 | 返回值 |
|------|----------|------|------------|--------|
| 1 | `select_branch` | 选择进入分支 | branch_id | 分支上下文 + 文件 |
| 2 | `branch.change_submit` | 分支内提交改动 | writes, deletes, description | 提交结果 |
| 3 | `branch.file_sync` | 同步分支文件 | - | 分支文件列表 |
| 4 | `pr.submit` | 提交 PR | title, description, self_review | PR 状态 |

### 3.3 select_project 返回值变更

登录并选择项目后，返回值新增分支信息：

```typescript
interface SelectProjectResponse {
  success: boolean;
  project_context: {
    id: string;
    name: string;
    direction: string;
    milestone: string;
    version: string;
    // 新增
    branches: {
      id: string;
      name: string;
      status: 'active' | 'merged' | 'closed';
      occupied_by: string | null;  // 当前在分支上的 agent 名字，null=空闲
    }[];
  };
}
```

### 3.4 select_branch 参数说明

```typescript
interface SelectBranch {
  branch_id: string;
}

interface SelectBranchResponse {
  success: boolean;
  branch_context: {
    id: string;
    name: string;
    base_version: string;       // 分支创建时的 main 版本
    files: FileInfo[];           // 分支当前文件
  };
  error?: {
    code: 'BRANCH_NOT_FOUND' | 'BRANCH_OCCUPIED' | 'BRANCH_CLOSED';
    message: string;
    occupied_by?: string;        // 谁在用这个分支
  };
}
```

**关键行为**：
- 进入分支后，`filelock` 和 `file.sync` 自动限定到该分支
- 未进入分支时调用 `branch.*` 工具 → 报错 `BRANCH_NOT_ENTERED`
- 进入分支后调用 `change.submit`（main 专用）→ 报错 `USE_BRANCH_CHANGE_SUBMIT`
- 退出分支：调用 `select_project` 重新选择项目即回到 main

### 3.5 branch.change_submit 参数说明

```typescript
interface BranchChangeSubmit {
  description?: string;
  writes: (string | { path: string; content: string })[];
  deletes?: string[];
}

interface BranchChangeSubmitResponse {
  success: boolean;
  message: string;  // "改动已写入分支"
}
```

**与 change.submit 的区别**：
- 无 `task_id`（分支内不强制关联任务）
- 无 `version`（分支内不做版本检查）
- 无审核流程（直接写入分支工作区）
- 改动写入分支的 git repo，不影响 main

### 3.6 branch.file_sync 参数说明

```typescript
interface BranchFileSync {
  // 无参数
}

interface BranchFileSyncResponse {
  success: boolean;
  branch_id: string;
  files: FileInfo[];
  message: string;
}
```

### 3.7 pr.submit 参数说明

```typescript
interface PRSubmit {
  title: string;
  description: string;
  self_review: {
    changed_functions: {
      file: string;
      function: string;
      change_type: 'added' | 'modified' | 'removed';
      impact: string;           // 对其他文件的影响描述
    }[];
    overall_impact: string;     // 整体影响评估
    merge_confidence: 'high' | 'medium' | 'low';  // 合并信心
  };
}

interface PRSubmitResponse {
  success: boolean;
  pr: {
    id: string;
    status: 'pending_human_review';  // 闲置，等人类确认
    title: string;
  };
  error?: {
    code: 'BRANCH_EMPTY' | 'BRANCH_NOT_ENTERED' | 'PR_ALREADY_EXISTS';
    message: string;
  };
}
```

**self_review 是必填项**：agent 必须在提交 PR 时自评改动内容，精确到函数级别。

---

## 四、平台端 Agent 变更

### 4.1 评估 Agent（新增角色）

| 属性 | 说明 |
|------|------|
| 触发条件 | 人类确认评估 PR 后 |
| 职责 | 技术评估：diff 统计、合并冲突检测、代码质量审查 |
| 输出 | 评估报告（合并成本评级 + 冲突文件列表 + 风险提示） |

**评估内容**：
1. `git diff main...feature/xxx --stat` → 改动统计
2. `git merge --no-commit --no-ff feature/xxx` → dry-run 合并检测冲突
3. 代码质量审查（架构影响、性能、安全）
4. 合并成本评级：低（<10 文件，无冲突）/ 中（10-30 文件或简单冲突）/ 高（>30 文件或复杂冲突）

**评估结果**：
| 结果 | 含义 | 下一步 |
|------|------|--------|
| approved | 技术上可以合并 | 交给维护 agent 做业务评估 |
| needs_work | 有问题但可修 | 通知 agent 继续在分支上改 |
| conflicts | 合并冲突 | 通知 agent 先 sync_main |
| high_risk | 高风险，不建议自动合并 | 人类决定 |

### 4.2 维护 Agent（职责变更）

| 变更 | 原来 | 现在 |
|------|------|------|
| 监听范围 | 所有 change.submit | 仅 main 上的变动 |
| PR 业务评估 | 无 | 新增：判断 PR 是否完成里程碑任务 |
| 版本号建议 | 无 | 新增：建议小版本 +1 还是里程碑跳 |

**PR 业务评估内容**：
1. PR 是否完成了当前里程碑中的任务
2. 方向是否与 direction 一致
3. 建议版本号升级方式（小版本 vs 里程碑跳）

### 4.3 合并 Agent（新增角色）

| 属性 | 说明 |
|------|------|
| 触发条件 | 人类最终确认合并后 |
| 职责 | 执行 git merge，解决简单冲突 |
| 安全机制 | `--no-commit` 试合并，失败则 `--abort` |

**合并流程**：
```
1. git merge --no-commit --no-ff feature/xxx
2. 检查结果
   ├─ 无冲突 → git commit → 成功
   ├─ 简单冲突（不同区域）→ 自动解决 → git add → git commit → 成功
   └─ 复杂冲突 → git merge --abort → 通知人类
3. 成功后：
   - git tag 新版本（由维护 agent 建议的版本号）
   - 广播 VERSION_UPDATE
   - 分支状态 → merged
   - 释放分支上的所有 filelock
   - agent 回到 main
```

### 4.4 审核 Agent（不变）

继续负责 main 上的 `change.submit` 常规审核，不参与分支和 PR 流程。

---

## 五、PR 完整流程

```
Agent 在分支上工作（多次 branch.change_submit，无审核）
  ↓
Agent 调用 pr.submit（含 self_review）
  ↓
PR 状态: pending_human_review（闲置）
  ↓
人类在 dashboard 看到 PR 摘要 + self_review
  ↓
人类点击"同意评估"
  ↓
PR 状态: evaluating
  ↓
评估 agent 技术评估（diff + dry-run merge + 代码审查）
  ↓
PR 状态: evaluated（技术）
  ↓
维护 agent 业务评估（里程碑完成度 + 方向一致性 + 版本号建议）
  ↓
PR 状态: evaluated（业务）
  ↓
人类看到两个 agent 的评估报告
  ↓
人类点击"确认合并" / "拒绝" / "要求修改"
  ↓
确认合并 → 合并 agent 执行 git merge
  ├─ 成功 → 版本升级 + 广播 + 分支关闭
  └─ 失败 → 通知人类，PR 回到 evaluated 状态
```

---

## 六、数据模型变更

### 6.1 新增 Branch 表

```sql
CREATE TABLE branch (
  id VARCHAR(64) PRIMARY KEY,
  project_id VARCHAR(64) NOT NULL,
  name VARCHAR(128) NOT NULL,           -- feature/alice-login-a3f1
  base_commit VARCHAR(64),              -- 创建时的 main commit hash
  base_version VARCHAR(8),              -- 创建时的 main 版本号
  status ENUM('active', 'merged', 'closed') DEFAULT 'active',
  creator_id VARCHAR(64) NOT NULL,      -- 创建者 agent_id
  occupant_id VARCHAR(64),              -- 当前在分支上的 agent_id（NULL=空闲）
  last_active_at DATETIME,
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
  merged_at DATETIME,
  closed_at DATETIME,
  FOREIGN KEY (project_id) REFERENCES project(id),
  FOREIGN KEY (creator_id) REFERENCES agent(id),
  FOREIGN KEY (occupant_id) REFERENCES agent(id)
);
```

### 6.2 新增 PullRequest 表

```sql
CREATE TABLE pull_request (
  id VARCHAR(64) PRIMARY KEY,
  project_id VARCHAR(64) NOT NULL,
  branch_id VARCHAR(64) NOT NULL,
  title VARCHAR(256) NOT NULL,
  description TEXT,
  self_review JSON NOT NULL,            -- agent 自评（函数级改动 + 影响）
  diff_stat TEXT,                        -- git diff --stat
  diff_full TEXT,                        -- git diff full
  status ENUM('pending_human_review', 'evaluating', 'evaluated', 'pending_human_merge', 'merged', 'rejected', 'merge_failed') DEFAULT 'pending_human_review',
  submitter_id VARCHAR(64) NOT NULL,
  tech_review JSON,                      -- 评估 agent 技术评估结果
  biz_review JSON,                       -- 维护 agent 业务评估结果
  version_suggestion VARCHAR(8),         -- 维护 agent 建议的版本号
  conflict_files JSON,                   -- 冲突文件列表
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
  merged_at DATETIME,
  FOREIGN KEY (project_id) REFERENCES project(id),
  FOREIGN KEY (branch_id) REFERENCES branch(id),
  FOREIGN KEY (submitter_id) REFERENCES agent(id)
);
```

### 6.3 Agent 表新增字段

```sql
ALTER TABLE agent ADD COLUMN current_branch_id VARCHAR(64);
-- NULL = 在 main 上工作
```

### 6.4 Change 表新增字段

```sql
ALTER TABLE change ADD COLUMN branch_id VARCHAR(64);
-- NULL = main 上的 change
```

### 6.5 FileLock 表新增字段

```sql
ALTER TABLE file_lock ADD COLUMN branch_id VARCHAR(64);
-- NULL = main 上的锁
```

---

## 七、API 设计

### 7.1 新增 API

| 路径 | 方法 | 说明 | 权限 |
|------|------|------|------|
| `/branch/create` | POST | 创建分支 | agent（需校验上限） |
| `/branch/enter` | POST | 进入分支 | agent（需校验是否被占用） |
| `/branch/leave` | POST | 离开分支回到 main | agent |
| `/branch/list` | GET | 列出项目所有分支 | agent |
| `/branch/close` | POST | 关闭分支 | agent 或人类 |
| `/branch/sync_main` | POST | 将 main 合入当前分支 | agent |
| `/branch/change_submit` | POST | 分支内提交改动 | agent（需在分支内） |
| `/branch/file_sync` | GET | 同步分支文件 | agent（需在分支内） |
| `/pr/submit` | POST | 提交 PR | agent（需在分支内） |
| `/pr/list` | GET | 列出项目所有 PR | agent |
| `/pr/approve_review` | POST | 人类同意评估 PR | 人类 |
| `/pr/approve_merge` | POST | 人类确认合并 PR | 人类 |
| `/pr/reject` | POST | 人类拒绝 PR | 人类 |

### 7.2 变更 API

| 路径 | 变更说明 |
|------|---------|
| `/auth/select-project` | 返回值新增 branches 列表 |
| `/change/submit` | 如果 agent 在分支上 → 报错 `USE_BRANCH_CHANGE_SUBMIT` |
| `/filelock/acquire` | 自动关联 agent 当前分支，锁范围限定到分支内 |
| `/file/sync` | 如果 agent 在分支上 → 返回分支文件 |
| `/status/sync` | 返回值新增分支信息 |

---

## 八、Git 操作设计

### 8.1 分支创建

```bash
# 平台 repo 始终在 main 上
git checkout main
git checkout -b feature/{name}
# 分支工作区：data/projects/{id}/branches/{branch_id}/repo/
# 用 git worktree 实现，主 repo 和分支各自独立目录
```

### 8.2 分支内提交

```bash
cd data/projects/{id}/branches/{branch_id}/repo/
git add -A
git commit -m "[branch:{name}] {description}"
```

### 8.3 PR diff 生成

```bash
cd data/projects/{id}/repo/  # main repo
git diff main...feature/{name} --stat   # 统计
git diff main...feature/{name}          # 全量 diff
```

### 8.4 快照合并

```bash
cd data/projects/{id}/repo/
# 1. 试合并
git merge --no-commit --no-ff feature/{name}
# 2a. 成功 → 提交
git commit -m "Merge feature/{name}"
# 2b. 失败 → 回退
git merge --abort
```

### 8.5 sync_main（将 main 合入分支）

```bash
cd data/projects/{id}/branches/{branch_id}/repo/
git merge main
# 如有冲突 → 返回冲突文件列表给 agent
```

---

## 九、开发规划

### 9.1 开发顺序

| 步骤 | 内容 | 依赖 |
|------|------|------|
| P0 | Branch 数据模型 + 迁移 | 无 |
| P0 | Git worktree 分支工作区管理 | Branch 模型 |
| P0 | branch.create/enter/leave/list/close API | worktree |
| P1 | Agent.CurrentBranchID + 分支感知（filelock/filesync/change 拦截） | P0 |
| P1 | branch.change_submit + branch.file_sync API | P1 |
| P1 | select_project 返回分支列表 + select_branch MCP 工具 | P1 |
| P2 | PullRequest 数据模型 | P1 |
| P2 | pr.submit API + self_review 处理 | PR 模型 |
| P2 | 评估 agent（技术评估 + dry-run merge） | PR 模型 |
| P3 | 维护 agent PR 业务评估 | 评估 agent |
| P3 | 合并 agent（快照合并 + 冲突处理） | 评估 agent |
| P3 | 人类 PR 审批 UI（dashboard） | 合并 agent |
| P4 | branch.sync_main API | P0 |
| P4 | MCP 工具扩展（pr.submit, branch.*） | P2 |
| P4 | VERSION_UPDATE 广播增加分支感知 | P1 |

### 9.2 预估工作量

| 模块 | 预估 |
|------|------|
| 后端 Go | Branch/PR 模型 + API + Git 操作 |
| 平台 Agent | 评估 agent + 合并 agent + 维护 agent 变更 |
| MCP 客户端 | select_branch + branch.* + pr.submit |
| 前端 | PR 审批 UI + 分支列表展示 |
