import { tool } from "@opencode-ai/plugin"

const A3C_BASE = "http://127.0.0.1:3303"

export default tool({
  description: "Create a new task in the current project milestone. Use this to add actionable work items.",
  args: {
    name: tool.schema.string().describe("Task name"),
    description: tool.schema.string().optional().describe("Task description"),
    priority: tool.schema.string().optional().describe("Priority: high, medium, or low (default: medium)"),
  },
  async execute(args, context) {
    const resp = await fetch(`${A3C_BASE}/api/v1/task/create`, {
      method: "POST",
      headers: { "Content-Type": "application/json", "Authorization": "Bearer " + (process.env.A3C_ACCESS_KEY || "") },
      body: JSON.stringify(args),
    })
    const data = await resp.json()
    return JSON.stringify(data)
  },
})