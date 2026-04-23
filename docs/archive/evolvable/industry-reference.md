# Self-Evolving Agent Systems — Industry Reference

> 本文档记录业界代表性"自进化 Agent 系统"的做法，供本项目 `internal/service/refinery` 与
> `internal/service/analyze` 等模块在演进时查阅对照。**不是实现方案**，只是"他们都怎么做
> 的"。写入路线图之前先来这里确认一下别人踩过的坑和赢过的招。

最后更新：2026-04-22
调研范围：2023 Voyager / Reflexion / Generative Agents → 2025 ACE / Bugbot / Devin Session Insights

---

## 0. 术语

本文尽量使用论文原名。下列映射帮助对齐我们的代码：

| 本文 | 我们代码里的对应 |
|---|---|
| Generator   | `AgentSession` 执行（chief / maintain / audit / fix / evaluate / merge / analyze） |
| Reflector   | `service/analyze.go` + `service/refinery/` 的 passes |
| Curator     | ⚠️ **目前没有** — ACE 论文证明效果的关键层 |
| Playbook    | 粗略对应注入后的 system prompt + 精选 `KnowledgeArtifact` |
| Delta item  | 我们还没有；最近的是 `SkillCandidate` / `Policy`（但缺 helpful/harmful counter） |

---

## 1. Voyager (2023, NVIDIA + Caltech)

**出处**：https://voyager.minedojo.org · arXiv 2305.16291

### 核心思想
LLM 驱动的终身学习 Agent，在 Minecraft 里不断积累 **可执行 skill**，相当于"给自己写插件库"。

### 关键机制
- **Automatic Curriculum**：Agent 自己基于当前能力提出下一个"合适难度"的任务，不等人类出题。
- **Skill Library**：每个 skill 是一段**可执行的 JavaScript 函数** + 文档；库以函数的**描述 embedding** 为 key，新任务来时按语义检索最相近的几个 skill 作为脚手架。
- **Iterative Prompting**：写代码 → 游戏里跑 → 失败 → LLM 看错误和环境反馈 → 改代码 → 再跑。通过就入库。
- **Self-verification**：用一个独立 LLM 当"考官"判断任务是否真完成，防止 Agent 自己骗自己。

### 能借鉴给我们的
- **Skill 必须可验证才能入库** — 我们的 L0/L1/L2 verdict 就是这个门槛，方向对。
- **Skill 按 embedding 索引** — 我们 `KnowledgeArtifact.Embedding` 已经有了，但 PatternExtractor 的匹配只用字面 n-gram，没用 embedding。
- **Skill 的可组合性**：Voyager 允许新 skill 调用已有 skill。我们的 `Skill_candidate` 目前是扁平的，没有引用关系。

### 用不上的
- Automatic Curriculum（我们不自己产生任务，任务来自人）。

---

## 2. Reflexion (2023, Northeastern + MIT)

**出处**：https://arxiv.org/abs/2303.11366 · NeurIPS 2023

### 核心思想
不动模型权重，靠**自然语言的自我反思**来强化 Agent。

### 关键机制
三模型架构：
- **Actor** `M_a`：生成 action、执行
- **Evaluator** `M_e`：给 trajectory 打分（对应我们的 audit）
- **Self-Reflection** `M_sr`：失败后**用自然语言**写 "下次我应该避免什么、怎么做更好"
- 反思文字进**episodic memory buffer**，下次 Actor 开工前作为 prompt 的一部分读回

### 能借鉴给我们的
- **Post-mortem 作为 first-class memory 类型** — 我们的 `Experience.do_differently` 字段有苗头但没用好；Reflexion 证明"自然语言形式的反思"比结构化字段更有效，因为 Actor 读起来就是读指令。
- **失败样本的价值高于成功样本** — 我们现在 `PatternExtractor` 只挖成功模式，`AntiPatternDetector` 是第二公民。Reflexion 暗示可以反过来。

### 警告
Reflexion 本质上是"同一任务多次 retry 学习"。我们的任务不重复，所以**跨任务**的 reflection 共享（谁来读？下一个同类任务）需要我们自己设计检索方式。

---

