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

-- Agent 表（登录即注册）
CREATE TABLE agent (
  id VARCHAR(64) PRIMARY KEY,
  name VARCHAR(128) UNIQUE NOT NULL,
  session_id VARCHAR(128),
  status ENUM('online', 'offline') DEFAULT 'offline',
  current_project_id VARCHAR(64),
  last_active DATETIME,
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
  FOREIGN KEY (current_project_id) REFERENCES project(id)
);

-- 内容块表
CREATE TABLE content_block (
  id VARCHAR(64) PRIMARY KEY,
  project_id VARCHAR(64) NOT NULL,
  block_type ENUM('direction', 'thought', 'version', 'task', 'lock') NOT NULL,
  content TEXT,
  updated_at DATETIME DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  FOREIGN KEY (project_id) REFERENCES project(id)
);

-- 任务表
CREATE TABLE task (
  id VARCHAR(64) PRIMARY KEY,
  project_id VARCHAR(64) NOT NULL,
  name VARCHAR(256) NOT NULL,
  description TEXT,
  status ENUM('pending', 'claimed', 'completed') DEFAULT 'pending',
  assignee_id VARCHAR(64),
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
  completed_at DATETIME,
  FOREIGN KEY (project_id) REFERENCES project(id),
  FOREIGN KEY (assignee_id) REFERENCES agent(id)
);

-- 文件锁表
CREATE TABLE file_lock (
  id VARCHAR(64) PRIMARY KEY,
  project_id VARCHAR(64) NOT NULL,
  file_path VARCHAR(512) NOT NULL,
  agent_id VARCHAR(64) NOT NULL,
  base_version VARCHAR(8), -- 锁定时的 git hash，用于精准检测同步冲突
  acquired_at DATETIME DEFAULT CURRENT_TIMESTAMP,
  released_at DATETIME,
  expires_at DATETIME,     -- 锁过期时间 (TTL机制)
  FOREIGN KEY (project_id) REFERENCES project(id),
  FOREIGN KEY (agent_id) REFERENCES agent(id)
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
| `project:{id}:tasks` | Hash | 项目任务缓存 |
| `project:{id}:locks` | Hash | 项目文件锁缓存 |
| `online:agents` | Set | 在线 Agent ID 集合 |
| `offline:queue:{id}` | List | Agent 离线消息队列 |
| `change:pending` | List | 待审核改动队列 |

---
