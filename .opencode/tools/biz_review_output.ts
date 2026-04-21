import { tool } from "@opencode-ai/plugin"

export default tool({
  description: "Output the business evaluation result of a Pull Request (used by Maintain Agent during PR biz review).",
  args: {
    result: tool.schema.string().describe("Business evaluation result: approved, rejected, or needs_changes"),
    biz_review: tool.schema.string().optional().describe("Detailed business review comments in markdown format"),
    version_suggestion: tool.schema.string().optional().describe("Suggested version number after merge, e.g. v1.6"),
    milestone_completion: tool.schema.string().optional().describe("Whether this PR completes the current milestone: none, partial, or complete"),
    direction_alignment: tool.schema.string().optional().describe("Whether the PR aligns with project direction: aligned, partial, or misaligned"),
    alignment_rationale: tool.schema.string().optional().describe("Why this PR aligns or misaligns with the project direction"),
  },
  async execute(args, context) {
    return JSON.stringify({ success: true, data: { tool: "biz_review_output", result: args.result, version_suggestion: args.version_suggestion, status: "captured" } })
  },
})