## 3. Generative Agents — Stanford Park et al. (2023)

**出处**：https://arxiv.org/abs/2304.03442（"Smallville"小镇模拟）

### 核心思想
Agent 有**记忆流（memory stream）**，不断写入观察/对话/反思；检索时按 **三因子评分**选最相关 top-K 喂回 prompt。

### 关键机制
经典检索公式：
```
score = α·importance + β·recency + γ·relevance
importance ≈ LLM 一次性给这条 memory 打 1-10 分
recency   = exp(-λ·hours_since_last_access)      // 指数衰减
relevance = cosine(query_embedding, memory_embedding)
```

- **Reflection tree**：每 N 条 observation 触发一次 LLM "归纳"，产出抽象更高的 reflection；reflection 自己也可以被归纳 → 递归结构。
- **Memory stream 永不删除**，只靠衰减和 top-K 选出来。

### 能借鉴给我们的
- **`recency = exp(-λ·Δt)` 替换我们现在的线性 recency**。我们 `artifact_context.go` 的 `recencyScore` 是 `max(0, 1 - age_days/180)`，一旦超过 180 天就是 0；指数衰减更自然。
- **importance 让 LLM 打分一次并持久化**，而不是我们现在的 `confidence` 固定值。
- **Reflection of reflection**：我们的 Refinery 只有一层。可以让 MetaPass 的输出再被二次聚合。

### 用不上的
Generative Agents 是"讲故事"场景，importance 的语义跟我们"对写代码有没有用"不同，需要重新定义 prompt。

---

## 4. Mem0 (2024)

**出处**：https://mem0.ai · https://github.com/mem0ai/mem0

### 核心思想
面向生产的 "Agent Memory as a Service"。主打**动态遗忘**和**结构化压缩**。

### 关键机制
- **Dynamic Forgetting**：低相关度条目按时间衰减，定期清理
- **压缩的是结构化事实，不是原文**：LLM 把会话浓缩成 `{subject, predicate, object}` 样的事实三元组
- Memory 层位于 context 之外，查询时按需注入

### 能借鉴给我们的
- **"存事实，不存原文"** — 我们的 `Experience` 保留了大段自然语言（approach / pitfalls / key_insight）。Mem0 建议再过一层 LLM 压缩成结构化事实，更易检索匹配。
- **Forgetting 是一等公民** — 我们目前只有 deprecate（usage≥10 & rate<0.3），没有时间维度的遗忘。

### 用不上的
Mem0 面向 chat agent，一次会话的上下文很短；我们的 session 可能跑几十轮，信号密度不同。

---

## 5. agentmemory (rohitg00, 2025)

**出处**：https://github.com/rohitg00/agentmemory

### 核心思想
面向**编码 Agent**的持久记忆层；MCP 服务器形式，可挂到 Claude Code / Cursor / Cline。

### 关键机制（最密集的一家）
- **Ebbinghaus 曲线衰减**：记忆随时间遗忘
- **访问即强化**：被检索到 → strength 增加
- **Stale auto-evict**：长期未访问 → 自动清理
- **Contradiction detection + resolution**：新事实与旧事实冲突时标记并挑一个
- **Triple-stream retrieval**：**BM25 + vector + knowledge graph** 三路并行
- **Reciprocal Rank Fusion (RRF, k=60)** 融合三路排名：
  ```
  score_i = Σ_j  1 / (k + rank_ij)
  ```
- **Session diversification**：每个 source session 最多回 3 条，防止单一 session 支配
- **4-tier consolidation**：raw → compressed → conceptual → archival（像大脑睡眠整理）
- **Hook-driven capture**：`PreToolUse / PostToolUse / PostToolUseFailure / PreCompact / SessionEnd` 全生命周期事件都捕获

### 能借鉴给我们的
- **RRF 替换加权和**。我们 `SelectArtifactsForInjection` 现在 `0.55·sem + 0.20·tag + 0.15·imp + 0.10·rec`，权重硬编码。RRF 只看排名位次，不需要 tune 权重，对信号 scale 不敏感。
- **Session diversification 约束**。我们现在一个 episode cluster 可能把 top-5 全占了。
- **BM25 keyword 通道**。我们只有 vector。对"变量名、error message"这种 literal 查询，BM25 更准。
- **Hook 化事件捕获**。我们在 `artifact_feedback.go` 只抓 L0/L1/L2 verdict，没抓 session 内部的失败 tool 调用、compact 触发点等；这些都是有价值的信号。

