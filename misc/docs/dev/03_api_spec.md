# API 规范

## 通用约定

### 基础信息

| 项 | 值 |
|-----|------|
| Base URL | `http://{platform_url}/api/v1` |
| 认证方式 | Header: `Authorization: Bearer {access_key}` |
| 内容类型 | `application/json` |
| 字符编码 | UTF-8 |
| 密钥格式 | 10+位随机字符串，明文存储 |

### 版本号规则

格式：`{milestone}.{task}`（如 2.1）

- `milestone`：里程碑编号，项目创建时从1开始，每次里程碑切换递增
- `task`：当前里程碑内完成的任务数，每完成一个任务递增1
- 示例：v2.1 = 第2个里程碑第1个完成的任务

版本号在审核通过合并代码时递增。

### 通用响应结构

```json
{
  "success": true,
  "data": {},
  "error": {
    "code": "ERROR_CODE",
    "message": "Human readable message"
  }
}
```

### 错误码体系

| 前缀 | 类别 | 示例 |
|------|------|------|
| `AUTH_*` | 认证相关 | AUTH_INVALID_KEY, AUTH_EXPIRED |
| `PROJECT_*` | 项目相关 | PROJECT_NOT_FOUND, PROJECT_FULL |
| `TASK_*` | 任务相关 | TASK_NOT_FOUND, TASK_CLAIMED, TASK_COMPLETED |
| `LOCK_*` | 文件锁相关 | LOCK_CONFLICT, LOCK_NOT_FOUND, LOCK_EXPIRED |
| `CHANGE_*` | 改动相关 | CHANGE_SUBMIT_FAILED, CHANGE_NOT_FOUND |
| `MILESTONE_*` | 里程碑相关 | MILESTONE_NOT_FOUND, MILESTONE_NOT_ACTIVE |
| `SYSTEM_*` | 系统相关 | SYSTEM_ERROR, SYSTEM_MAINTENANCE |
| `VERSION_*` | 版本相关 | VERSION_OUTDATED, VERSION_CONFLICT |

---

## 1. 认证与连接

### POST /auth/login

Agent登录平台。

**请求**：
```json
{
  "key": "string",
  "project": "string (optional)"
}
```

**响应（无project）**：
```json
{
  "success": true,
  "data": {
    "agent_id": "string",
    "agent_name": "string",
    "projects": [
      {
        "id": "string",
        "name": "string",
        "description": "string"
      }
    ]
  }
}
```

**响应（有project）**：
```json
{
  "success": true,
  "data": {
    "agent_id": "string",
    "agent_name": "string",
    "project_context": {
      "id": "string",
      "name": "string",
      "direction": "string",
      "milestone": "string",
      "version": "string"
    }
  }
}
```

**错误码**：`AUTH_INVALID_KEY`, `PROJECT_NOT_FOUND`

---

### POST /auth/logout

Agent登出，释放所有锁和任务。

**请求**：
```json
{
  "key": "string"
}
```

**响应**：
```json
{
  "success": true,
  "data": {
    "released_locks": ["string"],
    "released_tasks": ["string"]
  }
}
```

---

### POST /auth/heartbeat

心跳续租。

**请求**：
```json
{
  "key": "string"
}
```

**响应**：
```json
{
  "success": true,
  "data": {
    "server_time": "ISO8601",
    "lock_ttl": 300
  }
}
```

**说明**：
- 每5分钟发送一次（侧车进程自动处理）
- 成功续租锁和在线状态
- 超时5分钟未发送 → 视为断线

---

## 2. 任务管理

### POST /task/claim

领取任务。

**请求**：
```json
{
  "task_id": "string"
}
```

**响应**：
```json
{
  "success": true,
  "data": {
    "id": "string",
    "name": "string",
    "description": "string",
    "milestone_id": "string",
    "priority": "high | medium | low"
  }
}
```

**错误码**：`TASK_NOT_FOUND`, `TASK_CLAIMED`, `TASK_COMPLETED`

---

### POST /task/complete

完成任务。

**请求**：
```json
{
  "task_id": "string"
}
```

**响应**：
```json
{
  "success": true,
  "data": {
    "id": "string",
    "name": "string",
    "status": "completed"
  }
}
```

**错误码**：`TASK_NOT_FOUND`, `TASK_NOT_CLAIMED_BY_YOU`

---

### DELETE /task/{task_id}

删除任务（仅维护Agent可操作，反向创建逻辑）。

**响应**：
```json
{
  "success": true,
  "data": {
    "id": "string",
    "name": "string",
    "status": "deleted"
  }
}
```

