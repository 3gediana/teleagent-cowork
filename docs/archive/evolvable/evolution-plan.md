# Self-Evolution 改造方案

> 配套文档：`@/docs/evolvable/industry-reference.md`（业界做法）· `@/docs/evolvable/README.md`（愿景）
>
> 本文档是**可执行的改造路线图**，不是愿景。每一条都对应具体文件、类型和迁移动作。

最后更新：2026-04-23

---

## 0. 结论先行

我们已经有 **Generator（agents）+ Reflector（Analyze + Refinery）**，缺了 ACE 论文证明关键的
**Curator 层**。同时检索层硬编码、信号粒度粗、没有时间衰减机制。

改造分**三个核心 Phase + 一个可选增强 Phase**，各自独立可交付：

| Phase | 耗时 | 核心改动 | 关键收益 |
|---|---|---|---|
| 1 | 2 周 | Curator 层 + Delta Items + L1 信号 + 贡献度归因 | 候选自动晋升/下架；反馈信号粒度细 3-4 倍 |
| 2 | 1 周 | RRF 检索 + 指数衰减 + 会话多样性 | 检索准确率 +，权重不再靠拍脑袋 |
| **2.5** | **1-2 周** | **L1 标量自学习 + L2 Reranker + L5 Audit-Risk 预测器** | **硬编码超参自演化、注入精排、提交前预警** |
| 3 | 1 周 | Session Insights + Per-role Playbook | 每次 session 都在学习，不等定时任务 |

---

## 1. 目标 / 非目标

### 目标
- **闭环**："执行 → 反思 → 沉淀 → 再注入"在单个 session 尺度就能发生，不依赖周定时任务
- **低人工**：skill_candidate / policy_suggestion 不再需要人工 approve，自动按信号升降
- **无权重手调**：检索融合不用再 tune `0.55/0.20/0.15/0.10` 这种权重
- **向后兼容**：现有 `skill_candidate` / `policy` / `knowledge_artifact` 表保留；Curator 之上新建抽象层

### 非目标
- ❌ 跨项目自动 skill 复用（已有 GlobalPromoter，暂不动）
- ❌ 自动生成任务（Voyager curriculum — 我们不需要）
- ❌ 重写 Analyze Agent 或 Refinery passes（它们作为 Reflector 保留）
- ❌ 用进化算法跑 N 个 worker 竞标（AlphaEvolve — agent_pool 层面的事）

---

## 2. 目标架构

```
┌──────────────────────────────────────────────────────────────────┐
│  Generator 层（不改）                                              │
│    AgentSession → dispatcher → native runner → tool calls        │
│    产出：Experience / ToolCallTrace / Change / AuditVerdict      │
└──────────────────────────┬───────────────────────────────────────┘
                           │
          ┌────────────────┼────────────────┐
          ▼                                 ▼
┌─────────────────────┐           ┌─────────────────────┐
│  Reflector A (LLM)  │           │  Reflector B (确定) │
│  analyze.go         │           │  refinery/ passes   │
│  (不改)             │           │  (不改)             │
└──────────┬──────────┘           └──────────┬──────────┘
           │                                 │
           └────────────────┬────────────────┘
                            │
                            ▼  (新增)
┌──────────────────────────────────────────────────────────────────┐
│  Curator 层 — Phase 1 核心                                        │
│    curator/ingest.go:   接收候选，规范化为 DeltaItem              │
│    curator/merge.go:    嵌入相似 → 合并计数；不做 LLM 改写        │
│    curator/prune.go:    harmful>helpful+N / strength<τ → 下架     │
│    curator/playbook.go: 按 role 分桶输出当前有效 delta item      │
└──────────────────────────┬───────────────────────────────────────┘
                           │
                           ▼
┌──────────────────────────────────────────────────────────────────┐
│  Retrieval 层 — Phase 2 核心                                      │
│    artifact_context.go: RRF 融合 + 指数衰减 recency + session 多样性 │
└──────────────────────────┬───────────────────────────────────────┘
                           │
                           ▼
┌──────────────────────────────────────────────────────────────────┐
│  Feedback 层 — Phase 1 & 2 同时改                                  │
│    change_feedback.go: L1 部分信号；使用了才算数 (attribution)   │
│    strength_updater.go: 每小时衰减；访问即强化（Ebbinghaus）     │
└──────────────────────────┬───────────────────────────────────────┘
                           │
                           ▼ (Phase 2.5, 可选增强)  ← 新增
┌──────────────────────────────────────────────────────────────────┐
│  参数化增强层 — Phase 2.5 核心                                     │
│    ml/configs/<pid>/rrf.yaml: L1 学出来的超参（per-project，每周）│
│    sidecar /rerank:  L2 cross-encoder 精排 top-50 → top-K         │
│    sidecar /risk:    L5 audit-risk 预测（change 提交前预警）      │
│    反馈: Change × AuditVerdict 日志 → ml/logs/*.jsonl → 训练管线  │
└──────────────────────────┬───────────────────────────────────────┘
                           │
                           ▼ (Phase 3)
┌──────────────────────────────────────────────────────────────────┐
│  Session Insights 管线（新增）                                    │
│    insights/generator.go: 每个 session 结束 → 生成 post-mortem   │
│    → Curator（立即作为 delta item 入 playbook）                  │
└──────────────────────────────────────────────────────────────────┘
```