### 警告
4-tier consolidation 是 sleep-like batch job，成本高（LLM 调用密集）；小项目不需要。

---

## 6. Cognition Devin — Session Insights & Playbooks (2025)

**出处**：https://cognition.ai/blog/how-cognition-uses-devin-to-build-devin

### 核心思想
Devin 团队"用 Devin 建 Devin"过程中总结的自进化机制；去年 154 PR/周 → 今年 659 PR/周。

### 关键机制
- **Session Insights**：每个 session 结束后，LLM 分析 trajectory 输出 4 块：
  1. Issues & challenges（技术问题、沟通 gap、范围蔓延）
  2. Session timeline with efficiency metrics
  3. Action items（即时改进 + 流程优化）
  4. **Improved prompt suggestions** — 可以一键从 insight 起新 session
- **Playbooks**：重复性任务的 custom system prompt。一个好 playbook 包含：
  - Outcome（期望结果）
  - Steps（前置步骤）
  - Specifications（后置条件）
  - Advice to correct priors（校正 LLM 默认偏好）
  - Forbidden actions（显式禁止清单）
  - Required input
- **DeepWiki**：自动索引代码库生成 wiki + 架构图，作为 agent 的 codebase 知识底座

### 能借鉴给我们的
- **Improved prompt suggestions 一键使用** — 我们 Analyze 的输出目前只进 `skill_candidate` 等待人工 approve，没有"下次直接用"的路径。
- **Playbook 的结构（outcome/steps/spec/advice/forbidden/input）非常适合做我们 `Policy` 的升级版** — 现在的 Policy 太偏"if/then 规则"，缺具体操作指南。
- **DeepWiki-style 代码库理解**：我们 Maintain agent 有 ProjectPath 访问文件的能力，但没有自动索引成 wiki。

### 警告
Playbook 是**人工策展**（Cognition 团队自己写）；Devin 的自动产出是 Session Insights，不是 Playbook。"自动生成 Playbook" 是下一步尚未公开的方向。

---

## 7. Cursor Bugbot Learned Rules (2025-10)

**出处**：https://cursor.com/blog/bugbot-learning

### 核心思想
Bugbot 做 PR 代码审查；把"PR 反馈"作为信号**自动学成规则**。

### 数字
- 6 个月内：在 110k repos 上产生 44k learned rules
- Resolution rate（Bugbot 建议被采纳的比例）：52% → 80%

### 关键机制
三种信号源 → candidate rules → 累计信号 → 晋升 active / 降级 disabled
1. **Reaction**：用户对 Bugbot 评论点👎 → 负信号
2. **Reply**：用户回"不对，这里应该是..." → 正/负均可（LLM 判断）
3. **Missed bugs**：人类 reviewer 发现了 Bugbot 漏过的 → "这种类型下次要看"

规则生命周期：
```
candidate → (累积正信号 N 次) → active → (持续负信号) → disabled
     ↑                                         |
     └─── 人工在 UI 编辑/删除 ←────────────────┘
```

### 能借鉴给我们的
- **三种信号的类比**（非常 clean 地对应我们的情况）：
  | Bugbot | 我们的对应 |
  |---|---|
  | Reaction 👎 | L2 rejected / human reject PR |
  | Reply "应该是..." | Audit 的 L1 specific issues / evaluate's changes_needed |
  | Missed bug | 人工直接修 PR / rollback version |
- **helpful/harmful 双向 counter** 替代我们现在单向的 `usage_count + success_count + failure_count`。Bugbot 的两个 counter 更直接：helpful_count 代表"被点赞+被采纳"，harmful_count 代表"被吐槽+被 disable"。
- **自动晋升 + 自动降级** 不需要人工 approve —— 我们 `skill_candidate` 现在要人工 approve，是瓶颈。

---

## 8. ACE — Agentic Context Engineering (2025-10) ⭐