**错误码**：`TASK_NOT_FOUND`, `TASK_ALREADY_CLAIMED`

---

### GET /task/list

查询任务列表（status.sync内部调用）。

**查询参数**：
| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| project_id | string | 是 | 项目ID |

**响应**：
```json
{
  "success": true,
  "data": {
    "tasks": [
      {
        "id": "string",
        "name": "string",
        "description": "string",
        "status": "pending | claimed | completed",
        "assignee_id": "string | null",
        "assignee_name": "string | null",
        "milestone_id": "string",
        "priority": "high | medium | low"
      }
    ]
  }
}
```

---

## 3. 文件锁

### POST /filelock/acquire

锁定文件。

**请求**：
```json
{
  "task_id": "string",
  "files": ["string"],
  "reason": "string"
}
```

**响应（成功）**：
```json
{
  "success": true,
  "data": {
    "locked_files": ["string"],
    "expires_at": "ISO8601"
  }
}
```

**响应（冲突）**：
```json
{
  "success": false,
  "error": {
    "code": "LOCK_CONFLICT",
    "message": "Some files are already locked",
    "conflict_files": [
      {
        "file": "string",
        "locked_by": "string",
        "task_id": "string",
        "expires_at": "ISO8601"
      }
    ]
  }
}
```

**说明**：
- 同一task_id多次acquire会追加文件到已有锁记录
- 锁TTL为5分钟，心跳续租

---

### POST /filelock/release

释放文件锁。

**请求**：
```json
{
  "files": ["string"]
}
```

不传files则释放该Agent所有锁。

**响应**：
```json
{
  "success": true,
  "data": {
    "released_files": ["string"]
  }
}
```

---

## 4. 改动提交

### POST /change/submit

提交改动。MCP工具只负责启动，后台脚本处理上传。

**请求**：
```json
{
  "task_id": "string",
  "description": "string (optional)",
  "version": "string",
  "writes": [
    "src/auth/login.py",
    { "path": "src/config.py", "content": "inline content" }
  ],
  "deletes": ["src/old_module.py"]
}
```

**说明**：
- `writes`: 文件路径（MCP读取内容）或目录路径（MCP扫描）或 `{path, content}`（AI直接提供）
- `version`: MCP自动从 `.a3c_version` 读取
- `deletes`: 文件或目录路径

**响应**：
```json
{
  "success": true,
  "data": {
    "change_id": "string",
    "message": "Submitted, waiting for audit result"
  }
}
```

**错误码**：`VERSION_OUTDATED`, `INVALID_PARAMS`, `NO_FILES`, `TASK_NOT_CLAIMED_BY_YOU`

---

## 5. 文件同步

### POST /file/sync

同步平台最新代码到暂存区。

**请求**：
```json
{
  "version": "string (MCP自动填充)"
}
```

**响应**：
```json
{
  "success": true,
  "data": {
    "version": "string",
    "staging_path": ".a3c_staging/full/",
    "files": {
      "no_change": ["string"],
      "unlocked_modify": ["string"],
      "locked_modify": ["string"]
    },
    "message": "Files downloaded to staging area. AI decides whether to apply."
  }
}
```

**说明**：
- 全量拉取，每次sync覆盖整个暂存区
- 不自动覆盖本地文件，AI自行判断是否同步
- AI自行决策同步冲突处理方式（Skill中说明）

---

## 6. 状态同步

### GET /status/sync

获取当前项目和任务/锁状态。

**响应**：
```json
{
  "success": true,
  "data": {
    "direction": "string",
    "milestone": "string",
    "version": "string",
    "tasks": [
      {
        "id": "string",
        "name": "string",
        "description": "string",
        "status": "pending | claimed | completed",
        "assignee_name": "string | null",
        "priority": "high | medium | low"
      }
    ],
    "locks": [
      {
        "task_id": "string",
        "agent_name": "string",
        "files": ["string"],
        "reason": "string",
        "acquired_at": "ISO8601",
        "expires_at": "ISO8601"
      }
    ]
  }
}
```

---

## 7. 咨询接口

### POST /project/info

咨询Agent查询项目信息。

**请求**：
```json
{
  "query": "string"
}
```

**响应**：
```json
{
  "success": true,
  "data": {
    "answer": "string"
  }
}
```

---

## 8. 广播推送

### GET /events

SSE长连接，推送广播事件。

**事件类型**：

