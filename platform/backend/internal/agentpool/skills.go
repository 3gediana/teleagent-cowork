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
func materialiseSkills(workDir string) ([]string, error) {
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