**出处**：https://arxiv.org/abs/2510.04618 (Stanford + SambaNova + UC Berkeley)
**参考实现**：https://github.com/jmanhype/ace-playbook

### 核心思想
**不更新权重，靠演化 context 来自改进。** 反对 "每次重写整段 prompt"（会 context collapse），主张 **增量 delta + 保留历史**。

### 数字
- AppWorld benchmark：+10.6% over 基线
- Finance (XBRL)：+8.6%
- 适应延迟：**-82%**（vs GEPA）
- Token 成本：**-75%**（vs Dynamic Cheatsheet）
- 关键：所有角色用**同一个基座 LLM**（DeepSeek-V3.1），不需要 fine-tune

### 关键机制：Generator → Reflector → Curator

```
Task
 │
 ▼
┌─────────────┐
│  Generator  │  跑任务，产出 trajectory（reasoning + tool calls）
│             │  暴露 helpful moves 和 harmful moves
└──────┬──────┘
       │
       ▼
┌─────────────┐
│  Reflector  │  从 trajectory 抽取**具体经验教训**
│             │  （自然语言，条目化）
└──────┬──────┘
       │
       ▼
┌─────────────┐
│   Curator   │  把教训转成**typed delta items**
│             │    { id, content, helpful_count, harmful_count, ... }
│             │  **确定性合并**（不是 LLM 改写）：
│             │    - Dedupe（embedding 相似度 > 阈值 → 合并计数）
│             │    - Prune（harmful > helpful + N → 下架）
│             │    - No full rewrite（永远只做局部 delta 操作）
└──────┬──────┘
       │
       ▼
Evolving Playbook（持续增长的精炼经验库）
       │
       └─→ 下一次 Generator 开工时注入
```

### 两个设计铁律
1. **Incremental delta updates**：改 context 只能是 append / edit / delete 单条 item，**不能整段重写**。因为单次整段重写会把历史细节抹掉（"brevity bias" + "context collapse"）。
2. **Grow-and-refine**：让 playbook 变长是可以的（long-context LLM 能吃），但要持续精炼（de-dupe、prune），不能膨胀成噪声。

### 能借鉴给我们的（对我们影响最大的一家）
- **Curator 层就是我们缺的那一块**。我们现在 Analyze 和 Refinery 都是 Reflector，产出直接写进 `skill_candidate` / `policy` / `knowledge_artifact` 等表，**没有一个确定性的 merge/dedupe/prune 层**。
- **Delta item 数据模型** 可以覆盖/升级现有三张表：
  ```go
  type DeltaItem struct {
      ID            string
      Role          string    // chief / audit_1 / fix / ... — playbook 按 role 分桶
      Kind          string    // lesson / guard / recipe / anti_pattern
      Content       string    // 自然语言的精炼教训
      HelpfulCount  int
      HarmfulCount  int
      Embedding     []byte
      SourceEvents  []string  // provenance
      CreatedAt     time.Time
      LastReinforce time.Time
  }
  ```
- **确定性合并**（避免 LLM 漂移）：这也是为什么 ACE 的延迟能降 82%——Curator 的去重/剪枝是 Go 代码算，不是 LLM 调用。
- **Per-role playbook**：不同 agent（chief / audit / fix）看不同子集。我们现在 `SelectArtifactsForInjection` 有 `Audience` 参数但只分 commander / analyzer，粒度不够细。

### 注意
ACE 原文主要在 agent benchmark（AppWorld）和结构化金融推理（XBRL）上验证，还没有在**长周期多项目的编码 Agent 协作平台**上验证。我们算是第一批尝试这个场景的。

---

## 9. AlphaEvolve (DeepMind, 2025-05)

**出处**：https://deepmind.google/blog/alphaevolve-a-gemini-powered-coding-agent-for-designing-advanced-algorithms/ · arXiv 2506.13131

### 核心思想
进化算法 + LLM：对单个代码问题生成 N 个候选 → 用自动评估器打分 → 保留最好的一批 → 变异 → 下一轮。

