import { tool } from "@opencode-ai/plugin"

export default tool({
  description: "Output the PR merge execution result.",
  args: {
    result: tool.schema.string().describe("Merge result: success or failed"),
    reason: tool.schema.string().optional().describe("Reason for failure or details of resolution"),
  },
  async execute(args, context) {
    return JSON.stringify({ success: true, data: { tool: "merge_output", result: args.result, status: "captured" } })
  },
})
