import { tool } from "@opencode-ai/plugin"

export default tool({
  description: "Output fix verification result after reviewing flagged issues.",
  args: {
    change_id: tool.schema.string().describe("The change ID being fixed"),
    action: tool.schema.string().describe("Action: fix, delegate, or reject"),
    fixed: tool.schema.boolean().optional().describe("Whether the issue was fixed"),
    delegate_to: tool.schema.string().optional().describe("Delegate target"),
    reject_reason: tool.schema.string().optional().describe("Reason for rejection"),
  },
  async execute(args, context) {
    return JSON.stringify({ success: true, data: { tool: "fix_output", change_id: args.change_id, action: args.action, status: "captured" } })
  },
})