| 事件 | 数据 |
|------|------|
| `DIRECTION_CHANGE` | `{ header, payload: { block_type, content, reason, changes } }` |
| `MILESTONE_UPDATE` | 同上 |
| `MILESTONE_SWITCH` | 同上 |
| `VERSION_UPDATE` | 同上 |
| `VERSION_ROLLBACK` | 同上 |
| `AUDIT_RESULT` | `{ change_id, agent, result, new_version?, reject_reason? }` |

**SSE消息格式**：
```
event: DIRECTION_CHANGE
data: {"header":{"messageId":"...","type":"DIRECTION_CHANGE","version":"1.0","timestamp":"..."},"payload":{"block_type":"direction","content":"...","reason":"..."},"meta":{"agent":"...","project_id":"...","triggered_by":"..."}}
```

**广播确认机制**：平台跟踪每条广播已推送给哪些Agent。已接收的不再重复推送，新上线的Agent接收全量广播。

---

### POST /poll

轮询接口（侧车进程使用）。平台返回该Agent未收到的广播消息，并标记该Agent已收到。

**请求**：
```json
{
  "key": "string"
}
```

**响应**：
```json
{
  "success": true,
  "data": {
    "messages": [
      {
        "header": { "messageId": "", "type": "", "timestamp": "" },
        "payload": {},
        "meta": {}
      }
    ],
    "heartbeat_ok": true
  }
}
```

**机制说明**：
- 平台为每条广播维护已收到Agent集合（Redis Set）
- 侧车轮询时，平台返回该Agent未收到的广播
- 返回后将该Agent ID加入每条广播的acked集合
- 如果5秒内同一类型广播多次发生，只返回最新一条
- 新Agent首次轮询时，返回当前最新状态快照（方向块+里程碑块+版本号），不返回历史广播

---

## 9. 看板API

### GET /dashboard/state

获取看板完整状态。

**响应**：
```json
{
  "success": true,
  "data": {
    "direction": "string",
    "milestone": "string",
    "version": "string",
    "tasks": [],
    "locks": [],
    "agents": [
      {
        "id": "string",
        "name": "string",
        "status": "online | offline",
        "current_task": "string | null"
      }
    ]
  }
}
```

---

### POST /dashboard/input

看板输入（人类通过Web仪表盘发送）。

**请求**：
```json
{
  "target_block": "direction | milestone | task",
  "content": "string"
}
```

**说明**：方向块输入需要维护Agent与人类口头确认后更新。

---

### POST /dashboard/confirm

确认看板输入。

**请求**：
```json
{
  "input_id": "string",
  "confirmed": true
}
```

---

### POST /dashboard/clear_context

清空对话上下文。

**请求**：
```json
{
  "session_id": "string"
}
```

---

## 10. 项目管理

### POST /project/create

创建新项目（Web界面操作）。

**请求**：
```json
{
  "name": "string",
  "description": "string (optional)",
  "github_repo": "string (optional)",
  "import_existing": false
}
```

**说明**：
- `import_existing = false`：创建空项目，初始化空Git仓库
- `import_existing = true`：导入已有项目，指定github_repo，评估Agent自动分析项目结构

**响应**：
```json
{
  "success": true,
  "data": {
    "id": "string",
    "name": "string",
    "status": "initializing | ready"
  }
}
```

---

### POST /project/import-assess

触发评估Agent分析已导入项目的结构（平台内部调用）。

**响应**：
```json
{
  "success": true,
  "data": {
    "assess_id": "string",
    "status": "running"
  }
}
```

---

## 11. Agent注册（Web界面）

### POST /agent/register

注册新Agent（人类在Web界面操作）。

**请求**：
```json
{
  "name": "string",
  "project_id": "string (optional)"
}
```

**响应**：
```json
{
  "success": true,
  "data": {
    "agent_id": "string",
    "name": "string",
    "access_key": "string (仅此一次返回)"
  }
}
```

---

## 12. Git操作（平台内部）

以下API仅供平台内部Agent使用，不对外暴露。

### POST /internal/git/diff

获取指定版本的diff。

### POST /internal/git/commit

审核通过后执行git commit。

**Commit message格式**：`[task:{任务名}] {任务描述}`

示例：`[task:实现登录功能] 添加用户认证模块`

平台自动生成commit message，使用任务名作为标识。

### POST /internal/git/revert

回滚到指定版本。

### POST /internal/git/push

空闲时批量push到GitHub。

**Git提交与推送流程**：

```
审核Agent审核通过
    ↓
git add && git commit（生成新版本号，commit message = 任务名+描述）
    ↓
平台空闲时（无用户在线 > N 分钟）
    ↓
git push 到 GitHub
```

**TODO**: 空闲判断的具体N值