package agentpool

// Skill injection at spawn time.
//
// Every platform-hosted agent starts with the same baseline skill
// set — the "how to use this platform" skill from client/skill, plus
// every SkillCandidate currently active in the DB. This gives the
// freshly-spawned agent instant fluency: it knows about the A3C MCP
// tools (task.claim / change.submit / PR flow) without the operator
// manually symlinking anything.
//
// opencode reads skills from `<workdir>/.claude/skills/*/SKILL.md`
// — the same convention Claude Code uses. We create one subfolder
// per skill, with a SKILL.md inside. opencode's startup scan picks
// them up automatically, no extra config.
//
// The baseline "using-a3c-platform" skill lives in the repo under
// client/skill/. We look for it relative to the current working
// directory (where the platform binary runs) and copy it if found;
// absence is not fatal (the DB-backed skills still load).

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/a3c/platform/internal/model"
)

// materialiseSkills writes every active skill into
// <workDir>/.claude/skills/<slug>/SKILL.md. Returns the list of skill
// names that were materialised (for telemetry / UI).
//
// Best-effort per skill — if one fails to write, we log and continue.
// Missing a single skill shouldn't brick the whole spawn.
//
// Also drops an AGENTS.md at workDir root — opencode auto-picks that
// up as system context, which is the only place we can put hard
// rules that the runtime respects without the LLM "deciding" to
// read them. Skills under .claude/skills/ are advisory; AGENTS.md
// is load-bearing. The 2026-04 tasq-experiment session showed
// pool workers happily ignoring SKILL.md and reading the platform
// source tree (one level up from workDir) because nothing was
// forcing them back to `.a3c_staging/`. AGENTS.md fixes that.
func materialiseSkills(workDir string) ([]string, error) {
	// Write boundary rules first — this is the file opencode reads
	// unconditionally on session start. Best-effort: a write error
	// here is logged but doesn't fail the whole spawn, because
	// skills still give the agent partial guidance.
	if err := writeAgentsMD(workDir); err != nil {
		// Surface but don't block — an agent without AGENTS.md is
		// still functional, just prone to the sandbox-escape
		// behaviour documented in misc/bench/tasq-experiment/.
		// Keep the log terse; operators grepping for "AGENTS.md"
		// should find it.
		fmt.Fprintf(os.Stderr, "[Pool] warn: write AGENTS.md: %v\n", err)
	}

	skillsDir := filepath.Join(workDir, ".claude", "skills")
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir skills dir: %w", err)
	}

	injected := []string{}

	// 1. The baseline platform skill — copied from client/skill if
	// available on this machine. Finding it is best-effort: we walk
	// up from cwd looking for `client/skill/using-a3c-platform` up
	// to 3 levels, since the server is usually launched from
	// platform/backend/.
	if name, ok := copyBaselineSkill(skillsDir); ok {
		injected = append(injected, name)
	}

	// 1.5 Built-in convention skills that always ship with every
	// platform-hosted agent regardless of DB state. These are the
	// "operating system" rules a platform-native agent needs to be
	// a good citizen — e.g. reading OVERVIEW.md before exploring.
	// Independent of the DB-backed skills so they stay available
	// even on a fresh install with no skill_candidate rows yet.
	injected = append(injected, writeBuiltinSkills(skillsDir)...)

	// 2. Every active SkillCandidate from the DB. These are the
	// skills the Analyze agent has distilled + humans approved.
	// DB unset (e.g. in unit tests) = skip silently; the baseline
	// skill is enough for a pool agent to come up and claim tasks.
	if model.DB == nil {
		return injected, nil
	}
	var skills []model.SkillCandidate
	if err := model.DB.Where("status = ?", "active").Find(&skills).Error; err != nil {
		// DB read failure is not fatal — log and keep going.
		return injected, nil
	}
	for _, s := range skills {
		slug := slugify(s.Name)
		if slug == "" {
			continue
		}
		dir := filepath.Join(skillsDir, slug)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			continue
		}
		md := renderSkillMarkdown(s)
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(md), 0o644); err != nil {
			continue
		}
		injected = append(injected, slug)
	}

	return injected, nil
}