---

## 3. Phase 1 详细设计（2 周）

### 3.1 新表：`delta_item`

```go
// internal/model/delta_item.go
type DeltaItem struct {
    ID        string `gorm:"primaryKey;size:64"`
    ProjectID string `gorm:"size:64;index:idx_di_project"` // empty = global
    Role      string `gorm:"size:32;index:idx_di_role"`    // chief / audit_1 / fix / ...
    Kind      string `gorm:"size:32;index:idx_di_kind"`    // lesson / guard / recipe / anti_pattern / post_mortem

    Content string `gorm:"type:text;not null"` // 自然语言教训，Generator 可直读
    Scope   string `gorm:"type:json"`          // 适用范围 {file_categories, task_tags, ...}

    // 双向信号计数（ACE + Bugbot 借鉴）
    HelpfulCount int `gorm:"default:0"`
    HarmfulCount int `gorm:"default:0"`

    // Ebbinghaus strength（Phase 2 填充，Phase 1 先留字段 default 1.0）
    Strength       float64   `gorm:"default:1.0;index:idx_di_strength"`
    LastReinforce  time.Time

    // Provenance
    Source       string `gorm:"size:32"` // analyze / refinery / session_insight / human
    SourceIDs    string `gorm:"type:json"` // [original experience/artifact/session IDs]

    // 去重
    ContentHash  string `gorm:"size:64;index:idx_di_hash"` // SHA256 of normalized content
    Embedding    []byte `gorm:"type:longblob"`
    EmbeddingDim int

    Status    string    `gorm:"size:16;default:'active';index:idx_di_status"` // active / deprecated / superseded
    CreatedAt time.Time
    UpdatedAt time.Time
}
```

### 3.2 Curator 包（新）

```
internal/service/curator/
  ingest.go     // Ingest(ctx, candidates []Candidate) — 从 Analyze/Refinery 产出转 DeltaItem
  merge.go      // Merge(ctx, projectID) — embedding sim > 0.9 的合并 counter
  prune.go      // Prune(ctx, projectID) — harmful>helpful+2 或 strength<0.1 → deprecate
  playbook.go   // LoadPlaybook(role, projectID) []DeltaItem — 按 role 返回有效 top-K
  types.go      // Candidate 接口 + 转换器
```

**关键：Curator 不调 LLM。**合并和剪枝全是 Go 代码逻辑（embedding 比较 + 计数判断）。

### 3.3 数据流迁移

**不删 `skill_candidate` / `policy` / `knowledge_artifact` 表**。Phase 1 的策略是**并联**：
- Analyze agent 产出 `skill_candidate` 和 `policy`（不变）
- Refinery 产出 `knowledge_artifact`（不变）
- **新增**：两个产出源都**额外调** `curator.Ingest()` 同步到 `delta_item`
- 检索时：新 agent session 走新 playbook（Phase 3 起），老路径同时保留做 A/B

让我们一步一步迁。等 `delta_item` 表里信号跑稳了，再砍老表。

### 3.4 L1 部分信号（改 `change_feedback.go`）

现在的 L1 分支直接 early return。改成：

```go
// 旧
case "L1":
    return // drop signal

// 新
case "L1":
    // 部分信号：半个 success + 半个 harmful 提示，反映"方向对但有瑕疵"
    return bumpDeltaItemsForInjection(change.InjectedArtifacts, 0.5, 0.0)
case "L0":
    return bumpDeltaItemsForInjection(change.InjectedArtifacts, 1.0, 0.0)
case "L2":
    return bumpDeltaItemsForInjection(change.InjectedArtifacts, 0.0, 1.0)
```

为什么不给 L1 加 harmful？因为 L1 是"修修可以上"，本质上 artifact **指了正确的方向**，只是执行细节差。惩罚会误伤。

### 3.5 贡献度归因（改 `artifact_feedback.go`）

现在：一次注入 5 个 artifact，L0 时 5 个全 ++。问题：不知道哪个真起作用。

方案：**两档归因**
1. **显式归因**（preferred）：让 agent 在输出里引用用到的 artifact ID（例如 tool call args 里 `referenced_artifacts: [...]`）。被引用的才加 helpful。
2. **隐式归因**（fallback）：LLM-judge 单次调用，"下列 trajectory 里，是否明显用到了 artifact X？"→ 是/否。

Phase 1 实装隐式归因（简单），显式归因作为后续优化。判断缓存 by session_id 避免重复调 LLM。

### 3.6 验收标准 Phase 1

