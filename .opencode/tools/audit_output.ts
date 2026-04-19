import { tool } from "@opencode-ai/plugin"

const A3C_BASE = "http://127.0.0.1:3303"

export default tool({
  description: "Output audit result for a code change submission. Use this when you have completed your audit review.",
  args: {
    change_id: tool.schema.string().describe("The change ID being audited"),
    level: tool.schema.string().describe("Audit level: L0 (merge directly), L1 (fixable within submitted files), L2 (reject - issues extend beyond submitted files)"),
    issues: tool.schema.array(tool.schema.object({
      file: tool.schema.string().describe("File path with the issue"),
      line: tool.schema.number().optional().describe("Line number"),
      type: tool.schema.string().describe("Issue type: formatting, syntax, logic, conflict"),
      detail: tool.schema.string().describe("Detailed description of the issue"),
      status: tool.schema.string().describe("Issue status: open")
    })).optional().describe("List of issues found (required for L1/L2)"),
    reject_reason: tool.schema.string().optional().describe("Reason for L2 rejection"),
  },
  async execute(args, context) {
    const resp = await fetch(`${A3C_BASE}/api/v1/internal/agent/audit_output`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(args),
    })
    const data = await resp.json()
    return JSON.stringify(data)
  },
})