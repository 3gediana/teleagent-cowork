import { tool } from "@opencode-ai/plugin"
import { z } from "zod"

export default tool({
  description: "Output analysis result: distilled experiences, skill candidates, and policy suggestions",
  args: {
    distilled_experience_ids: z.array(z.string()).describe("Experience IDs that have been distilled"),
    skill_candidates: z.array(z.object({
      name: z.string(),
      type: z.enum(["process", "prompt", "routing", "guard"]),
      applicable_tags: z.array(z.string()),
      precondition: z.string(),
      action: z.string(),
      prohibition: z.string(),
      evidence: z.string()
    })).optional().describe("New skills extracted from experience patterns"),
    policy_suggestions: z.array(z.object({
      name: z.string(),
      match_condition: z.record(z.any()),
      actions: z.record(z.any()),
      priority: z.number()
    })).optional().describe("Policy suggestions to prevent recurring failures"),
    tag_suggestions: z.array(z.object({
      task_id: z.string(),
      suggested_tags: z.array(z.string())
    })).optional().describe("Tag suggestions for tasks"),
    model_suggestions: z.array(z.object({
      role: z.string(),
      recommended_model: z.string(),
      reason: z.string()
    })).optional().describe("Model recommendations per role"),
  },
  async execute(args, context) {
    return JSON.stringify({ success: true, data: { tool: "analyze_output", distilled_count: args.distilled_experience_ids.length, status: "captured" } })
  },
})
