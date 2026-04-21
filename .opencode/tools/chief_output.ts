import { tool } from "@opencode-ai/plugin"
import { z } from "zod"

export default tool({
  name: "chief_output",
  description: "Output session result for Chief Agent",
  parameters: z.object({
    result: z.string().describe("Result: reported / executed / approved / rejected / needs_clarification"),
    summary: z.string().describe("What you did or reported"),
  }),
  async execute({ result, summary }) {
    return { result, summary }
  },
})