- [ ] `delta_item` 表建立，Analyze/Refinery 产出**同步**写入（老表仍然写）
- [ ] `curator.Merge` 周运行一次，合并 embedding 相似度 >0.9 的 DeltaItem
- [ ] `curator.Prune` 周运行一次，下架 `HarmfulCount > HelpfulCount + 2` 的 item
- [ ] L1 verdict 不再被 drop，按 0.5 加 helpful
- [ ] 注入 5 个 artifact + L0 不再 5 个全 ++，只给真用到的加
- [ ] `platformlive` 跑一遍，验证 delta_item 能正常积累
- [ ] 单元测试：`curator_test.go` 覆盖 merge/prune 边界

---

## 4. Phase 2 详细设计（1 周）

### 4.1 指数衰减 Recency

改 `artifact_context.go::recencyScore`：

```go
// 旧（线性）
func recencyScore(now time.Time, a model.KnowledgeArtifact) float64 {
    age := now.Sub(a.UpdatedAt).Hours() / 24
    return math.Max(0, 1 - age/180)
}

// 新（指数，半衰期 30 天）
func recencyScore(now time.Time, lastUsedAt time.Time) float64 {
    ageHours := now.Sub(lastUsedAt).Hours()
    return math.Exp(-0.693 * ageHours / (30 * 24)) // ln(2)/halflife
}
```

半衰期 30 天按项目配置，不写死。未来可以让每个 kind 不同半衰期（anti_pattern 衰减更慢——反面教材长期有效）。

### 4.2 RRF 融合替换加权和

```go
// 旧
final := 0.55*sem + 0.20*tag + 0.15*imp + 0.10*rec

// 新：分别排名 → RRF
semRanked := rankBy(candidates, semanticScore)
tagRanked := rankBy(candidates, tagScore)
impRanked := rankBy(candidates, importanceScore)
recRanked := rankBy(candidates, recencyScore)

const rrfK = 60
for _, a := range candidates {
    final := 1.0/(rrfK + semRanked[a.ID]) +
             1.0/(rrfK + tagRanked[a.ID]) +
             1.0/(rrfK + impRanked[a.ID]) +
             1.0/(rrfK + recRanked[a.ID])
    scored = append(scored, InjectedArtifact{Artifact: a, Score: final, ...})
}
```

优点：
- 不需要 tune 权重
- 对信号 scale 不敏感（semantic 跟 importance 的分数范围差 100x 都无所谓）
- 缺某路信号时自动降权（rank 变大 → 1/(k+rank) 变小）

### 4.3 Session 多样性约束

```go
// applyBudget 之前加一步
const maxPerSourceCluster = 2
seen := map[string]int{}
filtered := []InjectedArtifact{}
for _, ia := range scored {
    cluster := clusterKey(ia.Artifact.SourceEvents) // 例如第一个 source session_id
    if seen[cluster] >= maxPerSourceCluster {
        continue
    }
    seen[cluster]++
    filtered = append(filtered, ia)
}
```

防止同一个 session 的 artifact 把 top-K 吃光。

### 4.4 Ebbinghaus strength 后台 worker

```go
// internal/service/curator/strength.go
// 每小时跑一次
func DecayAllStrengths() {
    model.DB.Model(&model.DeltaItem{}).
        Where("status = 'active'").
        Update("strength", gorm.Expr("strength * 0.997")) // ≈ -18%/month

    // 同步衰减 KnowledgeArtifact（过渡期双写）
    model.DB.Model(&model.KnowledgeArtifact{}).
        Where("status = 'active'").
        Update("confidence", gorm.Expr("confidence * 0.997"))
}

// 访问即强化：SelectArtifactsForInjection 内
func reinforceOnInjection(ids []string) {
    for _, id := range ids {
        model.DB.Model(&model.DeltaItem{}).Where("id = ?", id).
            Updates(map[string]interface{}{
                "strength":        gorm.Expr("LEAST(strength + 0.05, 2.0)"),
                "last_reinforce":  time.Now(),
            })
    }
}
```

`strength < 0.1` 时自动 deprecate（在 Prune pass 里）。被再次命中时自动 revive 到 active。

### 4.5 验收标准 Phase 2

- [ ] `recencyScore` 切到指数衰减，半衰期可配置
- [ ] `SelectArtifactsForInjection` 用 RRF，单元测试证明"某路信号缺失时仍正常"
- [ ] Session 多样性约束生效
- [ ] Strength worker 每小时衰减 active items；被注入时强化
- [ ] `platformlive` 的检索命中率对比测试：老 vs 新，观察 top-K 变化

---

## 5. Phase 2.5 详细设计（1-2 周，可选增强）

> **前置条件**：Phase 1/2 上线 ≥4 周，累计 ≥500 条 `(InjectedArtifacts, AuditVerdict)` 配对日志；Python sidecar 可达；训练阶段可访问 GPU（推理纯 CPU 够用）。
>
> **开关**：整个 Phase 2.5 包含 3 个独立子模块（L1 / L2 / L5），任何一个出问题都可以单独关掉，不影响 Phase 1/2 主流程。

