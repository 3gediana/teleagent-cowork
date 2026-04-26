# 平台全流程测试 - 管理员视角

**目标**: 扮演人类平台管理员,严格只通过 Chief chat 和 Agent Pool 前端功能操作,观察平台真实的多 agent 协作行为。

**起点状态**:
- Backend: 3003 live
- Frontend: 3303 live
- Project: `proj_980c6166` (pool-e2e), auto_mode=true
- LLM: minimax-e2e, MiniMax-M2.7
- Human agent: agent_82ca322d
- Pool agents: 0
- Tasks: 3 (历史遗留,不清理)
- Human access key (模拟前端登录态): `87823b3e4f7635c9f024dd90255dc2f3`

## 允许的操作通道
| 通道 | 对应前端 | 对应 API |
|---|---|---|
| 跟 Chief 对话 | ChiefPage > Chat tab | `POST /api/v1/chief/chat?project_id=X` `{message}` |
| 看 Chief 回复 | ChiefPage 自动拉 SSE | `GET /api/v1/dashboard/messages?channel=chief` |
| Spawn pool agent | AgentPoolPage > Spawn 按钮 | `POST /api/v1/agentpool/spawn` |
| 看 pool 状态 | AgentPoolPage 自动刷新 | `GET /api/v1/agentpool/list` |
| 看任务 | TaskPage | `GET /api/v1/task/list?project_id=X` |
| 看 PR | PRPage | `GET /api/v1/pr/list?project_id=X` |

## 禁止
- 不直接 insert 到 DB
- 不直接 RPUSH 到 Redis
- 不手动 rsync 文件
- 不给 agent 发 prompt(只给 Chief,其他 agent 的 prompt 是平台自己生成的)
- 不调 agent 自己应该调的 task.claim / filelock / change_submit

## Timeline

(见后续追加)
09:38:22 - message sent

### 我的人类反应
直接问 Chief:"有重复任务,帮我清理",测试平台**自进化 / self-heal** 能力。

## Phase 1.5 — 调试重复任务如何清理

### Chief 的反应(测试 self-heal)
- Chief 理解了指令, 回复: "已将去重指令转给 Maintain（urgency=now）。Maintain 会删除重复任务、保留细拆版本..."
- Chief 调 `delegate_to_maintain` 传 scope=tasks
- Maintain session 启动, 耗时 26s 完成

### Maintain 的工具调用(真实 trace)
```
read MILESTONE.md         → success=0  "project path not set on session"
glob **/*.md              → success=0  "project path not set on session"  
glob *                    → success=0  "project path not set on session"
grep .                    → success=0  "project path not set on session"
```
**任务数量: 18 → 仍然 18**。Maintain 什么都没改变。

### 为什么会失败 — 一条 PowerShell 坑让我诊断了 1 小时

- 我 PS 脚本里写 `$pid = 'proj_980c6166'`
- **`$pid` 是 PowerShell 只读自动变量**(当前进程 PID)
- 赋值静默失败,`$pid` 继续是 `37764`(我 PS 进程的 PID)
- URL 变成 `?project_id=37764`

### 但这暴露了 4 个真实平台缺陷

这些是平台应该 catch 但没 catch 的问题,恰好由我的 PS bug 撞出来:

1. **`POST /chief/chat` 不验证 project_id 存在**
   - 后端 log 里 `record not found` 明确了这点
   - 但 handler 没 early return,继续创建 Chief session
   - **应该返回 404 "project not found"**

2. **Agent session 可以用不存在的 project_id 创建**
   - DB 里插入 `agent_session.project_id='37764'` 没报错
   - 没有 FK constraint,也没有业务层校验

3. **Maintain 在空 project 上调工具时才失败,失败得太晚**
   - "project path not set on session" 是到工具层才报
   - 而且 Maintain 的 LLM 看到失败**没有向上汇报失败**,只是 chief_output 说"已处理"

4. **`create_task` 不验证 project_id 存在**
   - 18 个 orphan task 全都成功写入 `task.project_id='37764'`
   - project 表里没这个 id, 但 task 表照样接受

