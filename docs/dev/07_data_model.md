# 数据模型

## 1. MySQL 表结构

### 1.1 project - 项目表

```sql
CREATE TABLE project (
  id VARCHAR(64) PRIMARY KEY,
  name VARCHAR(256) NOT NULL,
  description TEXT,
  github_repo VARCHAR(512),
  status ENUM('initializing', 'ready', 'idle') DEFAULT 'initializing',
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
);

CREATE INDEX idx_project_name ON project(name);
```

### 1.2 agent - Agent表

```sql
CREATE TABLE agent (
  id VARCHAR(64) PRIMARY KEY,
  name VARCHAR(128) NOT NULL,
  access_key VARCHAR(256) NOT NULL,              -- 密钥明文存储
  session_id VARCHAR(128),                    -- 当前session
  status ENUM('online', 'offline') DEFAULT 'offline',
  current_project_id VARCHAR(64),
  last_heartbeat DATETIME,
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  FOREIGN KEY (current_project_id) REFERENCES project(id) ON DELETE SET NULL
);

CREATE UNIQUE INDEX idx_agent_name ON agent(name);
CREATE INDEX idx_agent_status ON agent(status);
CREATE INDEX idx_agent_project ON agent(current_project_id);
```

### 1.3 content_block - 内容块表

```sql
CREATE TABLE content_block (
  id VARCHAR(64) PRIMARY KEY,
  project_id VARCHAR(64) NOT NULL,
  block_type ENUM('direction', 'milestone', 'version') NOT NULL,
  content TEXT NOT NULL,
  version INT DEFAULT 1,                      -- 块版本号
  updated_at DATETIME DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  FOREIGN KEY (project_id) REFERENCES project(id) ON DELETE CASCADE
);

CREATE UNIQUE INDEX idx_block_project_type ON content_block(project_id, block_type);
```

### 1.4 milestone - 里程碑表

```sql
CREATE TABLE milestone (
  id VARCHAR(64) PRIMARY KEY,
  project_id VARCHAR(64) NOT NULL,
  name VARCHAR(256) NOT NULL,
  description TEXT,
  status ENUM('in_progress', 'completed') DEFAULT 'in_progress',
  created_by VARCHAR(64) NOT NULL,
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
  completed_at DATETIME,
  FOREIGN KEY (project_id) REFERENCES project(id) ON DELETE CASCADE
);

CREATE INDEX idx_milestone_project ON milestone(project_id);
CREATE INDEX idx_milestone_status ON milestone(project_id, status);
```

### 1.5 milestone_archive - 里程碑归档表

归档内容尽可能详细，给人类回顾AI工作成果。

```sql
CREATE TABLE milestone_archive (
  id VARCHAR(64) PRIMARY KEY,
  project_id VARCHAR(64) NOT NULL,
  milestone_id VARCHAR(64) NOT NULL,
  name VARCHAR(256) NOT NULL,
  description TEXT,
  direction_snapshot TEXT NOT NULL,           -- 归档时的方向块快照
  tasks JSON NOT NULL,                        -- 归档的任务列表（含完成者、改动文件、功能描述、审核次数）
  version_start VARCHAR(8),                  -- 里程碑开始版本
  version_end VARCHAR(8),                    -- 里程碑结束版本
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
  FOREIGN KEY (project_id) REFERENCES project(id) ON DELETE CASCADE
);

CREATE INDEX idx_archive_project ON milestone_archive(project_id);
```

**归档tasks字段JSON结构**：

```json
[
  {
    "task_id": "task_m4n5o6",
    "name": "实现登录功能",
    "description": "添加用户认证模块",
    "assignee": "Alice",
    "status": "completed",
    "files_changed": ["src/auth/login.py", "src/auth/register.py"],
    "feature_summary": "添加了登录注册功能，支持邮箱密码认证",
    "audit_count": 1,
    "created_at": "2026-04-19T10:00:00Z",
    "completed_at": "2026-04-19T15:00:00Z"
  }
]
```

### 1.6 task - 任务表

