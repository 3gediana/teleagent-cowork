## 五、用户端 MCP 工具集

### 5.1 设计原则

1. **工具最少化**：不超过 10 个工具
2. **用户端轻量化**：用户端只负责改代码和声明操作，不维护状态
3. **平台端智能化**：平台端 Agent 专门负责维护和分析
4. **AI 专注工作**：减少 AI 被打扰，方向/进度由平台广播，不需要 AI 主动查询

### 5.2 用户端工具（7 个）

| 序号 | 工具名称 | 功能 | AI填写参数 | MCP自动填充 | 返回值 |
|------|----------|------|-----------|-------------|--------|
| 1 | `a3c_platform` | 登录/登出平台 | action, url, name, project | - | 项目列表/上下文 |
| 2 | `task` | 领取/完成任务 | action, task_id | - | 任务详情 |
| 3 | `filelock` | 锁定/解锁文件 | action, files | - | 锁定结果 |
| 4 | `change.submit` | 提交改动 | task_id, writes, deletes, description | version | 启动状态 |
| 5 | `file.sync` | 同步文件 | - | version | 分类文件列表 |
| 6 | `status.sync` | 获取任务和锁状态 | - | - | 任务+锁状态 |
| 7 | `project_info` | 咨询项目信息 | query | - | 回答 |

**说明**：
- `task.create` 已删除，任务创建仅由平台端维护Agent负责
- 用户端只能领取和完成任务，不能创建任务

#### 5.2.1 a3c_platform 参数说明

**注册机制**：人类先在平台Web界面注册Agent（设定名字+获取密钥），MCP登录只需密钥+URL。

```typescript
interface A3CPlatform {
  action: 'login' | 'logout';
  platform_url: string;        // 平台地址
  key: string;                 // 注册时获取的密钥
  project?: string;             // 项目名称，进入项目时用
}

interface A3CPlatformResponse {
  success: boolean;
  
  // 登录成功（无project）
  projects?: {
    id: string;
    name: string;
    description?: string;
  }[];
  
  // 登录成功（有project）
  project_context?: {
    id: string;
    name: string;
    direction: string;          // 方向块
    milestone: string;          // 里程碑块
    version: string;            // 当前版本号
  };
  
  // 失败
  error?: {
    code: 'INVALID_KEY' | 'PROJECT_NOT_FOUND' | 'SYSTEM_ERROR';
    message: string;
  };
}
```

**登录流程**：

| 操作 | platform_url | key | project | 说明 |
|------|--------------|-----|---------|------|
| `login` | ✅ | ✅ | - | 首次连接，返回所有项目列表 |
| `login` | ✅ | ✅ | ✅ | 进入指定项目，返回项目完整上下文 |
| `logout` | ✅ | ✅ | - | 断开连接，释放锁和任务 |

#### 5.2.2 task 参数说明

```typescript
interface Task {
  action: 'claim' | 'complete';
  task_id: string;
}

interface TaskResponse {
  success: boolean;
  
  // 领取成功
  task?: {
    id: string;
    name: string;
    description: string;
  };
  
  // 失败
  error?: {
    code: 'TASK_NOT_FOUND' | 'TASK_CLAIMED' | 'TASK_COMPLETED' | 'SYSTEM_ERROR';
    message: string;
  };
}
```

#### 5.2.3 filelock 参数说明

**核心原则**：锁绑定任务，任务完成时释放，不支持部分释放。

```typescript
// 锁定文件
interface FilelockAcquire {
  action: 'acquire';
  files: string[];        // 文件路径列表
  task_id: string;        // 必填：关联任务ID
  reason: string;         // 必填：锁文件的理由
}

// 释放文件（一般不需要手动调用，任务完成时自动释放）
interface FilelockRelease {
  action: 'release';
  files?: string[];       // 可选，不填则释放该Agent所有锁
}

interface FilelockResponse {
  success: boolean;

  locked_files?: string[];     // 锁定成功
  released_files?: string[];   // 释放成功

  error?: {
    code: 'LOCK_CONFLICT' | 'LOCK_NOT_FOUND' | 'SYSTEM_ERROR';
    message: string;
    conflict_files?: {
      file: string;
      locked_by: string;
      task_id: string;
      expires_at: string;
    }[];
  };
}
```

**锁文件聚合机制**：

锁按 `task_id` 聚合存储，同一任务的多次锁文件会追加到同一记录：

```typescript
// 平台锁记录结构
interface FileLock {
  task_id: string;       // 任务ID
  agent: string;         // 锁定者
  files: string[];       // 锁定的文件列表（可追加）
  reason: string;        // 锁定理据
  acquired_at: datetime; // 首次锁定时间
  expires_at: datetime;  // 过期时间
}
```

**锁定机制补充**：
- **锁定 TTL**：文件锁默认 5 分钟过期。
- **心跳续租**：后台 Poller 脚本每次轮询时自动续租当前持有的锁。
- **断线释放**：Agent 掉线后，心跳停止，5 分钟内未续租则锁自动过期。锁过期的同时，该 Agent 已领取未完成的任务也会被释放回 pending 状态，所有未提交的改动全部归还平台。
- **任务完成释放**：change.submit 审核通过后，该任务的锁自动释放。
- **追加逻辑**：同一 task_id 的多次 filelock.acquie 会追加到已有锁记录的 files 列表。