### 5.1 目标与边界

**在做什么**：让目前**硬编码的数值**（RRF 的 `k`、衰减系数 `0.997`、prune 阈值 `harmful > helpful + 2` 等）变成从反馈日志学出来的；在 top-K 检索前加一层 cross-encoder reranker；给 change 提交前加个 risk 预警。

**不做什么**：
- ❌ 不训练主 LLM（agent 调的 qwen/gpt/claude 完全不动）
- ❌ 不做跨项目知识迁移
- ❌ 不替代 Curator 的确定性 merge/prune（那是 Phase 1 的事）
- ❌ 不引入新 API 外部依赖（训练脚本跑在本机，checkpoint 落盘）

### 5.2 L1 — 标量超参自学习（最简单，先做）

**对象**：把以下硬编码值变成**每项目每周重新学出来的配置**：

| 原硬编码 | 出处 | 变成什么 |
|---|---|---|
| `rrfK = 60` | `artifact_context.go` | 学出 per-project 的 k |
| 四通道权重 `0.55/0.20/0.15/0.10` | `artifact_context.go` | RRF 方案下变成 boost 系数 |
| `halflife_days = 30` | Phase 2 新增 | 学出项目真实的"知识过期周期" |
| `strength * 0.997` | `curator/strength.go` | 学出 decay_per_hour |
| `strength + 0.05` | 同上 | 学出 reinforce_step |
| `harmful > helpful + 2` | `curator/prune.go` | 学出 prune_threshold_delta |

**训练数据**：过去 30 天的 `Change.InjectedArtifacts × AuditVerdict`。每条 injection 是一个 sample：
```
features: [候选的 4 路分数, 命中 rank, 注入后 agent 是否引用, ...]
label:    audit_level → reward scalar
```

**reward 设计**：
```
reward = +1  if L0
         +0.3 if L1
         -1   if L2
         0    if unknown
```

**优化方法**（二选一）：
1. **离线 Nelder-Mead**：每周一次 `scipy.optimize.minimize` 在 ~10 维连续空间搜最优 reward。不保证全局最优，但够用，可重现。
2. **在线 Thompson Sampling**：每周维护一个 posterior（多元高斯），从 posterior 采样参数用于下周，实现 exploration/exploitation 平衡。更科学，但实现略复杂。

Phase 2.5 先做**方案 1**，后续酌情升级。

**产物**：`platform/backend/ml/configs/<project_id>/rrf.yaml`

```yaml
# auto-generated, do not edit by hand
learned_at: 2026-04-30T03:00:00Z
train_samples: 847
eval_reward: 142.3
prev_eval_reward: 138.1

rrf_k: 54
sem_boost: 1.2       # RRF 模式下各通道的权重乘子（稳定信号放大）
tag_boost: 1.0
imp_boost: 0.85
rec_boost: 1.1

recency_halflife_days: 26
strength_decay_per_hour: 0.9965
strength_reinforce_step: 0.04
prune_threshold_delta: 3
```

**Go 端接入**：
```go
// internal/service/config/learned.go
var learnedConfig atomic.Value // *LearnedConfig

func init() {
    go watchConfigFiles() // inotify / poll 每 60s 检查 mtime
}

// artifact_context.go 从里面读，读失败用硬编码默认
cfg := GetLearnedConfig(projectID)  // 不会 panic，失败返回默认
```

**兜底机制**（关键）：
- `train_samples < 500` → 写入"default"标记，Go 端用硬编码
- `eval_reward` 比上周降 >10% → 保留旧 yaml，新版放 `rrf.yaml.rejected`
- yaml 解析失败或字段缺失 → 单字段 fallback 到硬编码

**实现量**：Python 20 行（scipy + daily cron）+ Go 30 行加载器 + 1 张单元测试表。

### 5.3 L2 — Cross-encoder Reranker（高 ROI，推荐做）

**对象**：在 Phase 2 的 RRF 产出 top-50 后，再过一道 cross-encoder 精排成 top-K。

**Pipeline**：
```
 候选池 ~200
   │
   ▼ RRF 粗排（Phase 2 已有）
 top-50
   │
   ▼ cross-encoder(query, artifact_summary) —— L2 新增
 精排分
   │
   ▼ session 多样性 + budget（Phase 2 已有）
 top-K 注入
```

**模型选择**：
- 基线：`BAAI/bge-reranker-v2-m3`（~80MB，中英混合 + 代码友好）
- 微调：用我们的 `(query, artifact, L0/L2)` 做 pairwise contrastive learning
- **per-project** checkpoint（不共享——每个项目的"相关"定义不同）

**训练数据准备**：
```python
# ml/pipeline/make_reranker_data.py
for injection in last_90_days_injections:
    query = injection.query_text
    for ka in injection.injected_artifacts:
        if injection.audit_level == "L0" and ka.id in injection.attributed_to:
            pairs.append((query, ka.summary, label=1.0))
        elif injection.audit_level == "L2":
            pairs.append((query, ka.summary, label=0.0))
    # 加难负样本：top-50 里 rank 低 + L2 → 更明确的负例
```

