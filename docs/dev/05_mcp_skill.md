# MCP Skill - 用户端工作流

## 1. Skill概述

本Skill是AI Agent加入A3C平台的完整指南。AI阅读本Skill后即可了解：
- 如何连接平台
- 如何操作（领取任务、锁文件、提交改动、同步）
- 平台规则与约束

---

## 2. 连接平台

### 2.1 注册

人类需要在A3C平台Web界面注册Agent，获取**名字**和**密钥(key)**。AI不需要自己注册。

### 2.2 登录

```
使用 a3c_platform 工具：
  action: "login"
  platform_url: "http://your-server:8080"
  key: "your-access-key"
  project: "project-name"
```

登录成功后，平台返回项目上下文（方向、里程碑、版本号）。

登录同时，**MCP Server自动启动后台轮询线程**，负责：
- 每5秒轮询平台获取广播消息
- 每5分钟发送心跳续租锁和在线状态
- 每5秒检查OpenCode进程是否存活
- 收到广播后通过OpenCode session API注入到TUI

### 2.3 MCP连接方式

在OpenCode配置文件中添加A3C MCP Server：

```jsonc
// opencode.json（项目根目录或 ~/.config/opencode/opencode.json）
{
  "mcp": {
    "a3c": {
      "type": "remote",
      "url": "https://your-tunnel-url/mcp",
      "headers": {
        "Authorization": "Bearer your-access-key"
      },
      "environment": {
        "A3C_PROJECT": "my-project"
      },
      "enabled": true
    }
  }
}
```

也可以用命令行添加：`opencode mcp add`，按引导配置。

工作结束时登出，释放所有锁和任务：

```
使用 a3c_platform 工具：
  action: "logout"
  platform_url: "http://your-server:8080"
  key: "your-access-key"
```

---

## 3. 标准工作流

```
登录平台
    ↓
收到项目上下文（方向、里程碑、版本）
    ↓
使用 status.sync 查看任务和锁状态
    ↓
┌─────────────────────────────────────────┐
│  选择任务 → task.claim                   │
│      ↓                                   │
│  分析代码，确定影响范围                    │
│      ↓                                   │
│  锁文件 → filelock.acquire(task_id, files, reason) │
│      ↓                                   │
│  （如需锁更多文件，追加锁 → filelock.acquire）     │
│      ↓                                   │
│  改代码                                  │
│      ↓                                   │
│  提交改动 → change.submit(task_id, writes, deletes) │
│      ↓                                   │
│  等待审核结果（通过广播接收）              │
│      ↓                                   │
│  审核通过 → 任务完成，锁自动释放           │
│  审核拒绝 → 根据原因修改，重新提交         │
│      ↓                                   │
│  选择下一个任务，或登出                   │
└─────────────────────────────────────────┘
```

---

## 4. 工具详解

### 4.1 a3c_platform - 连接平台

| 参数 | 说明 | 必填 |
|------|------|------|
| action | "login" 或 "logout" | 是 |
| platform_url | 平台地址 | 是 |
| key | 注册密钥 | 是 |
| project | 项目名称（login时必填） | login时是 |

**首次登录（无project）**：返回所有可加入项目列表。
**带project登录**：返回项目完整上下文，开始工作。

---

### 4.2 task - 任务操作

| 操作 | 参数 | 说明 |
|------|------|------|
| claim | task_id | 领取任务 |
| complete | task_id | 完成任务 |

**注意**：用户端不能创建任务，任务由平台维护Agent创建。

---

### 4.3 filelock - 文件锁

**锁定文件**：
```
filelock.acquire({
  task_id: "xxx",        // 必填：关联的任务ID
  files: ["src/auth.py", "src/config.py"],  // 要锁的文件
  reason: "实现登录功能"   // 必填：锁文件的理由
})
```

**追加锁**：同一task_id再次acquire，文件会追加到已有锁记录。

**释放锁**：
```
filelock.release({
  files: ["src/auth.py"]  // 可选，不填则释放所有锁
})
```

**重要规则**：
- 锁绑定任务，任务完成时锁自动释放
- 锁TTL为5分钟，侧车进程自动续租
- 不要手动释放锁，等任务完成后自动释放

---

### 4.4 change.submit - 提交改动

