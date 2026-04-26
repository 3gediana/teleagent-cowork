## 七、数据模型

### 7.1 核心实体

```sql
-- 项目表
CREATE TABLE project (
  id VARCHAR(64) PRIMARY KEY,
  name VARCHAR(256) NOT NULL,
  description TEXT,
  github_repo VARCHAR(512),
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- Agent 表（需先在平台注册）
CREATE TABLE agent (
  id VARCHAR(64) PRIMARY KEY,
  name VARCHAR(128) UNIQUE NOT NULL,
  access_key VARCHAR(256) NOT NULL,    -- 注册时生成的密钥
  session_id VARCHAR(128),
  status ENUM('online', 'offline') DEFAULT 'offline',
  current_project_id VARCHAR(64),
  last_heartbeat DATETIME,            -- 最后心跳时间
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
  FOREIGN KEY (current_project_id) REFERENCES project(id)
);

-- 内容块表
CREATE TABLE content_block (
  id VARCHAR(64) PRIMARY KEY,
  project_id VARCHAR(64) NOT NULL,
  block_type ENUM('direction', 'milestone', 'version', 'task', 'lock') NOT NULL,
  content TEXT,
  updated_at DATETIME DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  FOREIGN KEY (project_id) REFERENCES project(id)
);

-- 里程碑表
CREATE TABLE milestone (
  id VARCHAR(64) PRIMARY KEY,
  project_id VARCHAR(64) NOT NULL,
  name VARCHAR(256) NOT NULL,
  description TEXT,
  status ENUM('in_progress', 'completed') DEFAULT 'in_progress',
  created_by VARCHAR(64) NOT NULL,
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
  completed_at DATETIME,
  FOREIGN KEY (project_id) REFERENCES project(id)
);

-- 里程碑归档表（人类回顾AI工作成果）
CREATE TABLE milestone_archive (
  id VARCHAR(64) PRIMARY KEY,
  project_id VARCHAR(64) NOT NULL,
  milestone_id VARCHAR(64) NOT NULL,
  name VARCHAR(256) NOT NULL,
  description TEXT,
  direction_snapshot TEXT,          -- 归档时的方向块快照
  tasks JSON,                      -- 归档的任务列表（含完成者、耗时、审核次数）
  version_end VARCHAR(8),          -- 归档时的版本号
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
  FOREIGN KEY (project_id) REFERENCES project(id)
);

-- 任务表
CREATE TABLE task (
  id VARCHAR(64) PRIMARY KEY,
  project_id VARCHAR(64) NOT NULL,
  milestone_id VARCHAR(64),        -- 归属里程碑，默认为当前活跃里程碑
  name VARCHAR(256) NOT NULL,
  description TEXT,
  priority ENUM('high', 'medium', 'low') DEFAULT 'medium',
  status ENUM('pending', 'claimed', 'completed') DEFAULT 'pending',
  assignee_id VARCHAR(64),
  created_by VARCHAR(64) NOT NULL,    -- 创建者（维护Agent或人类看板）
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
  completed_at DATETIME,
  FOREIGN KEY (project_id) REFERENCES project(id),
  FOREIGN KEY (milestone_id) REFERENCES milestone(id),
  FOREIGN KEY (assignee_id) REFERENCES agent(id)
);

-- 文件锁表（按任务聚合）
CREATE TABLE file_lock (
  id VARCHAR(64) PRIMARY KEY,
  project_id VARCHAR(64) NOT NULL,
  task_id VARCHAR(64) NOT NULL,       -- 关联任务ID
  agent_id VARCHAR(64) NOT NULL,
  files JSON NOT NULL,                -- 锁定的文件列表（可追加）
  reason TEXT NOT NULL,               -- 锁定理据
  base_version VARCHAR(8),            -- 锁定时的 git hash
  acquired_at DATETIME DEFAULT CURRENT_TIMESTAMP,
  released_at DATETIME,
  expires_at DATETIME,                -- 锁过期时间 (5分钟TTL，心跳续租)
  FOREIGN KEY (project_id) REFERENCES project(id),
  FOREIGN KEY (agent_id) REFERENCES agent(id),
  FOREIGN KEY (task_id) REFERENCES task(id)
);

-- 改动提交表
CREATE TABLE change (
  id VARCHAR(64) PRIMARY KEY,
  project_id VARCHAR(64) NOT NULL,
  agent_id VARCHAR(64) NOT NULL,
  task_id VARCHAR(64),
  modified_files JSON,
  new_files JSON,
  diff JSON,
  status ENUM('pending', 'approved', 'rejected') DEFAULT 'pending',
  audit_reason TEXT,  -- 审核原因（拒绝时填写）
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
  FOREIGN KEY (project_id) REFERENCES project(id),
  FOREIGN KEY (agent_id) REFERENCES agent(id),
  FOREIGN KEY (task_id) REFERENCES task(id)
);
```

### 7.2 Redis 数据结构

| Key Pattern | 类型 | 说明 |
|-------------|------|------|
| `project:{id}:current_milestone` | String | 当前活跃里程碑ID |
| `project:{id}:tasks` | Hash | 项目任务缓存（含milestone_id） |
| `project:{id}:locks` | Hash | 项目文件锁缓存 |
| `online:agents` | Set | 在线 Agent ID 集合 |
| `offline:queue:{id}` | List | Agent 离线消息队列 |
| `change:pending` | List | 待审核改动队列 |

---