**训练脚本**：`platform/backend/ml/train_reranker.py`
```
input:  ml/logs/injections_<project>.jsonl
output: ml/checkpoints/<project>/reranker/v<date>.pt
```

`sentence-transformers` 库 + `MultipleNegativesRankingLoss`，单 GPU 2-3 小时训完。

**Serving**：Python sidecar 新加端点
```
POST /rerank
  { 
    "project_id": "pid_xxx",
    "query": "...", 
    "candidates": [{"id":"ka_1","text":"..."}, ...] 
  }
→ [{ "id": "ka_1", "score": 0.87 }, ...]   // 按 score 降序
```

**Go 端接入**（改 `artifact_context.go`）：
```go
// Phase 2 的 RRF 结束后
rrfScored := applyRRF(candidates)
top50 := rrfScored[:min(50, len(rrfScored))]

if rerankerAvailable(q.ProjectID) {
    reranked, err := sidecar.Rerank(ctx, q.ProjectID, q.QueryText, top50)
    if err == nil {
        top50 = reranked
    }
    // 错误时静默降级到 RRF-only
}

return applyBudget(top50, q.Budget)
```

**冷启动**：没有 checkpoint → 直接跳过 rerank，走纯 RRF。Budget 和多样性约束照常生效。**第一次有 checkpoint 之前**和**没有 GPU 的小项目**都只跑 RRF。

**推理成本**：每次 query 10-30ms CPU（50 个候选批量推理），对我们 session-level 流程完全可忽略。

### 5.4 L5 — Audit-Risk 预测器（行为学级的改变）

**对象**：`change_submit` 处理器里，在提交给 audit 之前先算一次 `P(L2)`，高的直接返回 warning 给 agent。

**模型**：小型二分类器（LightGBM 起步，后期可换 distil-transformer）
```python
model: LightGBM (~ 100KB on disk)
input:  concat(
  diff_stats,                       # lines_added/deleted, files_touched, ast_ops_count
  task_desc_bge_embedding,          # 已有 bge sidecar
  tool_sequence_bigram_indicators,  # EpisodeGrouper 已输出
  agent_role_onehot,
  project_recent_L2_rate,           # SQL 聚合
  similar_task_historical_L2_rate,  # 按 task_tag 聚合
)
output: P(audit_level == L2)
```

**特征工程全 deterministic**：
- `diff_stats`：`go-git` 或自写 AST walker 现成可提
- `task_desc_embedding`：bge sidecar 已有 `/embed` 端点
- `tool_sequence_bigrams`：`refinery/pass_pattern_extractor.go` 里的现成逻辑
- 历史 L2 率：简单 SQL groupby

**使用场景**（按影响力排序）：
1. **Pre-submit warning**：`change_submit` 调用 `/risk`，若 `P(L2) > 0.4` 返回：
   ```
   "risk_warning": "This change resembles past L2-rejected changes in:
     - missing tests on modified public API (67% similarity)
     - large diff without rationale (58% similarity)
     Consider addressing before submission."
   ```
   给 agent 一次**主动改正**的机会，不是强制挡住。
2. **Chief routing**：Chief 派任务时，用 risk 预测决定"这个任务要不要指派给更成熟的 agent / 要不要多派一个 reviewer"
3. **Audit 先验**：给 audit agent 注入一个"历史上相似改动 L2 率 X%"的先验，让它的 attention 更集中

**训练**：LightGBM 分钟级训完，每周 cron。AUC 监控 + 类不平衡处理（scale_pos_weight）。

**兜底**：`/risk` 无响应 / 无模型 → warning 不发，流程完全透明。

### 5.5 训练数据管道

**原则**：不新增 DB 表。在现有 `Change`、`AuditVerdict`、`AgentSession` 基础上每日 ETL 成 JSONL。

```
platform/backend/ml/
├── configs/          # L1 产出
│   └── <project_id>/rrf.yaml
├── checkpoints/      # L2/L5 产出
│   └── <project_id>/
│       ├── reranker/v20260430.pt
│       └── risk/v20260430.lgb
├── logs/             # ETL 中间产物
│   └── <project_id>/
│       └── 2026-04-23.jsonl
└── pipeline/
    ├── export_daily.py   # DB → logs/*.jsonl
    ├── train_all.py      # logs → configs + checkpoints
    └── eval.py           # hold-out 评估 + 版本对比
```

**JSONL 格式**（每行一次 injection）：
```json
{
  "injection_id": "inj_xxx",
  "project_id": "pid_abc",
  "session_id": "ses_xxx",
  "role": "fix",
  "query_text": "...",
  "injected": [
    {
      "id": "ka_1",
      "summary": "...",
      "rrf_rank": 3,
      "raw_scores": {"sem": 0.81, "tag": 0.5, "imp": 0.3, "rec": 0.7}
    }
  ],
  "audit_level": "L0",
  "attribution": {"ka_1": 1.0, "ka_2": 0.0},
  "change_id": "cha_xxx",
  "diff_stats": {"lines_added": 23, "lines_deleted": 7, "files_touched": 2},
  "timestamp": "2026-04-23T10:00:00Z"
}
```