**用户端标准流程**：

```
领取任务 → 分析代码 → 确定影响范围 → 锁文件（必填task_id和reason）
         ↓
    继续分析，发现还需锁其他文件
         ↓
    追加锁文件（同一task_id）
         ↓
    改代码
         ↓
    提交 change.submit
         ↓
    审核通过 → 任务完成 → 锁自动释放
```

#### 5.2.4 change.submit 参数说明

MCP工具定位：只负责**启动**提交流程，实际工作由后台脚本完成。

```typescript
interface ChangeSubmit {
  // AI填写
  task_id: string;                              // 关联任务ID
  description?: string;                         // 改动说明（可选）
  
  writes?: (string | { path: string; content: string })[];
  // 文件路径 → MCP读取内容
  // 目录路径 → MCP扫描目录下所有文件
  // { path, content } → AI直接提供
  
  deletes?: string[];
  // 文件路径或目录路径
  
  // MCP自动填充
  version: string;  // 从 .a3c_version 读取
}

interface ChangeSubmitResponse {
  success: boolean;       // 仅表示启动成功与否
  
  error?: {
    code: 'INVALID_PARAMS' | 'NO_FILES' | 'SYSTEM_ERROR';
    message: string;
  };
  
  message?: string;       // "已提交，等待审核结果"
}
```

**MCP后台脚本处理流程**：
1. 扫描 writes 目录/文件
2. 读取文件内容
3. 读取 `.a3c_version` 填充版本号
4. 上传到平台 `pending/` 目录

**审核结果通知**：通过平台广播返回，不通过MCP返回。

```typescript
interface AuditBroadcast {
  type: 'AUDIT_RESULT';
  change_id: string;
  agent: string;
  result: 'approved' | 'rejected';
  new_version?: string;
  reject_reason?: {
    level: 'L1' | 'L2';
    issues: { file: string; detail: string; }[];
    message: string;
  };
}
```

#### 5.2.5 file.sync 参数说明

**核心原则**：不让平台干预用户端意图，所有文件都不自动覆盖，放入暂存区由AI自行判断。

```typescript
interface FileSync {
  version?: string;     // MCP自动填充，从 .a3c_version 读取
}

interface FileSyncResponse {
  success: boolean;
  version: string;              // 平台最新版本号
  
  staging_path: string;         // ".a3c_staging/full/"
  
  files: {
    no_change: string[];         // 本地无修改
    unlocked_modify: string[];   // 本地有修改但未锁定
    locked_modify: string[];    // 本地有修改且已锁定
  };
  
  message: string;
}
```

**暂存目录结构**：
```
.a3c_staging/
└── full/                     # 平台最新版本的完整文件树
    ├── src/
    └── ...
```

**版本管理**：用户端只保留1个最新版本，拉取时直接覆盖暂存区。

**AI后续处理**：
1. 查看 files 分类
2. 自行判断是否需要同步
3. 手动执行覆盖/合并操作

#### 5.2.6 status.sync 参数说明

```typescript
interface StatusSync {
  // 无参数
}

interface StatusSyncResponse {
  success: boolean;
  
  tasks: {
    id: string;
    name: string;
    description: string;
    status: 'pending' | 'claimed' | 'completed';
    assignee?: string;
  }[];
  
  locks: {
    task_id: string;
    agent: string;
    files: string[];
    reason: string;
    acquired_at: string;
    expires_at: string;
  }[];
}
```

#### 5.2.7 project_info 参数说明

```typescript
interface ProjectInfo {
  query: string;    // 自然语言提问
}

interface ProjectInfoResponse {
  success: boolean;
  answer: string;   // 咨询 Agent 的回答
}
```

**查询示例**：
- "v1.2.3 做了什么修改？"
- "当前文件结构是什么？"
- "方向块内容是什么？"

#### 5.2.8 操作类型说明

| 操作 | 类型 | 处理方 |
|------|------|--------|
| task.claim/complete | 原子操作 | 平台直接处理 |
| filelock.acquire/release | 原子操作 | 平台直接处理 |
| change.submit | 分析操作 | 进入审核队列，由审核Agent处理 |

### 5.3 错误码体系

| 错误前缀 | 类别 | 说明 |
|----------|------|------|
| `TASK_*` | 任务相关 | 任务不存在、已被领取、冲突 |
| `LOCK_*` | 文件锁相关 | 文件已被锁定、无权操作 |
| `CHANGE_*` | 改动相关 | 提交失败、审核中 |
| `SYSTEM_*` | 系统相关 | 系统错误、维护中 |

### 5.4 异常处理策略

| 异常类型 | 处理策略 |
|----------|----------|
| 网络超时 | 重试 3 次，间隔 1s/2s/5s |
| 资源冲突 | 返回错误，AI 重新选择任务/锁文件 |
| 审核拒绝 | 通知 AI 重新修改 |

---

> **平台端 Agent 设计**详见 [03b_platform_agents.md](03b_platform_agents.md)