```sql
CREATE TABLE task (
  id VARCHAR(64) PRIMARY KEY,
  project_id VARCHAR(64) NOT NULL,
  milestone_id VARCHAR(64),                   -- 归属里程碑，默认当前活跃里程碑
  name VARCHAR(256) NOT NULL,
  description TEXT,
  priority ENUM('high', 'medium', 'low') DEFAULT 'medium',
  status ENUM('pending', 'claimed', 'completed', 'deleted') DEFAULT 'pending',
  assignee_id VARCHAR(64),
  created_by VARCHAR(64) NOT NULL,           -- 维护Agent ID 或 'dashboard'
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  completed_at DATETIME,
  deleted_at DATETIME,                        -- 删除时间（软删除）
  FOREIGN KEY (project_id) REFERENCES project(id) ON DELETE CASCADE,
  FOREIGN KEY (milestone_id) REFERENCES milestone(id) ON DELETE SET NULL,
  FOREIGN KEY (assignee_id) REFERENCES agent(id) ON DELETE SET NULL
);

CREATE INDEX idx_task_project ON task(project_id);
CREATE INDEX idx_task_milestone ON task(project_id, milestone_id);
CREATE INDEX idx_task_status ON task(project_id, status);
CREATE INDEX idx_task_assignee ON task(assignee_id);
```

### 1.7 file_lock - 文件锁表

```sql
CREATE TABLE file_lock (
  id VARCHAR(64) PRIMARY KEY,
  project_id VARCHAR(64) NOT NULL,
  task_id VARCHAR(64) NOT NULL,
  agent_id VARCHAR(64) NOT NULL,
  files JSON NOT NULL,                       -- 锁定的文件列表（可追加）
  reason TEXT NOT NULL,                      -- 锁定理据
  base_version VARCHAR(8),                   -- 锁定时的版本号
  acquired_at DATETIME DEFAULT CURRENT_TIMESTAMP,
  released_at DATETIME,
  expires_at DATETIME NOT NULL,             -- 过期时间（5分钟TTL，心跳续租）
  FOREIGN KEY (project_id) REFERENCES project(id) ON DELETE CASCADE,
  FOREIGN KEY (agent_id) REFERENCES agent(id) ON DELETE CASCADE,
  FOREIGN KEY (task_id) REFERENCES task(id) ON DELETE CASCADE
);

CREATE INDEX idx_lock_project ON file_lock(project_id);
CREATE INDEX idx_lock_agent ON file_lock(agent_id);
CREATE INDEX idx_lock_task ON file_lock(task_id);
CREATE INDEX idx_lock_expires ON file_lock(expires_at);
```

### 1.8 change - 改动提交表

```sql
CREATE TABLE change (
  id VARCHAR(64) PRIMARY KEY,
  project_id VARCHAR(64) NOT NULL,
  agent_id VARCHAR(64) NOT NULL,
  task_id VARCHAR(64),
  version VARCHAR(8) NOT NULL,               -- 提交时的版本号
  modified_files JSON,                        -- 修改的文件列表+内容
  new_files JSON,                             -- 新增的文件列表+内容
  deleted_files JSON,                         -- 删除的文件列表
  diff JSON,                                  -- 生成的diff
  description TEXT,
  status ENUM('pending', 'approved', 'rejected') DEFAULT 'pending',
  audit_level ENUM('L0', 'L1', 'L2') NULL,   -- 审核等级
  audit_reason TEXT,                          -- 审核原因
  reviewed_at DATETIME,
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
  FOREIGN KEY (project_id) REFERENCES project(id) ON DELETE CASCADE,
  FOREIGN KEY (agent_id) REFERENCES agent(id) ON DELETE CASCADE,
  FOREIGN KEY (task_id) REFERENCES task(id) ON DELETE SET NULL
);

CREATE INDEX idx_change_project ON change(project_id);
CREATE INDEX idx_change_status ON change(project_id, status);
CREATE INDEX idx_change_agent ON change(agent_id);
CREATE INDEX idx_change_created ON change(created_at);
```

---

## 2. Redis 数据结构

### 2.1 Key设计

