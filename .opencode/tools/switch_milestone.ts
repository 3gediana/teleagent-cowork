import { tool } from "@opencode-ai/plugin"
import { z } from "zod"

export default tool({
  name: "switch_milestone",
  description: "Switch to a different milestone",
  parameters: z.object({
    milestone_id: z.string().describe("Target milestone ID"),
    reason: z.string().describe("Why switching"),
  }),
  async execute({ milestone_id, reason }) {
    return { milestone_id, reason, switched: true }
  },
})
