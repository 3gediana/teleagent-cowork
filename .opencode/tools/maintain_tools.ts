import { tool } from "@opencode-ai/plugin"

const A3C_BASE = "http://127.0.0.1:3303"

export const update_milestone = tool({
  description: "Update the milestone block content for the current project.",
  args: {
    content: tool.schema.string().describe("New milestone block content in the template format"),
  },
  async execute(args, context) {
    const resp = await fetch(`${A3C_BASE}/api/v1/internal/agent/session/${context.sessionID}/output`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ tool: "update_milestone", content: args.content }),
    })
    const data = await resp.json()
    return JSON.stringify(data)
  },
})

export const propose_direction = tool({
  description: "Propose a new direction block for the project. Must be confirmed by a human before writing.",
  args: {
    content: tool.schema.string().describe("Proposed direction block content"),
  },
  async execute(args, context) {
    const resp = await fetch(`${A3C_BASE}/api/v1/internal/agent/session/${context.sessionID}/output`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ tool: "propose_direction", content: args.content }),
    })
    const data = await resp.json()
    return JSON.stringify(data)
  },
})

export const delete_task = tool({
  description: "Delete a task from the current project milestone.",
  args: {
    task_id: tool.schema.string().describe("The task ID to delete"),
  },
  async execute(args, context) {
    const resp = await fetch(`${A3C_BASE}/api/v1/task/${args.task_id}`, {
      method: "DELETE",
      headers: { "Authorization": "Bearer " + (process.env.A3C_ACCESS_KEY || "") },
    })
    const data = await resp.json()
    return JSON.stringify(data)
  },
})