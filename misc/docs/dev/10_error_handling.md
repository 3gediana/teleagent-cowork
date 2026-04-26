# 错误处理规范

## 1. 设计原则

| 原则 | 说明 |
|------|------|
| 统一格式 | 所有API返回统一JSON结构 |
| 语义化错误码 | 错误码可定位到具体模块和原因 |
| 客户端可处理 | 错误信息足够AI判断下一步行动 |
| 不泄露内部 | 错误信息不暴露堆栈、SQL、内部路径 |

---

## 2. 错误响应格式

```json
{
  "success": false,
  "error": {
    "code": "LOCK_CONFLICT",
    "message": "2 files are already locked by other agents",
    "details": {
      "conflict_files": [
        {
          "file": "src/auth/login.py",
          "locked_by": "Alice",
          "task_id": "task_m4n5o6",
          "expires_at": "2026-04-19T15:30:00Z"
        }
      ]
    }
  }
}
```

| 字段 | 类型 | 说明 |
|------|------|------|
| code | string | 语义化错误码，大写+下划线 |
| message | string | 人类可读的错误描述 |
| details | object | 可选，结构化的错误详情，供AI判断下一步 |

---

## 3. 错误码完整定义

### 3.1 认证错误（AUTH_*）

| 错误码 | HTTP | 说明 | 客户端处理 |
|--------|------|------|-----------|
| AUTH_INVALID_KEY | 401 | 密钥无效 | 重新输入key |
| AUTH_ALREADY_ONLINE | 409 | 该key对应的Agent已在线 | 检查是否有残留进程或等待5分钟 |
| AUTH_KEY_EXPIRED | 401 | 密钥已失效 | 重新注册 |
| AUTH_PROJECT_REQUIRED | 400 | 登录时未指定项目 | 选择项目后重新登录 |

### 3.2 项目错误（PROJECT_*）

| 错误码 | HTTP | 说明 | 客户端处理 |
|--------|------|------|-----------|
| PROJECT_NOT_FOUND | 404 | 项目不存在 | 检查项目名 |
| PROJECT_FULL | 409 | 项目已满（超过6人） | 等待有人退出 |
| PROJECT_GIT_CLONE_FAILED | 502 | GitHub仓库克隆失败 | 检查仓库地址 |
| PROJECT_ASSESS_TIMEOUT | 504 | 评估Agent超时 | 重试 |
| PROJECT_NAME_EXISTS | 409 | 项目名已存在 | 换个名字 |
| PROJECT_NOT_READY | 503 | 项目还在初始化 | 等待后重试 |

### 3.3 任务错误（TASK_*）

| 错误码 | HTTP | 说明 | 客户端处理 |
|--------|------|------|-----------|
| TASK_NOT_FOUND | 404 | 任务不存在 | 检查task_id |
| TASK_CLAIMED | 409 | 任务已被其他Agent领取 | 选择其他任务 |
| TASK_COMPLETED | 410 | 任务已完成 | 选择其他任务 |
| TASK_NOT_CLAIMED_BY_YOU | 403 | 任务不属于当前Agent | 只能操作自己领取的任务 |
| TASK_DELETED | 410 | 任务已被删除 | 选择其他任务 |

### 3.4 文件锁错误（LOCK_*）

| 错误码 | HTTP | 说明 | 客户端处理 |
|--------|------|------|-----------|
| LOCK_CONFLICT | 409 | 文件已被锁定 | details中列出冲突文件和锁持有人 |
| LOCK_NOT_FOUND | 404 | 锁不存在 | 检查lock_id |
| LOCK_EXPIRED | 410 | 锁已过期 | 重新申请锁 |
| LOCK_NOT_YOURS | 403 | 锁不属于当前Agent | 只能释放自己的锁 |

### 3.5 改动错误（CHANGE_*）