// writeBuiltinSkills materialises skills that always ship with every
// platform-hosted agent regardless of what the skill_candidate DB
// contains. These encode platform-wide conventions that a
// platform-native agent needs to follow to be a good citizen.
//
// Keep each skill's SKILL.md self-contained — opencode / Claude Code
// read them independently, so they can't rely on each other for
// context. Best-effort per skill: a write failure on one does not
// block the others.
//
// Returns the slugs of skills actually materialised.
func writeBuiltinSkills(skillsDir string) []string {
	injected := []string{}
	builtins := []struct {
		slug    string
		content string
	}{
		{
			slug:    "project-overview-read",
			content: projectOverviewReadSkill,
		},
	}
	for _, b := range builtins {
		dir := filepath.Join(skillsDir, b.slug)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(b.content), 0o644); err != nil {
			continue
		}
		injected = append(injected, b.slug)
	}
	return injected
}

// projectOverviewReadSkill is the SKILL.md body for the built-in
// project-overview-read skill. Kept as a package-level constant so
// unit tests can assert against its contents without file I/O.
const projectOverviewReadSkill = `---
name: project-overview-read
description: "Read OVERVIEW.md at the project repo root before anything else — it is the project's living map (summary, directory structure, key files, recent structural changes) so you don't have to re-explore the codebase every session."
source: a3c-platform/builtin
type: convention
---

# project-overview-read

## When to apply

At the very start of every session, before any grep / read on source files,
before task execution, before any code edit. Also re-check it after every
` + "`file_sync`" + ` that returns updates, in case a teammate updated the overview.

## What to do

1. After ` + "`file_sync`" + `, open ` + "`OVERVIEW.md`" + ` at the project repo root (in
   ` + "`.a3c_staging/<project>/full/OVERVIEW.md`" + ` or
   ` + "`.a3c_staging/<project>/branch/<branch_id>/OVERVIEW.md`" + ` depending on mode).
2. Use it as your mental map:
   - **Summary** tells you what this project is.
   - **Structure** tells you where code lives.
   - **Key Files** tells you what each routinely-touched file does.
   - **Recent Structural Changes** tells you what just moved/renamed.
3. Only grep / read individual source files when OVERVIEW.md lacks the detail
   you need for the current task. This saves real time — don't skip it.

## Keep it fresh

When your change touches structural code — adds a new file, moves/removes
files, renames exported symbols, splits or merges modules — **edit
` + "`OVERVIEW.md`" + ` in the same ` + "`change_submit`" + ` call** that ships the code change.

- Update **Key Files** for added / renamed / deleted files.
- Update **Structure** when you introduce or remove a top-level directory.
- Prepend one line to **Recent Structural Changes** describing what changed.

The ` + "`change_submit`" + ` response includes an ` + "`overview_reminder`" + ` field when the
server sees structural code changes without an OVERVIEW update. Treat that
reminder as a strong hint — audit still runs, but future agents need the
updated map.

## What NOT to do

- Don't skip reading OVERVIEW.md to "save time" — re-exploration costs more.
- Don't blindly trust OVERVIEW.md when it contradicts the actual code. If
  you see drift, fix OVERVIEW.md as part of your change.
- Don't add long essays to OVERVIEW.md. Keep each entry terse — one line
  per file, one sentence per section update.

---

_Auto-generated by the A3C platform agent pool at spawn time._
`

// copyBaselineSkill walks up from cwd looking for
// client/skill/using-a3c-platform and copies its SKILL.md into the
// instance's skills dir. Returns the injected skill's folder name +
// whether anything was copied.
func copyBaselineSkill(skillsDir string) (string, bool) {
	candidates := []string{
		filepath.Join("client", "skill", "using-a3c-platform"),
		filepath.Join("..", "client", "skill", "using-a3c-platform"),
		filepath.Join("..", "..", "client", "skill", "using-a3c-platform"),
		filepath.Join("..", "..", "..", "client", "skill", "using-a3c-platform"),
	}
	src := ""
	for _, c := range candidates {
		if _, err := os.Stat(filepath.Join(c, "SKILL.md")); err == nil {
			src = c
			break
		}
	}
	if src == "" {
		return "", false
	}
	dst := filepath.Join(skillsDir, "using-a3c-platform")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return "", false
	}
	if err := copyFile(filepath.Join(src, "SKILL.md"), filepath.Join(dst, "SKILL.md")); err != nil {
		return "", false
	}
	// Copy references/ subdir if present — the skill links to it.
	refs := filepath.Join(src, "references")
	if info, err := os.Stat(refs); err == nil && info.IsDir() {
		_ = copyDir(refs, filepath.Join(dst, "references"))
	}
	return "using-a3c-platform", true
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func copyDir(src, dst string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		s := filepath.Join(src, e.Name())
		d := filepath.Join(dst, e.Name())
		if e.IsDir() {
			if err := copyDir(s, d); err != nil {
				return err
			}
			continue
		}
		if err := copyFile(s, d); err != nil {
			return err
		}
	}
	return nil
}

