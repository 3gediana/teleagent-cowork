import { tool } from "@opencode-ai/plugin"

const A3C_BASE = "http://127.0.0.1:3303"

export default tool({
  description: "Output project structure assessment result. Must follow the ASSESS_DOC.md template format exactly.",
  args: {
    content: tool.schema.string().describe("The full ASSESS_DOC.md content following the required template format"),
  },
  async execute(args, context) {
    const resp = await fetch(`${A3C_BASE}/api/v1/internal/agent/session/${context.sessionID}/output`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ tool: "assess_output", content: args.content }),
    })
    const data = await resp.json()
    return JSON.stringify(data)
  },
})