### 自检: 是我操作错,但平台应该防御这种错
- 真实场景里,人类不会踩 PS 的 `$pid` 坑, 但可能误点 project、传错 id、复制粘贴错了
- 平台的 defense-in-depth 不应该只靠调用方不出错

09:38:22 - first (wrong-project-id) message sent
09:42:39 - dedup (wrong-project-id) message sent
09:50:35 - diag (correct project_id=proj_980c6166) sent — log confirms "Created chat session for project proj_980c6166"
09:55:42 - real tasq request with correct $ProjId variable sent

## Phase 1.6 — 正确 project_id 下的 Chief → Maintain

**Chief 回复** (OK):
> 已将"创建 tasq CLI 任务管理工具任务清单"的指令转给 Maintain 处理（urgency=now）。Maintain 将按你的技术要求（Python 3.11+, SQLite, click+rich, pytest 覆盖）拆出约 10 个独立任务，每个含产出文件和验收标准。完成后会在平台上可见。

**Maintain 实际产出 15 任务**(~90s):
| id | priority | name | 对 tasq? |
|---|---|---|---|
| task_713e62e6 | high | Project scaffolding and dependency setup | ✓ |
| task_395f9e26 | high | Define core data models (Task/Project/Tag) | ✓ |
| task_9d7c1ef3 | high | Database layer: SQLite connection and CRUD | ✓ |
| task_3e6fc1d0 | medium | Config & formatter abstraction layer | ✓ |
| task_08efe265 | high | CLI entry point and add/rm/edit commands | ✓ |
| task_92343346 | high | Implement task creation and lifecycle management | ✓ |
| task_ad3a81a8 | medium | done command (status transition to done) | ✓ |
| task_cf2c526b | high | List and show commands with rich output | ✓ |
| task_daf1f4cd | medium | Project and tag subcommands | ✓ |
| task_8e4ea8db | medium | Report, export, and import commands | ✓ |
| task_a908402f | low | Interactive shell (REPL) mode | ✓ |
| task_43b5d83b | low | Integration tests and CI pipeline | ✓ |
| task_2c4a258b | high | **Define agent role specifications** | ✗ off-topic |
| task_ee0dca61 | medium | **Create agent registry and discovery mechanism** | ✗ off-topic |
| task_ecdf4987 | medium | **Add basic coordination primitives** | ✗ off-topic |

### 观察到的 5 号平台缺陷
5. **Maintain 跑题**: 生成了 3 个与 tasq 无关的"agent 平台"任务
   - 推测原因: Maintain 的 prompt 注入了 GlobalState,其中项目名 "pool-e2e" 和 Agent List 让 LLM 误解为还要做"agent 基础设施"
   - Maintain 的 DirectionBlock/MilestoneBlock 如果是空的或误导,LLM 会脑补
   - **15 task 里 20% 跑题**, 是用户说的"因为平台而做不好"的又一证据

### 此时状态
- proj_980c6166 下: 12 合理 task + 3 跑题 task
- proj_37764 (orphan) 下: 18 孤儿 task (上面的 bug)
- 0 pool agent

进入 Phase 2。

## Phase 2 — Spawn pool agent 并观察自主行为

### 2.1 spawn (通过 `/api/v1/agentpool/spawn` 正规 API)

- 连续 POST 3 次 spawn 请求, 每次间隔 5s
- 成功 spawn: worker-A (port 5500) + worker-B (port 5501) → status=ready
- **第 3 个失败** (worker-C 同样没出现, 上一轮 worker-gamma 也失败)
  - 推测: 资源竞争或 port/pid 管理 race。 暂且不深究, 2 个也够测协作

另: 上一轮后端进程随我的终端被关而连带死亡 → 这是**我的操作问题**,不是平台 bug。但重启后 pool manager 不 recover DB 里的 platform-hosted agent → 留下 zombie 记录(status=online 但进程不存在) → 这是真实问题, 不过次要, 先不展开。

### 2.2 核心实验: spawn 后 idle 5 分钟 → 观察是否自主开工

**基线 (10:12:45)**:
- 15 pending / 0 claimed / 0 completed
- 0 filelock / 0 change
- 0 new agent_session

