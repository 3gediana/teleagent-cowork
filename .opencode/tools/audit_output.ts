import { tool } from "@opencode-ai/plugin"

export default tool({
  description: "Output audit result for a code change submission.",
  args: {
    change_id: tool.schema.string().describe("The change ID being audited"),
    level: tool.schema.string().describe("Audit level: L0, L1, or L2"),
    issues: tool.schema.array(tool.schema.object({})).optional().describe("List of audit issues"),
    reject_reason: tool.schema.string().optional().describe("Reason for rejection"),
  },
  async execute(args, context) {
    return JSON.stringify({ success: true, data: { tool: "audit_output", change_id: args.change_id, level: args.level, status: "captured" } })
  },
})