### 能借鉴给我们的
大概率**用不上直接**。AlphaEvolve 解决的是"给定明确 fitness function 的算法优化"，我们的任务没有 fitness（就是 L0/L1/L2 三档，且有主观成分）。

唯一可能的借鉴：**同一任务允许 N 个 worker 并跑，自动比较结果选最好**——这是 `agent_pool` 层面的改动，不是 self-evolution。

---

## 10. 总对照表：我们有什么 / 缺什么

| 维度 | 业界共识做法 | 我们现状 | 差距 |
|---|---|---|---|
| 检索公式 | RRF 或多信号融合 | 4 路加权和 | ✗ 权重死板 |
| Recency 衰减 | `exp(-λ·Δt)` | `max(0, 1 - age/180)` | ✗ 线性不自然 |
| Importance 打分 | LLM 一次性打 1-10 | 固定 confidence | ✗ 不分轻重 |
| BM25 keyword | agentmemory / 大多数生产系统 | 无 | ✗ 缺 literal 通道 |
| Session 多样性 | 每 session 最多 N 条 | 无约束 | ✗ 单 session 能支配 |
| Curator 层 | ACE（效果验证） | 无 | ✗ **最大的缺口** |
| Delta item + helpful/harmful | ACE / Bugbot | 只有 usage/success/failure | ✗ 计数太粗 |
| 贡献度归因 | Reflexion + Bugbot | 平摊到所有 injected | ✗ 不知哪个真起作用 |
| L1 信号利用 | Bugbot 全信号都用 | 丢弃 | ✗ 漏 1/3 的审核信号 |
| Post-mortem as memory | Reflexion 的核心 | 散落在 `Experience.do_differently` 字段 | ✗ 未 first-class |
| Ebbinghaus 衰减 | agentmemory / Mem0 | 无 | ✗ 记忆不老化 |
| 访问即强化 | agentmemory | 无（只统计 usage_count） | ✗ 没有 strength 概念 |
| 矛盾检测 | agentmemory | 无 | ✗ 可能积累自相矛盾的 skill |
| Per-role playbook | ACE / Devin Playbooks | Audience 二分类 | △ 粒度不够 |
| Session Insights 一键用 | Devin | Analyze 产出要人工 approve | ✗ 反馈环太长 |
| 验证后才入库 | Voyager | 已有（L0/L1/L2） | ✓ |
| Reflection of reflection | Generative Agents | MetaPass 只有一层 | △ |
| 确定性去重 + 剪枝 | ACE Curator | 无 | ✗ |

---

## 11. 一句话总结

> **我们已经有了 Generator 和 Reflector，但缺一整个 Curator 层；检索维度很多但缺融合；计数器粒度太粗且不含时间衰减和强化机制。**

三个最有杠杆的改造方向（按 ROI 排）：

1. **引入 ACE-style Curator + delta items**（覆盖现有 `skill_candidate` / `policy`）—— 解决"候选堆积 + 无法自动晋升 + 无对称的 helpful/harmful"三个核心问题
2. **检索层换 RRF + session 多样性 + 指数衰减 recency** —— 调整分数公式即可，改动小效果直接
3. **引入 Session Insights 管线**（类 Devin + Reflexion 的 post-mortem）—— 让每次 session 的教训直接进 playbook，不等定时任务

具体路线图见后续专项设计文档。

---

## 附：参考链接

- Voyager: https://voyager.minedojo.org · https://github.com/MineDojo/Voyager
- Reflexion: https://arxiv.org/abs/2303.11366 · https://github.com/noahshinn/reflexion
- Generative Agents: https://arxiv.org/abs/2304.03442
- Mem0: https://mem0.ai · https://github.com/mem0ai/mem0
- agentmemory: https://github.com/rohitg00/agentmemory
- Devin — How Cognition Uses Devin to Build Devin: https://cognition.ai/blog/how-cognition-uses-devin-to-build-devin
- Cursor Bugbot learned rules: https://cursor.com/blog/bugbot-learning
- ACE: https://arxiv.org/abs/2510.04618 · https://github.com/jmanhype/ace-playbook
- AlphaEvolve: https://deepmind.google/blog/alphaevolve-a-gemini-powered-coding-agent-for-designing-advanced-algorithms/
