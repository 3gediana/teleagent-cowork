import { McpServer } from '@modelcontextprotocol/sdk/server/mcp.js'
import { StdioServerTransport } from '@modelcontextprotocol/sdk/server/stdio.js'
import { z } from 'zod'
import { ApiClient } from './api-client.js'
import { Poller } from './poller.js'
import { OpenCodeClient } from './opencode-client.js'
import { loadConfig, saveConfig } from './config.js'

const PLATFORM_URL = process.env.A3C_PLATFORM_URL || 'http://localhost:3003'
const ACCESS_KEY = process.env.A3C_ACCESS_KEY || ''
const PROJECT = process.env.A3C_PROJECT || ''
const OPENCODE_SERVE_URL = process.env.OPENCODE_SERVE_URL || 'http://127.0.0.1:4096'

async function main() {
  const savedConfig = loadConfig()
  const initialKey = ACCESS_KEY || savedConfig.access_key || ''
  const initialProject = PROJECT || savedConfig.project_id || ''

  const api = new ApiClient(PLATFORM_URL, initialKey, initialProject)
  const poller = new Poller(api)
  const startupTime = Date.now()
  const oc = new OpenCodeClient(OPENCODE_SERVE_URL, startupTime)

  const server = new McpServer({
    name: 'a3c',
    version: '0.2.0',
  })

  server.tool('a3c_platform', 'Connect to A3C platform - login or logout', {
    action: z.enum(['login', 'logout']).describe('Action to perform'),
    access_key: z.string().optional().describe('Access key for login (auto-uses cached key if not provided)'),
  }, async ({ action, access_key }) => {
    switch (action) {
      case 'login': {
        const key = access_key || savedConfig.access_key
        if (!key) return { content: [{ type: 'text', text: 'Error: No access key provided. Please register first or provide a key.' }] }
        try {
          const data = await api.login(key, savedConfig.project_id || '')
          api.setAccessKey(key)
          saveConfig({ access_key: key, project_id: savedConfig.project_id })
          // Don't start poller on login - wait for select_project
          return { content: [{ type: 'text', text: JSON.stringify(data, null, 2) }] }
        } catch (e: any) {
          const code = e?.response?.data?.error?.code || ''
          if (code === 'AUTH_ALREADY_ONLINE') {
            return { content: [{ type: 'text', text: 'Agent is already online. Please logout first using: ⚙ a3c_platform [action=logout]' }] }
          }
          return { content: [{ type: 'text', text: `Login failed: ${e?.response?.data?.error?.message || e.message}` }] }
        }
      }
      case 'logout': {
        const key = api.currentAccessKey || savedConfig.access_key
        if (!key) return { content: [{ type: 'text', text: 'Error: No access key available. Please login first.' }] }
        await poller.stop()
        try {
          const data = await api.logout(key)
          return { content: [{ type: 'text', text: JSON.stringify(data, null, 2) }] }
        } catch (e: any) {
          return { content: [{ type: 'text', text: `Logout failed: ${e?.response?.data?.error?.message || e.message}` }] }
        }
      }
    }
  })

  server.tool('select_project', 'Select a project to work on after login', {
    project_id: z.string().describe('Project ID to connect to'),
  }, async ({ project_id }) => {
    api.setProject(project_id)
    saveConfig({ project_id })
    const data = await api.selectProject(project_id)
    // Start poller after selecting project
    await poller.start()
    return { content: [{ type: 'text', text: JSON.stringify(data, null, 2) }] }
  })

  server.tool('task', 'Claim A3C tasks (completion is automatic when change is approved)', {
    action: z.enum(['claim']).describe('Action to perform (tasks are auto-completed on approval)'),
    task_id: z.string().describe('Task ID (required)'),
  }, async ({ action, task_id }) => {
    switch (action) {
      case 'claim': {
        const data = await api.claimTask(task_id)
        return { content: [{ type: 'text', text: JSON.stringify(data, null, 2) }] }
      }
    }
  })

  server.tool('filelock', 'Acquire or release file locks', {
    action: z.enum(['acquire', 'release', 'check']).describe('Action to perform'),
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
      case 'check': {
        if (!files) return { content: [{ type: 'text', text: 'Error: files required for check' }] }
        const data = await api.checkLocks(files)
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

  poller.setBroadcastHandler(async (messages) => {
    console.error('[Broadcast] Received %d messages', messages.length)
    const sessionId = await oc.getLatestSession()
    if (!sessionId) {
      console.error('[Broadcast] No active OpenCode session found')
      return
    }
    for (const msg of messages) {
      const eventType = msg.header?.type || msg.header?.Type || 'unknown'
      const payload = msg.payload || msg
      const text = `📡 [A3C BROADCAST] Event: ${eventType}\n\n${JSON.stringify(payload, null, 2)}`
      oc.injectMessage(sessionId, text)
      console.error('[Broadcast] Injected to session %s: %s', sessionId, eventType)
    }
  })

  const transport = new StdioServerTransport()
  await server.connect(transport)
  console.error('[A3C MCP Server] v0.2.0 Started, connecting to %s', PLATFORM_URL)
}

main().catch(console.error)
