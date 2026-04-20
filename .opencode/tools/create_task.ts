import { tool } from "@opencode-ai/plugin"

export default tool({
  description: "Create a new task in the current project milestone. Use this to add actionable work items.",
  args: {
    name: tool.schema.string().describe("Task name"),
    description: tool.schema.string().optional().describe("Task description"),
    priority: tool.schema.string().optional().describe("Priority: high, medium, or low (default: medium)"),
  },
  async execute(args, context) {
    return JSON.stringify({ success: true, data: { name: args.name, description: args.description, priority: args.priority || "medium", status: "created" } })
  },
})