每项目每日一文件；训练用最近 30 天，eval 用最近 7 天（hold-out）。

### 5.6 版本管理与回滚

**为什么要重视**：训练出的权重学歪了**不像 playbook/delta_item 能 git diff 人肉审查**。必须有自动回归测试。

**目录结构**：
```
ml/checkpoints/<project_id>/reranker/
  v20260423.pt       # 最新
  v20260416.pt       # 上一版
  v20260409.pt
  best.json          # {"current": "v20260423.pt", "eval_ndcg": 0.78}
  history.jsonl      # 每次训练都 append 一行 eval 结果
```

**每次训练后自动**：
1. 在 hold-out eval set 上评估：
   - Reranker: `ndcg@10`
   - Risk 预测器: `AUC`
   - L1 configs: `reward_per_injection`
2. 对比 vs `best.json` 里的当前版本：
   - 新版 > 当前 → 提升指针到新版
   - 新版 ≥ 当前 × 0.95 → 保留 checkpoint，指针不动
   - 新版 < 当前 × 0.9 → **告警**，不切换，打 tag 让人 review
3. 运行时 Go/Python 端读 `best.json` 决定加载哪个

**一键回滚**：修改 `best.json.current` 指回上一版，不需要停服（Python sidecar hot-reload checkpoint）。

### 5.7 与 Phase 1/2 的接入点

| 接入位置 | Phase 2.5 改动 |
|---|---|
| `artifact_context.go::SelectArtifactsForInjection` | RRF 后加可选 reranker 调用（L2） |
| `curator/strength.go::DecayAllStrengths` | 读 L1 `rrf.yaml` 里的 `strength_decay_per_hour`，不再硬编码 |
| `curator/prune.go::Prune` | 读 L1 `prune_threshold_delta` |
| `service/change_submit.go`（或 handler 对应位置） | 调 L5 `/risk`，高时返回 warning |
| `service/chief.go::TaskRouting` | Chief 派任务时读 L5 risk 作为路由依据 |
| `agent/runner.go` 或类似 | audit agent 的 system prompt 注入 L5 历史先验 |

**现有表完全不动**。新增文件全在 `platform/backend/ml/` 下，CI 可以独立构建 Python 部分。

### 5.8 预期效果（行业 baseline 估计，**非保证**）

| 指标 | Phase 2 结束 | Phase 2.5 结束 | 主因 |
|---|---|---|---|
| L0 率 | ~58-62% | **65-72%** | L5 预警 + L2 精排 |
| 注入命中率（agent 实际引用比例） | ~35% | **50-65%** | L2 reranker 核心收益 |
| helpful/harmful 比 | ~3:1 | **5-7:1** | 注入更准 → 反馈更干净 |
| 平均 prompt token | X | **X · 0.85** | 精排后少注入也够用 |
| 冷启动到"有用"的天数 | ~14 天 | **7-10 天** | L1 暖启动全局默认 |
| 人工 approve 频率 | 每 2 周 | **每月** | 自动升降 + reranker 补强 |

数字是**基于 IR 领域 cross-encoder rerank 的典型收益 (nDCG +15-30%) 外推**的，我们场景可能更高也可能更低，**以 eval 为准**。

### 5.9 风险与对策

| 风险 | 对策 |
|---|---|
| 反馈回路污染（L1 标签权重配错，reranker 学坏） | eval set 跑 baseline 对比，偏离 >5% 告警；L1 reward 权重本身也参与 L1 超参学习 |
| 过拟合老项目（A 的模式套 B） | **per-project checkpoint**，不共享。硬盘便宜 |
| 讨好性退化（agent 学到"什么 artifact 得 L0" → 过度保守） | 每周跑多样性指标；exploration 比例 < 20% 报警；保留一定比例 random injection 做对照 |
| 权重不透明（不知 `rrf_k=54` 是否对） | yaml 里记录 `eval_reward` / `train_samples` / `prev_eval_reward`，可人工审 |
| 冷启动期 reranker 反而拖累 | `train_samples < threshold` 自动跳过 rerank；hold-out eval 失败时回退到纯 RRF |
| L5 误报导致 agent 过度谨慎 | `P(L2) > 0.4` 阈值本身用 L1 学出来；warning 里附相似度证据让 agent 判断 |

### 5.10 验收标准 Phase 2.5

