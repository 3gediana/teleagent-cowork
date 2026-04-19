# 平台端 Agent 设计

## 1. Agent角色架构

### 1.1 核心理念

所有"Agent"本质是同一个OpenCode实例，通过不同的**Prompt模板**切换角色。平台根据触发条件选择对应Prompt，注入上下文，调用OpenCode。

| 角色 | 激活条件 | 上下文 | 自定义工具 |
|------|----------|--------|-----------|
| 审核角色 | 有change进审核队列 | 完整注入 | audit_output |
| 修复角色 | 审核角色判定L1 | 部分（审核请求+理由，其余自己读） | fix_output |
| 维护角色 | 20分钟定时/看板输入/里程碑完成 | 完整注入 | create_task, update_milestone, propose_direction, delete_task |
| 咨询角色 | 收到project_info请求 | 注入项目概览 | 无（直接输出文本） |
| 评估角色 | 项目导入时（一次性） | 完整项目文件 | assess_output |

### 1.2 Agent管理器

```go
type AgentManager struct {
    oc          *OpenCodeClient       // OpenCode客户端
    roleConfigs map[string]*RoleConfig // 角色配置（Prompt模板、可用工具）
}

type RoleConfig struct {
    PromptTemplate string   // Prompt模板路径
    Tools          []string  // 可用自定义工具列表
    OpenCodeTools  []string  // 可用OpenCode内置工具
}
```

---

## 2. 审核 Agent

### 2.1 角色

审核Agent负责判断代码提交的冲突等级，决定合并、修复或打回。

### 2.2 激活条件

- 有新的change进入审核队列时激活

### 2.3 双Agent协作机制

```
change入队
    ↓
审核Agent1（完整上下文）
    ↓
┌─── L0 ──→ 合并
├─── L1 ──→ 修复Agent（部分上下文）──→ 确认L1 ──→ 合并
│                                   └──→ 误判 ──→ 审核Agent2（完整上下文）
└─── L2 ──→ 打回用户端
                                                    ├──→ 确认L0 ──→ 合并
                                                    └──→ 确认L2 ──→ 打回用户端
```

### 2.3 输入设计

**系统注入（自动）**：

| 输入 | 说明 |
|-----|------|
| prompt模板 | 平台预设审核指令 |
| diff | 平台从Git仓库生成 |
| 任务详情 | 根据task_id查询 |
| 方向块 | 当前项目方向 |

**AI主动读取**：

| 输入 | 说明 |
|-----|------|
| 提交的文件内容 | pending/目录中 |
| 仓库当前代码 | Git当前版本 |

### 2.4 修复角色上下文

修复Agent接收的信息：
- 审核Agent1传来的审核请求（level、issues、reason）
- 提交的diff
- AI自行决定读取哪些额外文件

### 2.4 自定义工具

#### 审核 Agent 1 输出

```typescript
audit_output({
  level: 'L0' | 'L1' | 'L2',
  issues?: {
    file: string,
    line?: number,
    type: 'syntax' | 'format' | 'logic' | 'dependency',
    detail: string,
    status: 'pending_fix'
  }[],
  reject_reason?: string  // L2时必填
})
```

#### 修复 Agent 输出

```typescript
fix_output({
  action: 'fix' | 'delegate' | 'reject',
  fixed?: boolean,
  delegate_to?: 'audit_agent_2',
  reject_reason?: string
})
```

#### 审核 Agent 2 输出

```typescript
audit2_output({
  result: 'merge' | 'reject',
  reject_reason?: string
})
```

### 2.5 Prompt模板

**审核Agent1 - 系统提示**：

```
你是A3C平台的代码审核Agent。你的职责是判断提交代码的冲突等级。

## 项目方向
{direction_block}

## 当前里程碑
{milestone_block}

## 提交信息
- 任务: {task_name} - {task_description}
- 提交者: {agent_name}
- 文件变更: {changed_files}

## 审核标准
- L0: 无冲突，提交文件内无问题 → 直接合并
- L1: 问题仅在提交文件内（格式、语法等） → 转修复Agent
- L2: 问题涉及其他仓库文件，或与仓库代码冲突 → 打回用户端

请使用 audit_output 工具输出审核结果。
```

**修复Agent - 系统提示**：

```
你是A3C平台的修复Agent。你需要验证审核Agent1标记的问题。

## 提交的diff
{diff}

## 标记的问题
{issues}

## 修复标准
- 确认问题存在且可修复 → action: 'fix', fixed: true
- 发现全是误判 → action: 'delegate', delegate_to: 'audit_agent_2'
- 发现涉及其他文件 → action: 'reject', reject_reason: '...'

请使用 fix_output 工具输出结果。
```

