// Command embedcheck is a hands-on verification tool for the semantic
// injection pipeline. It does NOT use golden assertions — the only
// judge is your eyeballs.
//
// Setup it runs through:
//  1. Spin up an in-memory SQLite.
//  2. Seed ~12 realistic KnowledgeArtifacts written in a deliberately
//     "refinery-style" vocabulary (middleware, handler, preload, ...)
//  3. Embed them via the real bge-base-zh-v1.5 sidecar.
//  4. Feed ~10 "chat-style" task descriptions — phrased the way a PM or
//     client agent would ask, using NONE of the artifact keywords —
//     through SelectArtifactsForInjection.
//  5. Print top-5 candidates per task with score breakdown, plus a
//     "hoped-for match" hint so you can see at a glance whether the
//     retrieval is pulling the right artifact.
//
// The point is to stress-test "陌生任务 → 相关经验"—NOT "artifact keywords
// → the same artifact". If the top-1 for "用户登录一直弹 401" is the JWT
// recipe, the semantic path is working. If it's a database recipe, we
// have a problem.
//
// Run:
//   cd platform/backend
//   go run ./cmd/embedcheck
//
// Requires the embedder sidecar running at http://127.0.0.1:3011
// (or A3C_EMBEDDER_URL).
package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/a3c/platform/internal/model"
	"github.com/a3c/platform/internal/service"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// --- Seed artifacts -------------------------------------------------------
//
// Worded deliberately in "refinery output" style: terse, mentions the
// concrete tool names and code vocabulary. Each one has a short ID we
// use below to assert hoped-for matches. These IDs are display-only;
// the selector ranks by embedding similarity, not by ID.

type seed struct {
	id      string // short display-only tag for "expected match" column
	kind    string
	name    string
	summary string
}

var artifacts = []seed{
	{
		id: "auth-jwt-recipe", kind: "tool_recipe",
		name:    "recipe: JWT 签名验证失败排查",
		summary: "定位签发端 secret 配置 → 对比解析端算法 → 检查 header alg 字段 → 验证 exp 过期字段 → 回看中间件注入顺序",
	},
	{
		id: "auth-mw-antipattern", kind: "anti_pattern",
		name:    "anti: 盲改鉴权中间件",
		summary: "同一个 auth middleware 短时间内被多次编辑却没有先 read 请求生命周期代码，通常是根因理解偏差",
	},
	{
		id: "i18n-recipe", kind: "tool_recipe",
		name:    "recipe: 界面文案本地化",
		summary: "查找 i18n 资源目录 → 在默认语言增加 key → 同步补齐其他 locale → 在视图模板使用 $t/useTranslation",
	},
	{
		id: "rest-handler-pattern", kind: "pattern",
		name:    "pattern: 新增 REST 路由标准流",
		summary: "声明 handler struct → 在路由表注册 → 实现业务逻辑 → 补调用者身份校验 → 返回标准 envelope",
	},
	{
		id: "ownership-antipattern", kind: "anti_pattern",
		name:    "anti: 忽略归属校验直接返回资源",
		summary: "在接口实现处未校验调用者身份与资源归属，造成越权读取他人数据",
	},
	{
		id: "schema-migrate-recipe", kind: "tool_recipe",
		name:    "recipe: GORM schema 演进",
		summary: "在 model 上增加字段 → 运行 AutoMigrate → 补默认值 → 写回填历史数据的 batch job",
	},
	{
		id: "worktree-antipattern", kind: "anti_pattern",
		name:    "anti: PR 合并后未清理 worktree",
		summary: "feature 分支合并 main 之后未删除工作区目录,agent 继续在旧分支上写入已合并的代码",
	},
	{
		id: "docker-slim-recipe", kind: "tool_recipe",
		name:    "recipe: 镜像瘦身",
		summary: "多阶段构建 → base 选 alpine 或 distroless → 清理构建期依赖 → 启用 BuildKit 缓存与 squash",
	},
	{
		id: "secret-antipattern", kind: "anti_pattern",
		name:    "anti: 密钥硬编码",
		summary: "把 API key 或访问令牌直接写在配置文件或源代码里,一旦仓库外泄则全量暴露",
	},
	{
		id: "nplus1-antipattern", kind: "anti_pattern",
		name:    "anti: 数据库 N+1 查询",
		summary: "在循环中逐条查询关联表,应改写为 preload 或 JOIN 以避免网络往返爆炸",
	},
	{
		id: "logging-pattern", kind: "pattern",
		name:    "pattern: 结构化日志埋点",
		summary: "在错误边界注入 request_id → 记录输入参数摘要 → 标记异常层级便于告警聚合",
	},
	{
		id: "sql-inject-antipattern", kind: "anti_pattern",
		name:    "anti: SQL 字符串拼接",
		summary: "在查询里用 fmt.Sprintf 拼接用户传入字符串,产生 SQL 注入风险,应使用参数化查询",
	},
}

// --- Task probes ---------------------------------------------------------
//
// Each one is phrased like a real user / client agent would ask.
// *** NONE of the artifact keywords above appear verbatim here. ***
// The "hoped" field is what I (the author) think the top-1 artifact
// should be. You look at the output and decide whether the selector
// is actually retrieving sensible things.

type probe struct {
	task  string
	hoped string // id of the artifact I expect to see near the top
}

