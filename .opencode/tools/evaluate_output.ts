import { tool } from "@opencode-ai/plugin"

export default tool({
  description: "Output the PR technical evaluation result.",
  args: {
    result: tool.schema.string().describe("Evaluation result: approved, needs_work, conflicts, or high_risk"),
    merge_cost_rating: tool.schema.string().describe("Merge cost rating: low, medium, or high"),
    reason: tool.schema.string().optional().describe("Reason or explanation for the evaluation result"),
    conflict_files: tool.schema.array(tool.schema.string()).optional().describe("List of conflicting files, if any"),
    quality_patterns: tool.schema.string().optional().describe("Code quality patterns observed (good or bad)"),
    common_mistakes: tool.schema.string().optional().describe("Repeated mistakes seen across PRs in this project"),
  },
  async execute(args, context) {
    return JSON.stringify({ success: true, data: { tool: "evaluate_output", result: args.result, merge_cost_rating: args.merge_cost_rating, status: "captured" } })
  },
})