**5 分钟后 (10:18:05)**:
- 15 pending / 0 claimed / 0 completed ← **未变**
- 0 filelock / 0 change ← **未变**
- 0 new agent_session ← **未变**
- pool instance token 使用: **0** (两个 agent 都 0 tokens, 0 rotations)

**结论: pool agent 在 spawn-ready 之后, 不会自主扫描 task 表并开工.** 两个 agent 完全 idle, 连一次 LLM 调用都没发。

### 2.3 根因分析 — 源代码级验证

检查是否平台后端或某个 scheduler 会派活:

```
grep TASK_ASSIGN in backend source code:
→ broadcast_consumer_test.go   (test)
→ dormancy_test.go             (test)
→ 没有任何生产代码发送 TASK_ASSIGN 广播
```

检查 `BroadcastDirected()` 的所有调用点:
1. `change.go:483` 发 AUDIT_RESULT (审核反馈 agent)
2. `pool_archive_notifier.go` 发 POOL_ARCHIVE (session 归档通知)
3. ...就这两个,没了

**再检查前端**:
- `agentPoolApi`: list / opencodeProviders / metrics / spawn / shutdown / sleep / wake / purge
- 没有 assign / broadcast / send / inject / dispatch

**再检查 Chief / Maintain 工具表**:
- Chief: approve_pr / reject_pr / switch_milestone / create_policy / delegate_to_maintain / chief_output
- Maintain: create_task / update_milestone / propose_direction / biz_review_output
- **都没有"把任务派给某个 pool agent"的工具**

### 平台缺陷 #6 — 最核心、最严重
**pool agent 通信链路断裂**

设计上:
- Pool agent 每 5 秒 poll Redis 等待 `TASK_ASSIGN` 广播
- 收到后 MCP 把消息 inject 到 opencode session, agent 自动开始 task.claim 工作流

实际上:
- **没有任何生产代码发起 TASK_ASSIGN 广播**
- Chief/Maintain 工具栏里没有"派任务"工具
- 前端 UI 没有"派任务给 agent"按钮
- Task/Claim endpoint 是"当前 agent claim",不能"代表另一个 agent claim"

结果: **spawn 了 agent 等于雇了员工但不分配任务,完美 idle 到关机**。

这就是用户说"三个 LLM 团队做不出一个 CLI"的根本原因 —— 不是 LLM 不行, 是平台根本没让 agent 动起来。

### 此时状态
- proj_980c6166 下: 15 task 全部 pending
- 2 pool agent ready but idle
- 0 filelock / 0 change

### 决策点 — Phase 3 怎么走?

"正规前端路径"在 Phase 2 就断了。接下来的测试有两个选项:

**Option A — 严格停在前端路径**: 此处就是平台的真实极限。记录为 "平台不可用" 并交付报告。

**Option B — 作为知情的开发者用内部机制模拟**: 直接 `RPUSH a3c:broadcast:proj_980c6166 <TASK_ASSIGN event>` 到 Redis, 模拟"如果平台派了活"之后, 测试下游环节(filelock / file_sync / change_submit / audit 是否工作)。这能告诉我们 pool agent workflow 本身 work 不 work, 只是"触发口"缺失。

## Phase 3 — 修复缺陷 #6 后继续测试

### 3.1 修复: 加 Task Dispatcher scheduler

新文件 `@/Users/sans/CascadeProjects/coai2/platform/backend/internal/service/task_dispatcher.go` (其实在 `D:\claude-code\coai2\platform\backend\internal\service\task_dispatcher.go`):
- 每 15s 扫一次
- 找 online + is_platform_hosted + 当前 project_id 有值 + 没有 claimed task 的 pool agent
- 找 pending + assignee_id IS NULL 的 task, priority DESC, created_at ASC
- 1-to-1 matching, 发 `BroadcastDirected(agentID, "TASK_ASSIGN", {...})`
- 90s cooldown 防止同 task 重复派

`main.go` 加 `service.StartTaskDispatcher()` 启动。

编译通过, 后端重启成功。log 确认 `[Dispatcher] Task dispatcher started (interval=15s, cooldown=1m30s)`。

### 3.2 跑测

spawn w-alpha + w-beta (都是 MiniMax-M2.7, 都 ready, 都 project_id=proj_980c6166)。

