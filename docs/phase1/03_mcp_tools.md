## 五、MCP 工具集设计

### 5.1 设计原则

1. **工具最少化**：不超过 10 个工具
2. **用户端轻量化**：用户端只负责改代码和声明操作，不维护状态
3. **平台端智能化**：平台端 Agent 专门负责维护和分析
4. **AI 专注工作**：减少 AI 被打扰，方向/进度由平台广播，不需要 AI 主动查询

### 5.2 用户端工具（9 个）

| 工具名称 | 功能 | 输入参数 | 返回值 |
|----------|------|----------|--------|
| `a3c_platform` | 登录平台、切换项目 | action, platform_url, name, project? | 连接状态 |
| `task.create` | 创建任务 | name, description, related_files[], priority? | task_id |
| `task.claim` | 领取任务 | task_id | success |
| `task.complete` | 完成任务 | task_id | success |
| `filelock` | 锁定/解锁文件（声明） | action, files[] | success |
| `change.submit` | 提交改动 | files[], description?, task_id | change_id |
| `status.sync` | 同步任务/锁状态 | - | {tasks, locks} |
| `file.sync` | 同步文件 | version? | {version, operations} |
| `project_info` | 咨询项目信息 | query | {answer} |

#### 5.2.1 a3c_platform 参数说明

```typescript
interface A3CPlatform {
  action: 'login' | 'logout';
  platform_url: string;
  name: string;                 // Agent名字，名字冲突返回错误
  project?: string;            // 项目名称，进入项目时用
}
```

**登录流程**：

| 操作 | platform_url | name | project | 说明 |
|------|--------------|------|---------|------|
| `login` | ✅ | ✅ | - | 首次连接，返回所有项目列表 |
| `login` | ✅ | ✅ | ✅ | 进入指定项目，返回项目完整上下文 |
| `logout` | ✅ | ✅ | - | 断开连接，停止心跳 |

**返回数据**：

| 场景 | 返回内容 |
|------|----------|
| 登录（无project） | 所有项目列表（大厅状态） |
| 登录（带project） | 项目完整上下文（方向、进度、任务、锁） |
| 名字冲突 | 错误信息 |

#### 5.2.2 task.create 参数说明

```typescript
interface TaskCreate {
  name: string;
  description: string;
  related_files: string[];      // 必填，评估任务影响范围后填写
  priority?: 'high' | 'medium' | 'low';
}
```

**校验规则**：

| related_files | 行为 |
|---------------|------|
| 有值 | 正常创建任务 |
| 空数组 `[]` | 返回提示：先去评估任务影响范围 |

**任务数据结构**：

```typescript
interface Task {
  id: string;
  name: string;
  description: string;
  related_files: string[];       // 可空（维护Agent创建时）
  priority?: 'high' | 'medium' | 'low';
  status: 'pending' | 'claimed' | 'completed';
  created_by: string;            // 填入登录时的name
  created_at: datetime;
}
```

#### 5.2.3 task.claim / task.complete 参数说明

```typescript
interface TaskClaim {
  task_id: string;
}

interface TaskComplete {
  task_id: string;
}
```

#### 5.2.4 filelock 参数说明

```typescript
interface Filelock {
  action: 'acquire' | 'release';
  files: string[];              // 文件路径列表
}
```

**锁定机制补充**：
- **默认 TTL**：文件锁默认 4 小时过期。
- **心跳续租**：后台 Poller 脚本每次轮询时自动续租当前持有的锁。
- **断线释放**：Agent 下线（Poller 停止）后，心跳停止，锁在 TTL 到期后自动释放。

#### 5.2.5 change.submit 参数说明

```typescript
interface ChangeSubmit {
  files: {
    path: string;
    type: 'modified' | 'new' | 'deleted' | 'new_dir';
    desc?: string;              // new/new_dir时用途描述（可选）
  }[];
  description?: string;         // 改动说明（可选）
  task_id: string;
}
```

**type说明**：

| type | 说明 | 必要参数 |
|------|------|----------|
| `modified` | 修改文件 | path |
| `new` | 新增文件 | path, desc（可选） |
| `deleted` | 删除文件 | path |
| `new_dir` | 新增目录 | path, desc（可选） |

**说明**：
- 工具内部实现**两阶段提交**，确保原子性（对 Agent 透明）：
  1. 生成唯一 change_id。
  2. 逐个上传文件内容到平台暂存区。
  3. 全局确认提交，平台将该 change_id 标记为待审核。若中途失败则清理所有临时文件。