| 错误码 | HTTP | 说明 | 客户端处理 |
|--------|------|------|-----------|
| VERSION_OUTDATED | 409 | 版本号过期 | 调用file.sync同步后重新提交 |
| CHANGE_SUBMIT_FAILED | 500 | 提交处理失败 | 重试 |
| CHANGE_NOT_FOUND | 404 | change不存在 | 检查change_id |
| NO_FILES | 400 | 没有提交文件 | 添加writes或deletes |
| CHANGE_ALREADY_APPROVED | 409 | 该change已审核通过 | 无需重复操作 |

### 3.6 里程碑错误（MILESTONE_*）

| 错误码 | HTTP | 说明 | 客户端处理 |
|--------|------|------|-----------|
| MILESTONE_NOT_FOUND | 404 | 里程碑不存在 | 检查milestone_id |
| MILESTONE_NOT_ACTIVE | 409 | 不是当前活跃里程碑 | 只能操作活跃里程碑 |
| MILESTONE_HAS_ACTIVE_TASKS | 409 | 里程碑下还有未完成任务 | 先完成或释放任务 |

### 3.7 版本错误（VERSION_*）

| 错误码 | HTTP | 说明 | 客户端处理 |
|--------|------|------|-----------|
| VERSION_OUTDATED | 409 | 版本号落后 | file.sync同步后重新提交 |
| VERSION_CONFLICT | 409 | 版本号冲突 | file.sync同步 |

### 3.8 系统错误（SYSTEM_*）

| 错误码 | HTTP | 说明 | 客户端处理 |
|--------|------|------|-----------|
| SYSTEM_ERROR | 500 | 内部错误 | 重试或联系管理员 |
| SYSTEM_MAINTENANCE | 503 | 平台维护中 | 等待后重试 |
| RATE_LIMIT | 429 | 请求过于频繁 | 等待后重试 |

---

## 4. 客户端重试策略

| 场景 | 策略 |
|------|------|
| 网络超时 | 重试3次，间隔1s/2s/5s，指数退避 |
| 4xx错误 | 不重试，根据错误码处理 |
| 429 RATE_LIMIT | 等待Retry-After头指定的时间后重试 |
| 5xx错误 | 重试3次，间隔2s/4s/8s |
| LOCK_CONFLICT | 提示AI选择其他文件或等待 |
| VERSION_OUTDATED | 自动调用file.sync后重新提交 |

---

## 5. 服务端错误处理

### 5.1 日志规范

```go
// 日志格式
// [时间] [级别] [请求ID] [模块] 消息 | 错误详情
// 2026-04-19T15:30:00Z ERROR req_abc123 task claim failed | task_id=task_m4n5o6 err=TASK_CLAIMED
```

| 级别 | 场景 |
|------|------|
| ERROR | 4xx/5xx业务错误、数据库操作失败 |
| WARN | 锁冲突、版本过期、心跳超时 |
| INFO | 正常业务操作（登录/登出/提交/审核） |
| DEBUG | 轮询详情、广播推送细节 |

### 5.2 请求ID

每个HTTP请求生成唯一request_id，贯穿日志链路，方便追踪。

### 5.3 panic处理

Go HTTP handler统一recover panic，返回SYSTEM_ERROR，记录堆栈到日志。

---

## 6. 审核相关错误

审核过程中的错误不在API层返回，而是通过AUDIT_RESULT广播通知。

| 场景 | 通知方式 | 内容 |
|------|---------|------|
| 审核通过 | AUDIT_RESULT广播 | result=approved, new_version |
| L2打回 | AUDIT_RESULT广播 | result=rejected, reject_reason |
| L1修复后通过 | AUDIT_RESULT广播 | result=approved, new_version |
| 审核Agent超时 | 平台日志记录 | TODO: 超时策略待定 |

---

## 7. 广播丢了怎么办

侧车轮询机制的天然容错：

| 场景 | 处理 |
|------|------|
| 轮询请求失败 | 5秒后自动重试，不丢失 |
| 平台重启 | Redis持久化广播队列，重启后恢复 |
| Agent离线期间 | Redis保存离线消息，上线后首次轮询返回 |
| 5秒内多次同类广播 | 只保留最新一条，避免重复处理 |
| Agent首次上线 | 返回当前状态快照（方向+里程碑+版本），不推历史 |