import { tool } from "@opencode-ai/plugin"

export default tool({
  description: "Output project structure assessment result. Must follow the ASSESS_DOC.md template format exactly.",
  args: {
    content: tool.schema.string().describe("The full ASSESS_DOC.md content following the required template format"),
  },
  async execute(args, context) {
    return JSON.stringify({ success: true, data: { tool: "assess_output", status: "captured" } })
  },
})