var probes = []probe{
	{task: "用户登录总是弹 401 认证失败,排查一下", hoped: "auth-jwt-recipe"},
	{task: "给首页加一个中英文切换按钮", hoped: "i18n-recipe"},
	{task: "新增一个查询所有用户的接口 /api/users", hoped: "rest-handler-pattern"},
	{task: "数据库里加个 last_login 字段记录最后登录时间", hoped: "schema-migrate-recipe"},
	{task: "有个 PR 合并之后还能看到旧代码,怎么回事", hoped: "worktree-antipattern"},
	{task: "我们的后端接口响应很慢,我在循环里查了关联表", hoped: "nplus1-antipattern"},
	{task: "怎么让 docker 镜像再小一点,现在几百兆了", hoped: "docker-slim-recipe"},
	{task: "能不能给系统加点可观测性,报错的时候看不到上下文", hoped: "logging-pattern"},
	{task: "我把 aws 访问密钥放进 config.yaml 了,会有问题吗", hoped: "secret-antipattern"},
	{task: "这个看板有漏洞,别人能看到其他人的订单数据", hoped: "ownership-antipattern"},
}

// --- Wiring --------------------------------------------------------------

func main() {
	db := mustOpenSQLite()
	model.DB = db

	// Embed + persist each artifact via the live bge sidecar.
	client := service.DefaultEmbeddingClient()
	healthCheck(client)

	fmt.Println("\n── Embedding seed artifacts ─────────────────────────────────────")
	for i, s := range artifacts {
		text := fmt.Sprintf("[%s] %s\n%s", s.kind, s.name, s.summary)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		vecs, err := client.EmbedDocuments(ctx, []string{text})
		cancel()
		if err != nil {
			log.Fatalf("embed seed %q: %v", s.id, err)
		}
		ka := &model.KnowledgeArtifact{
			ID:           s.id,
			ProjectID:    "p1",
			Kind:         s.kind,
			Name:         s.name,
			Summary:      s.summary,
			Status:       "active",
			Confidence:   0.8,
			Version:      1,
			Embedding:    service.MarshalEmbedding(vecs[0]),
			EmbeddingDim: len(vecs[0]),
			CreatedAt:    time.Now(),
			UpdatedAt:    time.Now(),
		}
		if err := db.Create(ka).Error; err != nil {
			log.Fatalf("persist seed %q: %v", s.id, err)
		}
		fmt.Printf("  %2d. [%-13s] %-40s (%d-dim)\n", i+1, s.kind, truncate(s.name, 40), len(vecs[0]))
	}

	// Evaluate each probe — this is where we see whether "陌生任务
	// → 对口经验" actually works.
	fmt.Println("\n── Running probes ───────────────────────────────────────────────")
	hits := 0
	top3Hits := 0
	for i, p := range probes {
		result := service.SelectArtifactsForInjection(context.Background(), service.ArtifactQuery{
			ProjectID: "p1",
			Audience:  service.AudienceCoder,
			QueryText: p.task,
		})

		// Determine whether our hoped-for artifact appeared, and where.
		hopedRank := -1
		for idx, ia := range result {
			if ia.Artifact.ID == p.hoped {
				hopedRank = idx + 1
				break
			}
		}

		verdict := "✗ miss"
		switch {
		case hopedRank == 1:
			verdict = "✓ #1"
			hits++
			top3Hits++
		case hopedRank >= 2 && hopedRank <= 3:
			verdict = fmt.Sprintf("~ #%d", hopedRank)
			top3Hits++
		case hopedRank > 3:
			verdict = fmt.Sprintf("↓ #%d", hopedRank)
		}

		fmt.Printf("\n%02d. %s    [hoped=%s  →  %s]\n", i+1, p.task, p.hoped, verdict)
		fmt.Println("    " + strings.Repeat("-", 70))

		limit := 5
		if len(result) < limit {
			limit = len(result)
		}
		for rank := 0; rank < limit; rank++ {
			ia := result[rank]
			marker := "  "
			if ia.Artifact.ID == p.hoped {
				marker = "★ "
			}
			fmt.Printf("    %s#%d  score=%.3f  [%s] %s\n",
				marker, rank+1, ia.Score, ia.Artifact.Kind, ia.Artifact.Name)
			fmt.Printf("         reason: %s\n", ia.Reason)
		}
	}

	// Summary — not a pass/fail, just aggregate numbers so trends over
	// future tuning runs are visible.
	fmt.Println("\n── Summary ──────────────────────────────────────────────────────")
	fmt.Printf("  probes:         %d\n", len(probes))
	fmt.Printf("  top-1 match:    %d / %d  (%.0f%%)\n", hits, len(probes), 100*float64(hits)/float64(len(probes)))
	fmt.Printf("  top-3 match:    %d / %d  (%.0f%%)\n", top3Hits, len(probes), 100*float64(top3Hits)/float64(len(probes)))
	fmt.Println()
	fmt.Println("  Judge by eye:")
	fmt.Println("    - Is top-1 sensible even when the hoped artifact ranks #2-3?")
	fmt.Println("    - Are the non-hoped candidates at least plausible neighbours?")
	fmt.Println("    - Do the scores spread (big gap = confident match; flat = weak signal)?")
}

// --- Helpers -------------------------------------------------------------

func mustOpenSQLite() *gorm.DB {
	db, err := gorm.Open(sqlite.Open("file:embedcheck?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		log.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&model.KnowledgeArtifact{}, &model.Task{}); err != nil {
		log.Fatalf("migrate: %v", err)
	}
	return db
}

func healthCheck(client *service.EmbeddingClient) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	h, err := client.Health(ctx)
	if err != nil {
		log.Fatalf("embedder /health failed — is the sidecar running on :3011?  error: %v", err)
	}
	fmt.Printf("── Sidecar healthy ────────────────────────────────────────────────\n")
	fmt.Printf("  model=%s  dim=%d  device=%s  batch=%d\n", h.Model, h.Dim, h.Device, h.BatchSize)
}

func truncate(s string, n int) string {
	// Rune-aware truncation for CJK; we want ~40 visible cells.
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