- 平台从 Git 拉取原始文件，生成 diff 后供 Audit Agent 审核。
- 平台缓存文件，审核完成后清理。

#### 5.2.6 status.sync 参数说明

```typescript
interface StatusSync {
  // 无参数
}
返回: { tasks, locks }  // 返回全部任务和锁状态
```

#### 5.2.7 file.sync 参数说明

```typescript
interface FileSync {
  version?: string;    // 用户端当前版本号（Git Hash，可选）
}

// 平台返回
interface FileSyncResponse {
  version: string;     // 最新版本号（Git Hash）
  synced: {            // 已同步的文件操作列表
    type: 'write' | 'delete';
    path: string;
    content?: string;  // type='write'时返回
  }[];
  skipped: string[];   // 被跳过的文件（当前Agent已锁定）
  conflicts: {         // 潜在冲突告警（已锁定但平台已更新）
    file: string;
    platform_hash: string;
  }[];
}

// MCP 工具内部流程
// 1. 读取本地 .a3c_version 文件（存储 Git commit hash）
// 2. 发送版本号给平台
// 3. 平台对比差异 (git diff client_version HEAD)
// 4. 平台过滤掉当前 Agent 已经锁定的文件，仅对未锁定文件生成 synced 列表
// 5. MCP 工具执行 synced 列表中的操作（覆盖本地，确保本地非锁定文件始终最新）
// 6. 若存在 conflicts，向 Agent 输出告警提示可能有冲突
// 7. 更新 .a3c_version 文件
```

**说明**：
- **跳过锁定文件**：保证 Agent 正在修改的文件不被覆盖，未锁定文件会被自动覆盖。
- **版本控制**：版本号使用 Git commit hash 前 8 位，首次同步时版本号为空，平台返回除锁定文件外的全量文件。
- **二进制文件**：MVP 阶段暂不支持二进制文件同步。

#### 5.2.8 project_info 参数说明

```typescript
interface ProjectInfo {
  query: string;    // 自然语言提问
}

// 返回
interface ProjectInfoResponse {
  answer: string;   // 咨询 Agent 的回答
}
```

**查询示例**：
- "v1.2.3 做了什么修改？"
- "当前文件结构是什么？"
- "方向块内容是什么？"

#### 5.2.9 操作类型说明

| 操作 | 类型 | 处理方 |
|------|------|--------|
| task.claim/complete | 原子操作 | 平台直接处理 |
| filelock.acquire/release | 原子操作 | 平台直接处理 |
| task.create | **决策操作** | 维护Agent决定是否创建（根据用户输入或审核通过的改动） |
| change.submit | 分析操作 | 进入审核队列，由审核Agent处理 |

### 5.3 平台端 Agent

平台端 Agent 基于 OpenCode 框架实现，支持无头模式运行和 session 管理。

#### 5.3.1 OpenCode 框架

**特性**：
- 无头模式：`opencode run` 非交互式运行
- Session 机制：通过 session ID 保持多轮对话
- 自定义工具：平台自定义工具供 Agent 调用
- 权限配置：限制 Agent 可使用的工具

**技术实现**：
- 平台通过 OpenCode SDK 管理 session
- 平台捕获 Agent 的工具调用，返回结果继续对话
- Agent 完成任务后输出最终结果

#### 5.3.2 审核 Agent

| 项目 | 说明 |
|------|------|
| **框架** | OpenCode |
| **职责** | 判断改动方向是否符合项目整体方向，判断文件冲突 |
| **输入** | diff + 新文件内容（只看提交相关文件） |
| **输出** | approve / reject + 冲突处理建议 |
| **上下文** | 不需要跨任务维护，每次独立 |

**审核流程**：
```
change.submit
    ↓
平台存储文件到 pending/ 目录
    ↓
平台从 Git 拉取当前版本，生成 diff
    ↓
启动审核 Agent
    ↓
注入 prompt：diff + 新文件内容 + 项目方向
    ↓
审核 Agent 分析代码
    ↓
调用 audit_output 工具
    ↓
平台捕获输出
```

**冲突处理**：
| 情况 | 处理 |
|------|------|
| 修改不同部分 | 都通过，合并 |
| 修改同一部分 | 返回错误给后提交者 |
| 新增同一文件 | 返回错误给后提交者 |

**自定义工具**：

```typescript
audit_output({
  result: 'approve' | 'reject',  // 审核结果
  reason?: string,                // 拒绝原因
  conflicts?: string[]            // 冲突文件列表（可选）
})
```

#### 5.3.3 维护 Agent

