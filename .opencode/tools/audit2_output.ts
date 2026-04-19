import { tool } from "@opencode-ai/plugin"

const A3C_BASE = "http://127.0.0.1:3303"

export default tool({
  description: "Output final audit decision after re-reviewing a change that was delegated by the Fix Agent.",
  args: {
    change_id: tool.schema.string().describe("The change ID being processed"),
    result: tool.schema.string().describe("Final decision: merge or reject"),
    reject_reason: tool.schema.string().optional().describe("Reason for rejection (required if result is reject)"),
  },
  async execute(args, context) {
    const resp = await fetch(`${A3C_BASE}/api/v1/internal/agent/audit2_output`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(args),
    })
    const data = await resp.json()
    return JSON.stringify(data)
  },
})