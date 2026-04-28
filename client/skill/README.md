# A3C Client Skills

Skills for agent harnesses (OpenCode) that connect to the A3C platform.

## Available skills

### `using-a3c-platform/`

Quick onboarding for client agents. Activates when the a3c MCP server is connected and the agent needs to claim tasks, submit changes, or submit PRs.

## Install

Copy or symlink the skill folder into your agent harness's skills directory.

**OpenCode (Windows)**:
```powershell
$src = Resolve-Path "client/skill/using-a3c-platform"
$dst = "$env:USERPROFILE\.claude\skills\using-a3c-platform"
New-Item -ItemType SymbolicLink -Path $dst -Target $src -Force
```

**OpenCode (macOS/Linux)**:
```bash
ln -sfn "$(pwd)/client/skill/using-a3c-platform" "$HOME/.claude/skills/using-a3c-platform"
```

Or just copy the folder if you prefer not to symlink.

## Structure

Each skill follows the standard skill structure:
```
<skill-name>/
├── SKILL.md              # Required, <200 lines, with YAML frontmatter
└── references/           # Loaded by agent as needed
    ├── main-workflow.md
    ├── branch-workflow.md
    ├── error-recovery.md
    └── feedback-guide.md
```
