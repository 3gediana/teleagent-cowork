import { tool } from "@opencode-ai/plugin"

export default tool({
  description: "Output final audit decision after re-reviewing a change.",
  args: {
    change_id: tool.schema.string().describe("The change ID being reviewed"),
    result: tool.schema.string().describe("Result: merge or reject"),
    reject_reason: tool.schema.string().optional().describe("Reason for rejection"),
  },
  async execute(args, context) {
    return JSON.stringify({ success: true, data: { tool: "audit2_output", change_id: args.change_id, result: args.result, status: "captured" } })
  },
})
