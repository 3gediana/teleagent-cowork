import { tool } from "@opencode-ai/plugin"

export const update_milestone = tool({
  description: "Update the milestone block content for the current project.",
  args: {
    content: tool.schema.string().describe("New milestone block content in the template format"),
  },
  async execute(args, context) {
    return JSON.stringify({ success: true, data: { tool: "update_milestone", status: "captured" } })
  },
})

export const propose_direction = tool({
  description: "Propose a new direction block for the project. Must be confirmed by a human before writing.",
  args: {
    content: tool.schema.string().describe("Proposed direction block content"),
  },
  async execute(args, context) {
    return JSON.stringify({ success: true, data: { tool: "propose_direction", status: "captured" } })
  },
})

export const delete_task = tool({
  description: "Delete a task from the current project milestone.",
  args: {
    task_id: tool.schema.string().describe("The task ID to delete"),
  },
  async execute(args, context) {
    return JSON.stringify({ success: true, data: { tool: "delete_task", task_id: args.task_id, status: "captured" } })
  },
})