| 项目 | 说明 |
|------|------|
| **框架** | OpenCode |
| **职责** | 维护项目执行路径（thought/task），以及对人类方向输入进行结构化整理 |
| **输入** | 项目上下文、看板内容 |
| **输出** | 更新方向块/思路块、决定是否创建新任务 |
| **工具** | OpenCode 内置工具 |

**维护流程**：
```
用户通过看板输入想法/问题/建议
    ↓
激活维护 Agent
    ↓
分析用户输入
    ↓
决策：
  - 用户要改方向 → 更新方向块 + 广播
  - 用户提建议 → 可能创建新任务
  - 用户提问题 → 可能创建新任务
    ↓
更新文本块
    ↓
如需创建任务 → 通知平台创建任务
    ↓
平台检测变更，广播通知
```

#### 5.3.X 方向修改行为约束（强约束）

维护Agent在方向块（direction）上的行为必须遵循以下规则：

1. **默认行为**
   - 不得修改 direction
   - 所有 change.submit 触发的更新，仅允许修改 thought（思路块）

2. **唯一允许触发方向修改的场景**
   - 来源必须为人类看板输入（方向看板）
   - 不允许基于代码变更或任务完成自动推断方向变化

3. **行为模式**
   当检测到方向看板输入时：

```
Step 1: 解析人类输入（可能是模糊描述）
Step 2: 主动提问补全信息（目标、约束、非目标等）
Step 3: 输出结构化 proposed_direction
Step 4: 等待人类确认
Step 5: 确认后由平台应用（非Agent直接修改）
```

4. **禁止行为**
   - ❌ 基于 change.submit 修改 direction
   - ❌ 基于 thought 推断并修改 direction
   - ❌ 未经人类确认直接写入 direction

#### 5.3.4 咨询 Agent（待定）

| 项目 | 说明 |
|------|------|
| **框架** | OpenCode |
| **职责** | 回答项目状态问题，提供信息查询 |
| **输入** | 自然语言提问 |
| **输出** | 项目信息回答 |
| **触发** | 收到 project_info 请求时启动 |

**可查询内容**：
- 版本修改内容
- 文件结构
- 任务状态
- 锁状态
- 方向/思路块内容

**典型查询示例**：
| 用户问题 | 咨询 Agent 回答 |
|---------|-----------------|
| "v1.2.3 做了什么修改？" | "修改了 3 个文件：main.py 添加认证功能、utils.py 新增工具函数、config.py 更新配置" |
| "当前文件结构？" | "项目包含 src/、tests/、docs/ 三个目录，共有 15 个文件" |
| "方向是什么？" | "当前方向：实现用户认证模块" |

**具体行为**：待讨论
    ↓
更新方向块/进度块
    ↓
如需创建任务 → 通知平台创建任务
    ↓
平台检测变更，广播通知
```

**维护Agent无需自定义工具**，使用OpenCode内置工具：
- `read`: 读取方向块、进度块、任务块
- `edit`: 编辑方向块、进度块
- `glob`: 查找项目文件

#### 5.3.4 输入渠道

| 渠道 | 来源 | 处理方式 |
|------|------|----------|
| **初始化对话** | 项目创建者（网页端AI引导） | 完整初始化项目方向、绑定GitHub仓库 |
| **看板输入** | 任何项目成员 | 整合人类想法到项目状态中（不判断对错，只找合适位置） |
| **审核触发** | 审核通过的改动 | 综合分析，更新项目状态 |

**看板功能**：

| 看板类型 | 内容 | 维护Agent处理方式 |
|----------|------|-------------------|
| **问题反馈** | 当前项目存在的问题 | 自动生成Task |
| **改进建议** | 新的思路/方向建议 | 更新Direction/Progress |

看板是人类干预项目的入口，维护Agent负责整合，不判断对错。

### 5.4 错误码体系

| 错误前缀 | 类别 | 说明 |
|----------|------|------|
| `TASK_*` | 任务相关 | 任务不存在、已被领取、冲突 |
| `LOCK_*` | 文件锁相关 | 文件已被锁定、无权操作 |
| `CHANGE_*` | 改动相关 | 提交失败、审核中 |
| `SYSTEM_*` | 系统相关 | 系统错误、维护中 |

### 5.5 异常处理策略

| 异常类型 | 处理策略 |
|----------|----------|
| 网络超时 | 重试 3 次，间隔 1s/2s/5s |
| 资源冲突 | 返回错误，AI 重新选择任务/锁文件 |
| 审核拒绝 | 通知 AI 重新修改 |

---