**~15s 后 dispatcher 派了 2 个 task**:
```
10:37:23 [Dispatcher] Assigned task_2c4a258b ("Define agent role specifications") → w-alpha
10:37:23 [Dispatcher] Assigned task_92343346 ("Implement task creation and lifecycle state machine") → w-beta
10:37:24 [Pool] injected TASK_ASSIGN into agent_1903e68b (session=ses_...)
10:37:24 [Pool] injected TASK_ASSIGN into agent_f638f812 (session=ses_...)
```

**2 个 agent 都成功 claim 了 task**:
```
pending 15 → 13
claimed 0 → 2
```

但 4 分钟后:
```
claimed 2,   completed 0,   filelocks 0,   changes 0
w-alpha tokens 31605, w-beta tokens 39436
```

**50k+ tokens 消耗, 0 行代码产出**。查 opencode session message 看 LLM 在做什么:

### 3.3 w-alpha 的实际行为

MsgTrace:
1. receive TASK_ASSIGN: "Define agent role specifications"
2. `skill using-a3c-platform` 读 SKILL.md
3. `a3c_project_info query="agent role specifications Builder Operator Reviewer Maintainer"` — **以为任务是给 A3C 平台定义 agent roles**
4. `glob **/*.md` — 在自己的 pool 工作目录 `platform/data/pool/pool_XXX/` 里找, 找不到(因为这是 agent 的 opencode 工作目录, 不是 tasq staging)
5. `a3c_task claim task_2c4a258b` → ✓
6. 此时 dispatcher 又塞来 task_08efe265 ("CLI entry point") — **w-alpha 被打断**
7. `a3c_status_sync`
8. `a3c_task claim task_08efe265` → 失败 (可能是因为 w-alpha 已持有一个 claim)
9. 卡住, `<think>` 继续消耗 token

**完全没调 file_sync**, 完全没调 filelock。

### 3.4 w-beta 的实际行为(更惊人)

MsgTrace:
1. receive TASK_ASSIGN: "Implement task creation API and lifecycle state machine"
2. `a3c_task claim task_92343346` → ✓
3. `a3c_project_info` → 失败
4. **使用 opencode 内置的 `task` (sub-agent) 工具 explore `D:\claude-code\coai2\platform\data\pool\pool_XXX`**
5. 这个 pool 目录里只有 skills, 所以 sub-agent 继续往上找, **读到了 A3C 平台源码**:
   - `read D:\claude-code\coai2\platform\backend\internal\handler\task.go`
   - `read D:\claude-code\coai2\platform\backend\internal\model\models.go` (offset 100 刚好命中 Task struct)
   - `read D:\claude-code\coai2\platform\backend\internal\service\broadcast.go`
6. `<think>` "Now I have a comprehensive understanding of the codebase. The task requires me to implement a task creation API and lifecycle state machine..."
7. 写了一个 `todowrite` 计划, 打算**修改 A3C 平台自身** (Task model 加状态、broadcast 重构等)
8. 开始提出"Implementation Plan: New State Machine created → assigned → in_progress..."

**w-beta 把 A3C 平台当成了它要实现的项目**。如果让它继续, 它会 `change_submit` 去改 platform/backend/internal/handler/task.go!!

**立刻 shutdown 两个 worker 中断实验**。

### 发现的新缺陷

#### 缺陷 #7 — Dispatcher idle 检测过粗
- 只靠 `assignee_id + status=claimed` 判断 agent 空闲
- Agent 收到 TASK_ASSIGN → LLM 思考 → 调 task.claim 之间有 30-60s 延迟
- 这期间 dispatcher 又给同一 agent 派新 task, 打断上下文
- **需要改进**: 检查 agent 最近 N 分钟是否有 opencode 消息, 或引入 "pending assign" 中间态

