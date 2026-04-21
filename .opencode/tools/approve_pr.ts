import { tool } from "@opencode-ai/plugin"
import { z } from "zod"

export default tool({
  name: "approve_pr",
  description: "Approve a PR (review or merge step). Use this in AutoMode to replace human approval.",
  parameters: z.object({
    pr_id: z.string().describe("PR ID"),
    action: z.enum(["approve_review", "approve_merge"]).describe("Which approval step"),
    reason: z.string().describe("Why you approve this"),
  }),
  async execute({ pr_id, action, reason }) {
    return { pr_id, action, reason, approved: true }
  },
})