| Key Pattern | 类型 | 说明 | TTL |
|-------------|------|------|-----|
| `project:{id}:current_milestone` | String | 当前活跃里程碑ID | 无 |
| `project:{id}:tasks` | Hash | 项目任务缓存 field=task_id, value=JSON | 无 |
| `project:{id}:locks` | Hash | 项目文件锁缓存 field=file_path, value=lock_id | 无 |
| `online:agents` | Set | 在线Agent ID集合 | 无 |
| `agent:{id}:heartbeat` | String | Agent心跳时间戳 | 5min |
| `agent:{id}:project` | String | Agent当前项目ID | 5min |
| `broadcast:{project_id}` | List | 项目广播消息队列 | 无 |
| `broadcast:{project_id}:{msg_id}:acked` | Set | 已收到该广播的Agent ID集合 | 24h |
| `offline:{agent_id}:messages` | List | Agent离线消息队列 | 24h |
| `change:pending:{project_id}` | List | 待审核改动队列 | 无 |
| `session:{agent_id}` | String | Agent session信息 | 5min |

### 2.2 缓存策略

| 数据 | 缓存策略 |
|------|----------|
| 任务列表 | 写入时更新缓存，读取优先缓存 |
| 文件锁 | 写入时更新缓存，5分钟过期兜底 |
| 在线状态 | 心跳更新，5分钟超时清除 |
| 广播消息 | 写入Redis List + 已推送Agent集合，不重复推送 |
| 离线消息 | 写入Redis List，下次轮询时返回并清除 |

---

## 3. Git 仓库结构

### 3.1 平台端仓库

每个项目一个Git仓库，存储在平台本地：

```
/data/repos/{project_id}/
├── .git/
├── src/                    # 项目源代码
├── docs/                  # 项目文档
├── README_FILE_MAP.md     # 项目文件概览
└── ...
```

### 3.2 用户端暂存区

```
项目根目录/
├── .a3c_version            # 当前同步的版本号
├── .a3c_staging/
│   └── full/               # 平台最新版完整文件树
└── ...（项目文件）
```

### 3.3 版本号规则

格式：`v{milestone}.{task}`（如 v2.1）

- `milestone`：里程碑编号，项目创建时从1开始，每次里程碑切换递增
- `task`：当前里程碑内已完成的任务数，每完成一个任务递增1
- 示例：v2.1 = 第2个里程碑第1个完成的任务
- 项目初始版本：v1.0
- 版本号在审核通过合并代码时递增
- 回滚时退回旧版本号
- 版本号与Git标签对应

### 3.4 评估文档

导入已有项目时，评估Agent生成标准化项目文档：

```
/data/repos/{project_id}/
├── .git/
├── ASSESS_DOC.md     # 评估Agent输出的项目结构文档（固定位置）
├── src/
└── ...
```

平台读取 `ASSESS_DOC.md` 的内容，提取项目结构描述填入里程碑块。

### 3.4 pending 目录

提交但未审核的文件暂存：

```
/data/pending/{project_id}/{change_id}/
├── writes/
│   ├── src/auth/login.py
│   └── ...
├── deletes/
│   └── src/old_module.py
└── meta.json               # 提交元信息
```

---

## 4. 数据库迁移

### 4.1 迁移目录

```
migrations/
├── 001_create_project.sql
├── 002_create_agent.sql
├── 003_create_content_block.sql
├── 004_create_milestone.sql
├── 005_create_milestone_archive.sql
├── 006_create_task.sql
├── 007_create_file_lock.sql
└── 008_create_change.sql
```

### 4.2 迁移工具

TODO: 选择迁移工具（golang-migrate / goose / GORM AutoMigrate）

---

## 5. ID生成规则

| 实体 | 前缀 | 格式 | 示例 |
|------|------|------|------|
| 项目 | proj | `proj_{uuid_short}` | proj_a1b2c3 |
| Agent | agent | `agent_{uuid_short}` | agent_x9y8z7 |
| 任务 | task | `task_{uuid_short}` | task_m4n5o6 |
| 文件锁 | lock | `lock_{uuid_short}` | lock_p7q8r9 |
| 改动 | chg | `chg_{uuid_short}` |chg_s1t2u3 |
| 里程碑 | ms | `ms_{uuid_short}` | ms_v4w5x6 |
| 内容块 | cb | `cb_{uuid_short}` | cb_y7z8a9 |

`uuid_short` = UUID v4 取前8位，保证唯一性（冲突时重生成）。