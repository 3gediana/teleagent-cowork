import { tool } from "@opencode-ai/plugin"
import { z } from "zod"

export default tool({
  name: "reject_pr",
  description: "Reject a PR with reason",
  parameters: z.object({
    pr_id: z.string().describe("PR ID"),
    reason: z.string().describe("Why you reject this"),
  }),
  async execute({ pr_id, reason }) {
    return { pr_id, reason, rejected: true }
  },
})