**审核Agent2 - 系统提示**：

```
你是A3C平台的复审审核Agent。审核Agent1的L1判断被修复Agent判定为误判。

## 完整上下文
{full_context}

请使用 audit2_output 工具输出最终判断。
```

---

## 3. 维护 Agent

### 3.1 角色

维护Agent负责维护项目执行路径，处理看板输入，管理任务和里程碑。

### 3.2 激活条件（批量延迟）

| 触发条件 | 说明 |
|----------|------|
| 距上次激活满20分钟 | 定时批量处理 |
| 当前里程碑任务全部完成 | 判断是否切换里程碑 |
| 人类看板输入 | 直接触发 |

**不逐个激活，攒一批再处理。**

### 3.3 权限矩阵

| 信号来源 | 方向块 | 里程碑块 | 任务看板 |
|----------|:------:|:-------:|:------:|
| 人类看板输入（方向级） | ⚠️ 需确认 | ✅ | ✅ |
| 人类看板输入（里程碑级） | ❌ | ✅ | ✅ |
| 任务完成信号 | ❌ | ✅ | ✅ |
| 审核通过后通知 | ❌ | ✅ | ✅ |

**强约束**：
- 方向块修改必须在对话中与人类确认后才能写入（口头确认，不搞弹窗）
- 维护Agent**不能**自行跨里程碑，只能提议
- 维护Agent直接写里程碑块内容（非候选方向），按严格模板格式书写
- 里程碑块不包含task_id，只描述方向和目标

### 3.4 自定义工具

### 3.3 自定义工具

#### create_task（内部工具，仅维护Agent可用）

```typescript
create_task({
  name: string,
  description: string,
  priority?: 'high' | 'medium' | 'low'  // 默认 medium
})
// task_id 由平台自动生成
// milestone_id 自动填充当前活跃里程碑
// created_by 自动填充维护Agent ID
```

#### update_milestone（内部工具）

```typescript
update_milestone({
  content: string  // 新的里程碑块内容
})
```

#### propose_direction（内部工具）

维护Agent在对话中与人类对齐方向后，直接写入方向块内容。propose_direction工具负责：

```typescript
propose_direction({
  content: string  // 方向块完整内容，按模板格式
})
// 对话中已与人类确认，直接应用
// 如对话中人类未明确确认，则不调用此工具
```

#### delete_task（内部工具）

删除任务，维护Agent可操作：

```typescript
delete_task({
  task_id: string
})
```

#### write_milestone（内部工具）

写入里程碑块内容，维护Agent按模板格式直接写入：

```typescript
write_milestone({
  content: string  // 按里程碑块模板格式写入
})
```

**里程碑块模板格式**：

```markdown
# 里程碑：{milestone_name}

## 目标
- {目标1}
- {目标2}

## 项目结构
{从ASSESS_DOC.md自动填充的项目结构描述}

## 注意事项
- {注意1}
- {注意2}
```

### 3.4 可用工具

维护Agent使用OpenCode内置工具 + 平台自定义工具：

| 工具 | 用途 |
|------|------|
| read | 读取方向块、里程碑块、项目文件 |
| edit | 编辑里程碑块、项目文档概览 |
| glob | 查找项目文件 |
| create_task | 创建任务 |
| update_milestone | 更新里程碑块 |
| propose_direction | 写入方向块（确认后） |
| delete_task | 删除任务 |
| write_milestone | 写入里程碑块 |

### 3.5 Prompt模板

```
你是A3C平台的维护Agent。你的职责是维护项目执行路径。

## 当前项目信息
- 方向块: {direction_block}
- 里程碑块: {milestone_block}
- 任务列表: {task_list}
- 项目文档概览: {readme_file_map}

## 权限约束
- 方向块：对话中与人类确认后，使用propose_direction写入
- 里程碑块：使用write_milestone按模板格式直接写入
- 任务：可以创建（create_task）和删除（delete_task），归入当前活跃里程碑
- 禁止自行跨里程碑，只能提议切换

## 触发原因
{trigger_reason}

## 待处理输入
{input_content}

请根据以上信息决定下一步操作。
```

### 3.6 里程碑切换流程

