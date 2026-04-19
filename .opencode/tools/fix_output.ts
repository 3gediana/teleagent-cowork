import { tool } from "@opencode-ai/plugin"

const A3C_BASE = "http://127.0.0.1:3303"

export default tool({
  description: "Output fix verification result after reviewing flagged issues from Audit Agent 1.",
  args: {
    change_id: tool.schema.string().describe("The change ID being processed"),
    action: tool.schema.string().describe("Action: fix (issues confirmed and fixed), delegate (false positive, delegate to audit_agent_2), or reject"),
    fixed: tool.schema.boolean().optional().describe("Whether issues were fixed (for action=fix)"),
    delegate_to: tool.schema.string().optional().describe("Delegate target: audit_agent_2"),
    reject_reason: tool.schema.string().optional().describe("Reason for rejection"),
  },
  async execute(args, context) {
    const resp = await fetch(`${A3C_BASE}/api/v1/internal/agent/fix_output`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(args),
    })
    const data = await resp.json()
    return JSON.stringify(data)
  },
})