- [ ] `platform/backend/ml/` 目录建立，`pipeline/export_daily.py` 能从 DB 产出 jsonl
- [ ] L1：`rrf.yaml` 每周自动产出；Go 端启动时 watch 文件变化，热加载
- [ ] L1：`train_samples < 500` 时回落到硬编码默认，单元测试覆盖
- [ ] L2：sidecar `/rerank` 端点 live；没 checkpoint 时静默 fallback 到纯 RRF
- [ ] L2：per-project checkpoint，`best.json` 版本管理就绪
- [ ] L5：`change_submit` 在 `P(L2) > 0.4` 时返回 warning（内容带相似度证据）
- [ ] L5：Chief `TaskRouting` 消费 risk 预测
- [ ] Hold-out eval 自动跑；新 checkpoint < 旧版 0.9 时告警
- [ ] 一键回滚：修改 `best.json.current` 后 Python sidecar 60s 内生效
- [ ] A/B：启用 vs 禁用整个 Phase 2.5 连跑 2 周，对比 L0 率 + 注入命中率

---

## 6. Phase 3 详细设计（1 周）

### 6.1 Session Insights Generator

```
internal/service/insights/
  generator.go   // OnSessionComplete(sessionID) — 触发生成
  prompts.go     // post-mortem 模板
```

触发点：`dispatcher.go` 的 session 完成钩子（现在已经有）。

LLM prompt（精简）：

```
You just completed a <role> session on task "<task_name>".
Final outcome: <L0/L1/L2/failed>.
Trajectory summary: <tool calls + key outputs>.

Extract 1-3 concise lessons in JSON:
[
  {
    "kind": "lesson" | "guard" | "recipe" | "anti_pattern",
    "content": "One sentence, actionable, generic enough to apply to future tasks of this kind",
    "scope": {"task_tags": [...], "file_categories": [...]}
  }
]

Do NOT repeat task-specific facts. Do NOT include file paths or variable names.
```

产出直接喂 Curator `Ingest`，绕过 `skill_candidate` 老路径（因为 post-mortem 本就是 ACE 意义上的 delta item）。

### 6.2 Per-role Playbook 注入

改 `session_context_builder.go`（如果没有就新建）：

```go
// 各 agent 启动时调用
func BuildPlaybookBlock(role, projectID string) string {
    items := curator.LoadPlaybook(role, projectID, 10) // top 10 delta items
    if len(items) == 0 {
        return ""
    }
    var sb strings.Builder
    sb.WriteString("## Playbook (learned from past sessions)\n\n")
    for _, item := range items {
        sb.WriteString(fmt.Sprintf("- [%s] %s\n", item.Kind, item.Content))
    }
    return sb.String()
}
```

注入位置：替换或增补现在"Refinery Knowledge Artifacts"段落。

### 6.3 Chief 应该先体验

所有 role 里，**Chief 最吃亏**——它做决策，但现在基本没吃到 Refinery 产出。先让 Chief 用上 per-role playbook，作为 Phase 3 的早期验证。

### 6.4 验收标准 Phase 3

- [ ] Session 结束自动生成 post-mortem（比例 > 80%，失败的更重要）
- [ ] 每个 role 有独立 playbook，LoadPlaybook 按 role 过滤
- [ ] Chief 的 system prompt 注入 playbook
- [ ] A/B：同一项目跑 20 个任务，对比启用/关闭 playbook 的 L0 率

---

## 7. 数据迁移策略

### 7.1 为什么不直接合并老表

`skill_candidate` / `policy` / `knowledge_artifact` 三张表分别对应不同产出源，schema 差异大。强行合表会打乱现有 Analyze 流和 Refinery 流。

### 7.2 采用并联，观察一段时间

```
Phase 1 落地后：
  新产出 → skill_candidate/policy/knowledge_artifact (老)   ← 读，用于注入（暂时）
         → delta_item (新)                                 ← 写入，积累信号

Phase 3 落地后：
  新产出 → skill_candidate/policy/knowledge_artifact (老) ← 继续写（考古价值）
         → delta_item (新)                              ← 读，用于注入
                                                         ← 写入，积累信号

观察期（~4 周）：
  如果 delta_item 表现好 → 老表变只读/废弃
  如果 delta_item 有问题 → 修复 Curator 逻辑
```

### 7.3 迁移脚本（一次性）

一次性把历史 `skill_candidate` / `knowledge_artifact` 的 active 条目倒入 `delta_item`：

```go
// cmd/migrate_delta/main.go
// 只跑一次，幂等（根据 ContentHash dedupe）
func importExistingArtifacts() {
    var artifacts []model.KnowledgeArtifact
    model.DB.Where("status = 'active'").Find(&artifacts)
    for _, a := range artifacts {
        curator.IngestOne(Candidate{
            Role:      inferRole(a.Kind),
            Kind:      "lesson",
            Content:   a.Summary,
            Scope:     inferScope(a.Payload),
            Source:    "migration:" + a.ProducedBy,
            SourceIDs: []string{a.ID},
        })
    }
    // 同样处理 skill_candidate
}
```

---

## 8. 观测性

Phase 1 起就要加指标（否则改动好坏全靠感觉）：