```
当前里程碑所有任务完成
    ↓
激活维护Agent
    ↓
判断：是否还需更多任务？
├── 是（开关打开）→ 创建任务，继续当前里程碑
└── 否 → 提议切换里程碑
          ↓
     人类在看板确认？
     ├── 是 → 平台执行：归档 → 清空 → 新建 → 维护Agent填写内容
     └── 否 → 人类手动加任务
```

---

## 4. 咨询 Agent

### 4.1 角色

回答项目状态问题，提供信息查询，不影响项目进程。

### 4.2 特性

| 特性 | 说明 |
|------|------|
| 无持久上下文 | 每次激活全新注入 |
| 可并行运行 | 不影响项目进程 |
| 只读 | 不修改任何项目数据 |

### 4.3 激活条件

- 收到 `project_info` 请求时启动

### 4.4 输入注入

每次激活时注入项目概览到system prompt：

```
## 项目概览
- 方向块: {direction_block}
- 里程碑块: {milestone_block}
- 任务列表: {task_list}
- 锁状态: {lock_list}
- 当前版本: {version}
- 项目列表: {project_list}
- 分支信息: {branch_list}
```

### 4.5 可用工具

| 工具 | 用途 |
|------|------|
| read | 读取项目文件 |
| glob | 查找项目文件 |

咨询Agent不需要自定义工具，直接输出文本回答。

### 4.6 Prompt模板

```
你是A3C平台的咨询Agent。你的职责是回答关于项目状态的问题。

## 当前项目概览
{project_overview}

你可以使用 read 和 glob 工具查看项目文件的细节。
请直接回答用户的问题，不要修改任何内容。
```

---

## 5. 评估 Agent（一次性）

### 5.1 角色

评估Agent在项目导入时激活一次，全面分析项目结构，生成标准化的项目文档。

### 5.2 激活条件

- 仅在项目导入（`import_existing = true`）时激活
- 一次性运行，完成后不再激活

### 5.3 职责

1. 全面扫描项目文件结构
2. 分析每个文件/文件夹的功能
3. 输出标准化的项目文档到固定位置
4. 文档内容部分填充到里程碑块中

### 5.4 输出

评估Agent将结果写入固定位置：`ASSESS_DOC.md`（项目根目录）

**ASSESS_DOC.md 模板格式**：

```markdown
# 项目结构评估

## 项目概述
{项目简述}

## 目录结构

### {目录路径}
- {文件名} — {功能简述}

### {目录路径}
- {文件名} — {功能简述}

## 依赖关系
{主要模块间依赖}

## 技术栈
{使用的技术}
```

### 5.5 自定义工具

```typescript
assess_output({
  content: string  // 按模板格式输出的项目结构评估
})
```

### 5.6 Prompt模板

```
你是A3C平台的项目评估Agent。你需要全面分析导入项目的结构。

## 项目路径
{project_path}

请执行以下步骤：
1. 使用 glob 扫描所有文件
2. 使用 read 阅读关键文件
3. 分析每个文件/文件夹的功能
4. 使用 assess_output 工具按模板格式输出结果

输出必须严格遵循模板格式，因为平台需要解析这个文档。
```

### 5.7 平台处理流程

```
项目导入（import_existing = true）
    ↓
平台从GitHub克隆仓库
    ↓
激活评估Agent
    ↓
评估Agent分析项目 → 输出 ASSESS_DOC.md
    ↓
平台读取 ASSESS_DOC.md
    ↓
提取项目结构描述 → 填入里程碑块
    ↓
项目状态变为 ready
```

---

## 6. Agent 注册与隔离

### 6.1 注册流程

```
人类在Web界面填写Agent名字
    ↓
平台生成 access_key（10+位随机字符串，明文存储）
    ↓
显示 key 给人类（仅此一次）
    ↓
人类配置 key 到 MCP 工具
    ↓
Agent 使用 a3c_platform(login, key, platform_url) 登录
```

### 6.2 单项目绑定

- Agent登录时必须指定project
- 一个Agent只能参与一个项目
- 切换项目必须先logout再login

### 6.3 断线处理

| 场景 | 处理 |
|------|------|
| 心跳超时（5分钟） | 锁过期 + 任务释放回pending |
| 主动logout | 释放锁 + 任务释放回pending |
| 侧车进程崩溃 | 5分钟后等同于心跳超时 |

**资源释放流程**：

1. 检测心跳停止
2. 释放该Agent所有文件锁
3. 该Agent已领取未完成任务 → 状态重置为pending
4. 已提交未审核的change → 保留在审核队列
5. 私信通知其他Agent可重新领取