// slugify converts a skill name into a filesystem-safe folder name.
// Keeps ASCII alnum + hyphen; collapses runs of other characters to
// single dashes. Empty result means "don't materialise this skill".
func slugify(name string) string {
	var sb strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			sb.WriteRune(r)
			prevDash = false
		default:
			if !prevDash && sb.Len() > 0 {
				sb.WriteRune('-')
				prevDash = true
			}
		}
	}
	s := strings.TrimRight(sb.String(), "-")
	if len(s) > 60 {
		s = s[:60]
	}
	return s
}

// renderSkillMarkdown turns a SkillCandidate DB row into a SKILL.md
// conforming to the Claude/opencode skill format: YAML frontmatter
// with name + description, then the body. Keep it ≤ 200 lines.
func renderSkillMarkdown(s model.SkillCandidate) string {
	desc := s.Action
	if desc == "" {
		desc = fmt.Sprintf("%s (%s) — auto-loaded from platform skill library", s.Name, s.Type)
	}
	// YAML-safe: strip embedded quotes from description.
	desc = strings.ReplaceAll(desc, "\"", "'")

	var sb strings.Builder
	sb.WriteString("---\n")
	sb.WriteString(fmt.Sprintf("name: %s\n", s.Name))
	sb.WriteString(fmt.Sprintf("description: \"%s\"\n", desc))
	sb.WriteString(fmt.Sprintf("source: a3c-platform/skill_candidate/%s\n", s.ID))
	sb.WriteString(fmt.Sprintf("type: %s\n", s.Type))
	sb.WriteString("---\n\n")

	sb.WriteString(fmt.Sprintf("# %s\n\n", s.Name))
	if s.Precondition != "" {
		sb.WriteString("## When to apply\n")
		sb.WriteString(s.Precondition)
		sb.WriteString("\n\n")
	}
	if s.Action != "" {
		sb.WriteString("## What to do\n")
		sb.WriteString(s.Action)
		sb.WriteString("\n\n")
	}
	if s.Prohibition != "" {
		sb.WriteString("## What NOT to do\n")
		sb.WriteString(s.Prohibition)
		sb.WriteString("\n\n")
	}
	if s.Evidence != "" {
		sb.WriteString("## Evidence\n")
		sb.WriteString(s.Evidence)
		sb.WriteString("\n\n")
	}
	sb.WriteString("---\n")
	sb.WriteString("_Auto-generated by the A3C platform agent pool at spawn time. Updates propagate on next spawn._\n")
	return sb.String()
}

// writeAgentsMD drops an AGENTS.md at workDir root. opencode reads
// AGENTS.md at session start and includes it as system-prompt
// context, which is strictly stronger than `.claude/skills/*/SKILL.md`
// (advisory) or broadcast payloads (only seen after TASK_ASSIGN).
//
// Contents are deliberately short and numbered — the 2026-04 tasq
// experiment showed long advisory prose (SKILL.md's 150 lines) was
// ignored; a terse numbered list that opens with hard prohibitions
// tends to survive LLM compression into concrete behaviour.
func writeAgentsMD(workDir string) error {
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return fmt.Errorf("mkdir workDir for AGENTS.md: %w", err)
	}
	return os.WriteFile(filepath.Join(workDir, "AGENTS.md"), []byte(agentsMDContent), 0o644)
}

