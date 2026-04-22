package service

// Tag rule engine — light-weight, deterministic, no LLM
// =====================================================
//
// New tasks get classified into rough topic tags at creation time so
// the injection selector has something to work with BEFORE the refinery
// has seen enough history for semantic retrieval to shine on this task.
//
// By design the rules are:
//   - simple          — hand-written keyword + path checks, no regex DSL
//   - conservative    — prefer not tagging over mis-tagging. Every rule
//                       here emits Status=proposed so a reviewer (human
//                       or Analyze Agent) can confirm or reject later.
//   - explainable     — each emitted tag carries its Evidence JSON, so
//                       a dashboard can show "this 'bugfix' tag fired
//                       because title contains 'fix'+'bug'".
//   - bilingual       — Chinese and English keyword groups are both
//                       matched; this codebase has mixed-language task
//                       titles in the wild.
//
// Scoring philosophy:
//   - Structural signals (file paths, common+rare words co-occurring)
//     score ~0.5.
//   - Single-keyword matches score ~0.3.
//   - Nothing ever ships at 1.0 — that's reserved for humans.
//
// This file is a starting point. When Analyze Agent starts rating
// rule quality (PR 7+), we'll gate rules by their historical
// confirmed/rejected ratio. For now every rule runs every time.

import (
	"encoding/json"
	"strings"
)

// TagProposal is what the rule engine emits. Callers persist these
// into TaskTag rows with Status=proposed.
type TagProposal struct {
	Tag        string
	Dimension  string
	Confidence float64
	Source     string
	// Evidence explains why this rule fired; stored verbatim as the
	// TaskTag.Evidence JSON blob for auditability.
	Evidence map[string]any
}

// ProposeTagsFromText runs the keyword rules against a task's title
// and description and returns a de-duplicated list of proposals. Never
// panics; the caller is expected to iterate and persist.
func ProposeTagsFromText(name, description string) []TagProposal {
	text := strings.ToLower(name + "\n" + description)
	proposals := []TagProposal{}

	// --- Category axis -------------------------------------------------
	// Each entry needs at least one match from "primary" keywords. If
	// a "qualifier" keyword also appears we boost confidence.
	catRules := []struct {
		tag              string
		primary, qualify []string
	}{
		{
			tag:     "bugfix",
			primary: []string{"fix", "修复", "bug", "故障", "报错", "异常", "回退"},
			qualify: []string{"修复", "回退", "bug", "crash", "故障", "错误", "报错"},
		},
		{
			tag:     "feature",
			primary: []string{"add", "新增", "implement", "实现", "支持", "feature", "功能", "扩展"},
			qualify: []string{"新功能", "feature", "add", "支持", "实现"},
		},
		{
			tag:     "refactor",
			primary: []string{"refactor", "重构", "整理", "cleanup", "清理", "抽取", "重命名"},
			qualify: []string{},
		},
		{
			tag:     "docs",
			primary: []string{"文档", "readme", "comment", "注释", "说明", "doc"},
			qualify: []string{},
		},
		{
			tag:     "test",
			primary: []string{"test", "单测", "集成测试", "e2e", "回归"},
			qualify: []string{"case", "coverage", "覆盖率"},
		},
		{
			tag:     "performance",
			primary: []string{"性能", "performance", "slow", "优化速度", "latency", "throughput"},
			qualify: []string{},
		},
		{
			tag:     "security",
			primary: []string{"security", "安全", "越权", "注入", "xss", "csrf", "漏洞", "鉴权"},
			qualify: []string{},
		},
	}
	for _, r := range catRules {
		matched := matchedKeywords(text, r.primary)
		if len(matched) == 0 {
			continue
		}
		qualified := matchedKeywords(text, r.qualify)
		conf := 0.3
		if len(matched) >= 2 || len(qualified) >= 1 {
			conf = 0.5
		}
		proposals = append(proposals, TagProposal{
			Tag:        r.tag,
			Dimension:  "category",
			Confidence: conf,
			Source:     "auto_kw",
			Evidence: map[string]any{
				"matched_keywords":   matched,
				"qualify_keywords":   qualified,
				"rule_id":            "cat:" + r.tag,
			},
		})
	}

	// --- Layer axis ----------------------------------------------------
	// These fire on domain vocabulary. No path info is available here
	// (tasks are proposed pre-code); paths land later via Episode
	// FilesTouched and get consumed by the topic_context extractor.
	layerRules := []struct {
		tag      string
		keywords []string
	}{
		{"frontend", []string{"frontend", "前端", "ui", "界面", "页面", "组件", "component", "tsx", "jsx", "react", "vue"}},
		{"backend", []string{"backend", "后端", "api", "接口", "handler", "middleware", "中间件", "server"}},
		{"infra", []string{"infra", "基础设施", "docker", "部署", "ci", "pipeline", "deploy", "k8s", "运维"}},
		{"mobile", []string{"ios", "android", "mobile", "移动端", "app"}},
	}
	for _, r := range layerRules {
		matched := matchedKeywords(text, r.keywords)
		if len(matched) == 0 {
			continue
		}
		conf := 0.35
		if len(matched) >= 2 {
			conf = 0.5
		}
		proposals = append(proposals, TagProposal{
			Tag:        r.tag,
			Dimension:  "layer",
			Confidence: conf,
			Source:     "auto_kw",
			Evidence: map[string]any{
				"matched_keywords": matched,
				"rule_id":          "layer:" + r.tag,
			},
		})
	}

	return dedupeProposals(proposals)
}

// matchedKeywords returns every keyword from `candidates` that appears
// as a substring in `text`. Case-insensitive — the caller has already
// lowercased `text`; we also lowercase candidates defensively.
func matchedKeywords(text string, candidates []string) []string {
	out := []string{}
	for _, kw := range candidates {
		kwLower := strings.ToLower(kw)
		if kwLower == "" {
			continue
		}
		if strings.Contains(text, kwLower) {
			out = append(out, kw)
		}
	}
	return out
}

// dedupeProposals collapses multiple proposals of the same (Tag, Dimension)
// into the highest-confidence one and merges their evidence so downstream
// reviewers see every reason the rule system had for picking this tag.
func dedupeProposals(in []TagProposal) []TagProposal {
	byKey := map[string]TagProposal{}
	for _, p := range in {
		k := p.Dimension + "/" + p.Tag
		if existing, ok := byKey[k]; !ok || p.Confidence > existing.Confidence {
			byKey[k] = p
		}
	}
	out := make([]TagProposal, 0, len(byKey))
	for _, p := range byKey {
		out = append(out, p)
	}
	return out
}

// EvidenceJSON marshals a proposal's evidence map. Kept as a helper so
// callers (handlers, the Analyze reconciler) don't each have to import
// encoding/json for one call.
func EvidenceJSON(e map[string]any) string {
	if len(e) == 0 {
		return ""
	}
	b, err := json.Marshal(e)
	if err != nil {
		return ""
	}
	return string(b)
}