| 指标 | 采集点 | 解读 | 属于 Phase |
|---|---|---|---|
| `delta_item_count{role,status}` | 每次 Curator 运行 | 总量、按 role 分布 | 1 |
| `delta_item_helpful_harmful_ratio{kind}` | 每次 feedback 写入 | 按 kind 看哪类教训最准 | 1 |
| `injection_hit_rate` | 每次注入 | Agent 实际引用的比例（贡献度归因的副产物） | 1 |
| `l0_rate_by_has_playbook` | Audit 完成时 | A/B：有 playbook vs 没有 | 3 |
| `curator_merge_dedup_rate` | Curator 跑完 | 去重率，反映候选质量 | 1 |
| `curator_prune_rate` | Curator 跑完 | 下架率，反映"糟粕"比例 | 1 |
| `rrf_reward_per_injection` | L1 每周训练 | 标量学出来的超参比硬编码好多少 | 2.5 |
| `rrf_k_learned` / `halflife_days_learned` | L1 输出 | 看各项目收敛到什么值，扫异常 | 2.5 |
| `reranker_ndcg_at_10` | L2 hold-out eval | rerank 质量；new vs best.json | 2.5 |
| `reranker_fallback_rate` | 每次检索 | sidecar 不可用时 fallback 的比例 | 2.5 |
| `audit_risk_auc` | L5 hold-out eval | 预测器判别能力 | 2.5 |
| `pre_submit_warning_rate` | change_submit | 多少 change 收到预警；与 L2 相关性 | 2.5 |
| `post_warning_l2_rate` | warning 后 audit | 收到预警后 agent 主动修改后的 L2 率 | 2.5 |

所有指标先落 `log.Printf` + 写到 `refinery_run.PassStats` / `ml/history.jsonl`，将来接 Prometheus 简单。

---

## 9. 开放问题（待决定后再动工）

### Phase 1-3 相关

1. **Delta item content 多长合适**？ACE 论文里是一句话。我们的 task 比较具体（代码层），可能需要 2-3 句。需要实测。
2. **Post-mortem 触发频率**？每个 session 都跑 → LLM 成本可能高。可能只跑 L1/L2 session（"出错的才反思"）。
3. **矛盾检测**放在 Phase 几？agentmemory 有但我们目前 candidate 还不够多，暂时放 backlog。
4. **跨项目晋升**是否要接入 delta_item？现在 `GlobalPromoter` 只处理 knowledge_artifact。可能让 delta_item 先在单项目跑，成熟后再设计 global 层。
5. **Audit agent 用什么 role 的 playbook**？audit_1 和 audit_2 要分开吗？（两者任务不同）暂定分开，各自独立积累。

### Phase 2.5 相关

6. **L1 优化方法选哪个**？Nelder-Mead 简单，Thompson Sampling 更科学但实现重。先上 Nelder-Mead，观察收敛稳定性再升级。
7. **L2 reranker 要不要 per-role**？不同 role 的 query 分布差别大（chief 是决策语言，fix 是技术语言）。per-role checkpoint 理论更好但数据量要求高。先 per-project，观察数据再细化。
8. **L5 特征包括 agent 的正在进行的 dialogue 历史吗**？理论上 agent 已经说了什么很有信息量。但交叉信息泄漏风险（会反向影响 agent 说话）。暂定不用，仅用结果层特征。
9. **Python sidecar 独立进程 vs go-embed**？保持 sidecar（现有 bge embedding 已在这）还是移入 Go（onnxruntime-go）？先 sidecar，原因：迭代快、互操作性好、已经在用。
10. **训练资源怎么解决**？本机 GPU 有时没有。每周一次 2-3 小时的 GPU 时间可以考虑租用（vast.ai / runpod）或经济无压力的闲置 GPU。最差情况全 CPU 训练，bge-reranker 这种小模型 CPU 也能跑。
11. **是否为不同项目分别训练**？YES（per-project）。规则：项目数据量 < 500 条 → 用全局平均 checkpoint。
12. **checkpoint 大小会不会失控**？bge-reranker 单 checkpoint ~80MB，假设 20 个项目 = 1.6GB。暂可接受，超过再想合并策略。

---

## 10. 一句话收尾

**把我们从"定期蒸馏 + 人工 approve"推进到"每次 session 都在学习 + 自动升降"，再加一层"超参自演化"**。三个核心 Phase 完成之后 Curator 层成为系统的"中央记忆整理工"，Analyze/Refinery 降级成它的两个信息源；Phase 2.5 提供引擎下的"调参无人机"。

**开工顺序建议**：
- Phase 1 (2 周) → 用 1-2 周观察数据
- Phase 2 (1 周) → 同样观察一周
- Phase 3 (1 周) → 观察 2 周，让 playbook 积累起来
- Phase 2.5 在 Phase 2/3 后或并行开始（两者不冲突，数据管线能兼容）

中间任何一个 Phase 发现方向错就停，不要一口气四个全做了。Phase 2.5 尤其要注意：**它的附加值来自于前三个 Phase 的数据质量**，数据能驱动的信号不好时 Phase 2.5 会做负功。
