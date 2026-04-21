import { tool } from "@opencode-ai/plugin"
import { z } from "zod"

export default tool({
  name: "create_policy",
  description: "Create a decision policy for the Chief Agent. Use this when human tells you rules to remember.",
  parameters: z.object({
    name: z.string().describe("Human-readable policy name"),
    match_condition: z.string().describe("JSON match condition, e.g. {\"scope\":\"pr_review\",\"file_count_gt\":5}"),
    actions: z.string().describe("JSON actions, e.g. {\"require_human\":true,\"warn\":\"大改动需人类确认\"}"),
    priority: z.number().optional().describe("Priority (higher = checked first)"),
  }),
  async execute({ name, match_condition, actions, priority }) {
    return { name, match_condition, actions, priority: priority ?? 0, created: true }
  },
})