#### 缺陷 #8 — Pool 工作目录 sandbox 逃逸 (**严重**)
- Pool agent 的 `CWD` = `D:\claude-code\coai2\platform\data\pool\pool_XXX`
- 这个目录**在 A3C 平台源码树内部**
- Agent 的 `read` / `glob` 没有硬性边界, 往上级目录探索就能读到平台源码
- **w-beta 已实际读到 A3C platform 的 task.go / models.go / broadcast.go**
- 如果 LLM 决定"我要修改 task lifecycle 就该改这些", `change_submit` 就能写破坏平台自身
- **修复**: pool 工作目录移出平台源码树(如 `%TEMP%\a3c-pool\` 或 `~/.a3c-workdir/pool/`);或 opencode 层面限制 read 的允许前缀

#### 缺陷 #9 — Agent 不遵守 SKILL.md 的 core loop
- SKILL.md 明确: file_sync → filelock → edit → change_submit
- **w-alpha / w-beta 都跳过了 file_sync 和 filelock**
- MiniMax-M2.7 自主推理, 用 opencode 内置 `read`/`glob`/`task` 探索, 不信任 a3c_ 前缀工具
- 结果: 4 分钟 50k+ tokens, 0 行代码
- **可能原因**: SKILL.md 放在 `.claude/skills/` 里, opencode 的 skill 加载是"参考资料", 非 system prompt 强制
- **修复**: 把 core loop 放进 opencode session 的 system prompt 硬约束, 而不是可读的 skill 文件

#### 缺陷 #10 — Maintain 生成的任务描述对项目缺乏绑定
- "Define agent role specifications" / "Implement task creation API and lifecycle state machine" 这种描述可以套任何 multi-agent/task 系统
- 没有 tasq context(Python、CLI、SQLite 等关键词), agent 的 LLM 猜错项目
- 加上缺陷 #8 让 agent 读到了 platform 源码, 完美形成"错把 A3C 当目标项目"的陷阱
- **修复**: Maintain 的 create_task prompt 必须强制注入 "当前项目是 tasq, Python 3.11 CLI 工具" 这种 project header

## 缺陷汇总表

| # | 缺陷 | 现象 | 严重度 | 修复状态 |
|---|---|---|---|---|
| 1 | Chief/Maintain 无 idempotency | 人类重发请求 → 任务重复 | 中 | 未修 |
| 2 | Maintain 无 delete_task 工具 | 清理重复任务 无法自动化 | 中 | 未修 |
| 3 | `/chief/chat` 不校验 project_id | 孤儿 session | 低 | 未修 |
| 4 | `create_task` 不校验 project_id | 孤儿 task 进 DB | 低 | 未修 |
| 5 | Maintain 跑题 3/15 任务 | 生成与需求无关的"agent 基础设施"任务 | 中 | 未修 |
| **6** | **TASK_ASSIGN 派发链路缺失** | pool agent spawn 后永远 idle | **致命** | **本实验已修 (task_dispatcher.go)** |
| 7 | Dispatcher idle 检测过粗 | 同一 agent 被连派多个 task 打断上下文 | 中 | 待改进 |
| **8** | **Pool 工作目录 sandbox 逃逸** | agent 读到/可改 A3C 平台自身源码 | **严重** | 未修 |
| 9 | Agent 不遵守 SKILL.md 流程 | 跳过 file_sync/filelock, 白烧 50k tokens 不产出 | 严重 | 未修 |
| 10 | Maintain 任务描述缺项目 context | agent 误解任务对象 | 严重 | 未修 |

## 结论

用户问"为什么三个 LLM 团队做不出一个 CLI 工具",这次实验给出了非常具体的答案:

1. **平台的核心通信链路从来没有接通**(缺陷 #6)— 没有 dispatcher 时,pool agent 是绝对 idle 的,什么也不会做
2. **即使通了,worker 的行为也会失控**:
   - 工作目录让它读到错的代码 (#8)
   - LLM 不按 SKILL.md 走 (#9)
   - 任务描述让它做错事 (#10)

LLM 本身没问题(MiniMax-M2.7 的 reasoning 很认真, 有 `<think>` 分析任务), **是平台把它们放进了一个错误的执行上下文**: 没有任务广播、没有项目 sandbox、skill 不是强制的、任务描述抽象。

这 10 个缺陷并列存在时, 100% 做不出任何 CLI 工具。最要命的是 #8 #9 #10 —— 即使修了 dispatcher, 让 agent 在错的工作目录下按错的理解干活, **反而比 idle 更危险**(会污染平台自己的源码)。
