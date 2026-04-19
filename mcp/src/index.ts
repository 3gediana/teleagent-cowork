import { McpServer } from '@modelcontextprotocol/sdk/server/mcp.js'
import { StdioServerTransport } from '@modelcontextprotocol/sdk/server/stdio.js'
import { z } from 'zod'
import { ApiClient } from './api-client.js'
import { Poller } from './poller.js'

const PLATFORM_URL = process.env.A3C_PLATFORM_URL || 'http://localhost:3303'
const ACCESS_KEY = process.env.A3C_ACCESS_KEY || ''
const PROJECT = process.env.A3C_PROJECT || ''

async function main() {
  const api = new ApiClient(PLATFORM_URL, ACCESS_KEY, PROJECT)
  const poller = new Poller(api)

  const server = new McpServer({
    name: 'a3c',
    version: '0.2.0',
  })

  server.tool('a3c_platform', 'Connect to A3C platform - login or logout', {
    action: z.enum(['login', 'logout']).describe('Action to perform'),
    project: z.string().optional().describe('Project ID to connect to (for login)'),
  }, async ({ action, project }) => {
    switch (action) {
      case 'login': {
        const data = await api.login(ACCESS_KEY, project || PROJECT)
        await poller.start()
        return { content: [{ type: 'text', text: JSON.stringify(data, null, 2) }] }
      }
      case 'logout': {
        await poller.stop()
        const data = await api.logout()
        return { content: [{ type: 'text', text: JSON.stringify(data, null, 2) }] }
      }
    }
  })

  server.tool('task', 'Claim or complete A3C tasks', {
    action: z.enum(['claim', 'complete']).describe('Action to perform'),
    task_id: z.string().describe('Task ID (required)'),
  }, async ({ action, task_id }) => {
    switch (action) {
      case 'claim': {
        const data = await api.claimTask(task_id)
        return { content: [{ type: 'text', text: JSON.stringify(data, null, 2) }] }
      }
      case 'complete': {
        const data = await api.completeTask(task_id)
        return { content: [{ type: 'text', text: JSON.stringify(data, null, 2) }] }
      }
    }
  })

  server.tool('filelock', 'Acquire or release file locks', {
    action: z.enum(['acquire', 'release']).describe('Action to perform'),
    task_id: z.string().optional().describe('Task ID (required for acquire)'),
    files: z.array(z.string()).optional().describe('Files to lock (for acquire) or release (optional, releases all if omitted)'),
    reason: z.string().optional().describe('Reason for locking (required for acquire)'),
  }, async ({ action, task_id, files, reason }) => {
    switch (action) {
      case 'acquire': {
        if (!task_id || !files) return { content: [{ type: 'text', text: 'Error: task_id and files required for acquire' }] }
        const data = await api.acquireLock(task_id, files, reason || '')
        return { content: [{ type: 'text', text: JSON.stringify(data, null, 2) }] }
      }
      case 'release': {
        const data = await api.releaseLock(files)
        return { content: [{ type: 'text', text: JSON.stringify(data, null, 2) }] }
      }
    }
  })

  server.tool('change_submit', 'Submit code changes for review', {
    task_id: z.string().describe('Task ID associated with the change'),
    description: z.string().optional().describe('Description of the change'),
    version: z.string().describe('Current version (read from .a3c_version)'),
    writes: z.array(z.union([z.string(), z.object({ path: z.string(), content: z.string() })])).describe('Files to write'),
    deletes: z.array(z.string()).optional().describe('Files to delete'),
  }, async ({ task_id, description, version, writes, deletes }) => {
    const data = await api.submitChange({
      task_id,
      description: description || '',
      version,
      writes,
      deletes: deletes || [],
    })
    return { content: [{ type: 'text', text: JSON.stringify(data, null, 2) }] }
  })

  server.tool('file_sync', 'Sync platform files to local staging area', {
    version: z.string().describe('Current local version'),
  }, async ({ version }) => {
    const data = await api.syncFiles(version)
    return { content: [{ type: 'text', text: JSON.stringify(data, null, 2) }] }
  })

  server.tool('status_sync', 'Get current project status (tasks, locks, directions)', {}, async () => {
    const data = await api.syncStatus()
    return { content: [{ type: 'text', text: JSON.stringify(data, null, 2) }] }
  })

  server.tool('project_info', 'Query project information via consulting agent', {
    query: z.string().describe('Question about the project'),
  }, async ({ query }) => {
    const data = await api.projectInfo(query)
    return { content: [{ type: 'text', text: JSON.stringify(data, null, 2) }] }
  })

  poller.setBroadcastHandler((messages) => {
    console.error('[Broadcast] Received %d messages', messages.length)
    for (const msg of messages) {
      console.error('[Broadcast] %s: %s', msg.header?.type || msg.type, JSON.stringify(msg.payload || msg))
    }
  })

  const transport = new StdioServerTransport()
  await server.connect(transport)
  console.error('[A3C MCP Server] v0.2.0 Started, connecting to %s', PLATFORM_URL)
}

main().catch(console.error)