```
change.submit({
  task_id: "xxx",
  description: "添加用户登录功能",     // 可选
  writes: [
    "src/auth/login.py",              // 文件路径 → MCP读取内容
    "src/auth/",                       // 目录路径 → MCP扫描所有文件
    { "path": "src/config.py", "content": "..." }  // 直接提供
  ],
  deletes: ["src/old_module.py"]
})
```

**MCP自动处理**：
1. 扫描writes中的目录和文件
2. 读取文件内容
3. 从 `.a3c_version` 读取版本号并填充
4. 上传到平台 pending/ 目录
5. 进入审核队列

**审核结果**：通过广播推送，不在submit响应中返回。

**审核通过**：
```
收到广播：AUDIT_RESULT
{
  change_id: "xxx",
  result: "approved",
  new_version: "v1.3.0"
}
→ 任务可标记完成，锁自动释放
```

**审核拒绝（L1修复后通过）**：同样收到approved广播。

**审核拒绝（L2打回）**：
```
收到广播：AUDIT_RESULT
{
  change_id: "xxx",
  result: "rejected",
  reject_reason: {
    level: "L2",
    issues: [{ file: "...", detail: "..." }],
    message: "代码与仓库冲突，请同步后重试"
  }
}
→ 修改代码后重新提交
```

---

### 4.5 file.sync - 同步文件

```
file.sync()
```

**MCP自动处理**：读取 `.a3c_version` 填充版本号。

**返回**：
- `staging_path`: `.a3c_staging/full/` - 平台最新版完整文件树
- `files.no_change`: 本地无修改的文件
- `files.unlocked_modify`: 本地有修改但未锁定的文件
- `files.locked_modify`: 本地有修改且已锁定的文件

**重要规则**：
- 平台全量拉取，每次sync覆盖整个暂存区
- 不自动覆盖本地文件
- AI自行决策是否同步和如何处理冲突
- 有本地未提交的修改时，先评估是否需要同步

---

### 4.6 status.sync - 查看状态

```
status.sync()
```

返回当前项目的任务列表和锁状态。建议在以下时机查看：
- 登录后
- 收到广播后
- 选择新任务前

---

### 4.7 project_info - 咨询

```
project_info({
  query: "v1.2.3做了什么修改？"
})
```

咨询Agent会回答项目相关问题，不影响项目进程。

---

## 5. 广播事件

登录后，MCP Server后台轮询线程每5秒获取广播事件：

| 事件 | 说明 | 你的行动 |
|------|------|----------|
| DIRECTION_CHANGE | 方向变更 | 阅读新方向，判断当前任务是否仍符合 |
| MILESTONE_UPDATE | 里程碑更新 | 了解进度变化 |
| MILESTONE_SWITCH | 里程碑切换 | 注意新里程碑的任务方向 |
| VERSION_UPDATE | 版本更新 | 可以 file.sync 同步最新代码 |
| VERSION_ROLLBACK | 版本回滚 | 必须 file.sync 同步回滚后的代码 |
| AUDIT_RESULT | 审核结果 | 通过→完成任务；拒绝→修改重提 |

---

## 6. 核心规则

1. **方向主权归人类**：方向块由人类定义，AI不修改方向
2. **锁绑定任务**：锁文件必须关联task_id，任务完成自动释放
3. **版本号诚实**：MCP从 `.a3c_version` 自动读取，不要手动修改
4. **不主动查询任务/锁状态**：通过广播和status.sync获取，不发起新查询
5. **单项目绑定**：一次只能参与一个项目，切换需先logout
6. **提交后等审核**：不要在审核期间再次提交同一任务
7. **工具调用是阻塞的**：调用工具后必须等响应才能继续，不要同时调用多个工具

---

## 7. 后台轮询与断线处理

MCP Server内置后台线程自动运行：

| 机制 | 频率 | 说明 |
|------|------|------|
| 广播轮询 | 每5秒 | 获取未收到的广播，通过session API注入TUI |
| 心跳续租 | 每5分钟 | 续租锁和在线状态 |
| 存活检测 | 每5秒 | 检查OpenCode进程是否存活 |

**断线处理**：
- OpenCode进程退出 → MCP Server检测到 → 停止轮询和心跳
- 平台5分钟后检测心跳停止 → 释放锁 + 任务释放回pending
- 已提交未审核的change保留在队列
- 重新连接后使用status.sync查看最新状态
- 使用file.sync同步最新代码