// agentsMDContent is the root-level system-prompt context every
// platform-hosted pool worker starts with. Keep it focused on the
// two things the LLM keeps getting wrong if left to its own devices:
//
//   1. Working-directory boundary — do NOT read/edit parent dirs.
//      Pool workdir sits inside the platform data tree, so "glob **"
//      or a `task` sub-agent exploration will silently walk into
//      platform source code. AGENTS.md is the only place we can
//      plant a rule strong enough to make reasoning models like
//      MiniMax-M2.7 respect the boundary.
//
//   2. Core loop contract — file_sync → filelock → write staging
//      → change_submit. Without this written up front the LLM will
//      helpfully invent its own flow ("let me just read files and
//      write them"), which ships changes that bypass audit.
//
// Everything else (project context, task description) gets added
// per-assignment via the TASK_ASSIGN broadcast payload; AGENTS.md
// only carries the invariants.
const agentsMDContent = `# A3C Platform-Hosted Pool Worker — Non-negotiable Rules

You are running inside the A3C platform's agent pool. These rules
apply to EVERY task you work on. They override your general
instincts about "how to explore a codebase".

## Hard prohibitions (violation = task marked failed)

1. **Never read or edit files outside this working directory.** Your
   CWD is your sandbox. ` + "`../`" + `, absolute paths to
   ` + "`D:\\claude-code\\`" + `, ` + "`/Users/`" + `, etc. are all OFF LIMITS. If you
   find yourself typing ` + "`read filePath=../`" + ` or
   ` + "`glob pattern=/**/...`" + `, STOP. The project you are working on is NOT
   the A3C platform itself — it lives behind ` + "`file_sync`" + `.
2. **Never touch ` + "`.opencode/`" + `, ` + "`.claude/`" + `, or ` + "`AGENTS.md`" + `** (these files).
   They are platform-managed. Pretend they're read-only.
3. **Never explore with opencode's built-in ` + "`task`" + ` (sub-agent) tool
   as a substitute for ` + "`file_sync`" + `.** Sub-agents inherit your CWD
   and will escape the boundary above. Use the A3C ` + "`file_sync`" + ` tool
   to get project files, not sub-agent exploration.

## Core loop — follow this exact sequence every task

    1. Wait for a TASK_ASSIGN broadcast. The payload carries
       task_id, task_name, description, and a PROJECT CONTEXT
       header with direction + current milestone. Read it.
    2. ` + "`a3c_task action=claim task_id=<id>`" + ` — officially take the
       task. Platform won't let two agents claim the same one.
    3. ` + "`a3c_file_sync`" + ` — pulls current project files into
       ` + "`.a3c_staging/<project_id>/full/`" + `. You MUST do this before
       reading anything about the project. Returns a ` + "`version`" + ` you'll
       need when submitting.
    4. ` + "`read .a3c_staging/<project_id>/full/OVERVIEW.md`" + ` — project's
       living map. Tells you where things are.
    5. ` + "`a3c_filelock action=acquire files=[...] task_id=<id>`" + ` — lock
       exactly the files you plan to write. Required; the server
       rejects changes that touch files you didn't lock.
    6. Edit inside ` + "`.a3c_staging/<project_id>/full/...`" + ` — NOT in the
       CWD root, NOT outside staging.
    7. ` + "`a3c_change_submit writes=[{path,content}] version=<from step 3>`" + `.
       Response has ` + "`next_action`" + `: ` + "`done`" + ` / ` + "`wait`" + ` / ` + "`revise`" + `. Act on
       that, not on the status string.
    8. ` + "`a3c_feedback task_id=<id> outcome=success|partial|failed`" + ` with
       one key_insight. This is how the platform learns.

## What to do when confused

- Description ambiguous? Call ` + "`a3c_status_sync`" + ` to see project
  direction + milestone before making assumptions about what
  language/framework/style to use.
- Don't know which files to touch? ` + "`a3c_project_info query=\"<your question>\"`" + `
  asks a Consult agent — it has read access to OVERVIEW and can
  point you at the right module.
- Tempted to ` + "`read ../something`" + ` to "understand the bigger picture"?
  STOP. The bigger picture lives in OVERVIEW.md and
  ` + "`a3c_status_sync`" + `. Everything else is out of bounds.

---

_This file is managed by the A3C platform. It is regenerated on
every pool spawn; don't edit it — your edits will vanish._
`

