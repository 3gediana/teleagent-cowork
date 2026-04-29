#!/usr/bin/env node
import { McpServer } from '@modelcontextprotocol/sdk/server/mcp.js'
import { StdioServerTransport } from '@modelcontextprotocol/sdk/server/stdio.js'
import { z } from 'zod'
import { ApiClient } from './api-client.js'
import { Poller } from './poller.js'
import { OpenCodeClient } from './opencode-client.js'
import { loadConfig, saveConfig, workdirRoot } from './config.js'
import * as fs from 'fs'
import * as path from 'path'
import { fileURLToPath } from 'url'

const __filename = fileURLToPath(import.meta.url)
const __dirname = path.dirname(__filename)

const PLATFORM_URL = process.env.A3C_PLATFORM_URL || 'http://localhost:3003'
const ACCESS_KEY = process.env.A3C_ACCESS_KEY || ''
const PROJECT = process.env.A3C_PROJECT_ID || process.env.A3C_PROJECT || ''
const OPENCODE_SERVE_URL = process.env.OPENCODE_SERVE_URL || 'http://127.0.0.1:4096'

/**
 * formatClaimResponse turns the backend's task.claim response into a
 * markdown-like text block the upstream coding agent can actually use.
 *
 * The backend now attaches `hints` — recipes, patterns and anti-patterns
 * selected by the refinery + semantic retrieval — to every successful
 * claim. Dumping the raw JSON would work (the agent can parse anything),
 * but a structured layout is easier for both the agent and the operator
 * inspecting a transcript. We also preserve the injected_ids so the
 * change.submit path can echo them back for feedback accounting.
 *
 * If the backend is old (no hints field) or the hints were unavailable
 * for this task (sidecar down, cold cache), the function gracefully
 * falls back to plain task metadata — an agent on the old protocol sees
 * the same thing it used to.
 */
function formatClaimResponse(data: any): string {
  if (!data?.success) return JSON.stringify(data, null, 2)
  const d = data.data || {}
  const lines: string[] = []
  lines.push(`# Task claimed — ${d.name}`)
  lines.push('')
  lines.push(`- id: ${d.id}`)
  if (d.priority) lines.push(`- priority: ${d.priority}`)
  if (d.milestone_id) lines.push(`- milestone: ${d.milestone_id}`)
  if (d.description) {
    lines.push('')
    lines.push('## Description')
    lines.push(d.description)
  }

  const hints = d.hints
  if (!hints || !Array.isArray(hints.injected_ids) || hints.injected_ids.length === 0) {
    lines.push('')
    lines.push('_(no experience hints available for this task yet)_')
    return lines.join('\n')
  }

  const fmtItem = (h: any): string =>
    `- **${h.name}** (score=${(h.score ?? 0).toFixed(2)})\n  ${h.summary}\n  _reason: ${h.reason}_`

  lines.push('')
  lines.push('## Experience hints (from past work)')
  lines.push(`selected ${hints.meta?.selected ?? 0} of ${hints.meta?.candidate_pool ?? 0} candidates`)

  if (Array.isArray(hints.recipes) && hints.recipes.length > 0) {
    lines.push('')
    lines.push('### 🛠 Recipes — try these steps in this order')
    hints.recipes.forEach((h: any) => lines.push(fmtItem(h)))
  }
  if (Array.isArray(hints.patterns) && hints.patterns.length > 0) {
    lines.push('')
    lines.push('### ✓ Patterns — what usually works in this situation')
    hints.patterns.forEach((h: any) => lines.push(fmtItem(h)))
  }
  if (Array.isArray(hints.anti_patterns) && hints.anti_patterns.length > 0) {
    lines.push('')
    lines.push('### ⚠ Anti-patterns — avoid these, known failure modes')
    hints.anti_patterns.forEach((h: any) => lines.push(fmtItem(h)))
  }

  lines.push('')
  lines.push(`<!-- a3c_injected_ids: ${hints.injected_ids.join(',')} -->`)
  return lines.join('\n')
}

async function main() {
  const savedConfig = loadConfig()
  const initialKey = ACCESS_KEY || savedConfig.access_key || ''
  const initialProject = PROJECT || savedConfig.project_id || ''

  const api = new ApiClient(PLATFORM_URL, initialKey, initialProject)
  const poller = new Poller(api)
  const startupTime = Date.now()
  const oc = new OpenCodeClient(OPENCODE_SERVE_URL, startupTime)

  // Start the persistent SSE tracker right away so we lock onto the
  // attach client's session as soon as it makes any noise (input,
  // submit, switch). Idempotent.
  oc.startSessionTracker()

  // Sidecar self-suicide: if the attach client has produced no
  // sessionID-bearing event for 30 min, the TUI is almost certainly
  // closed. Exit so:
  //   - poller.heartbeat() stops, letting the platform's 7-min sweep
  //     mark this agent offline and release its tasks/locks/branch
  //   - the OpenCode server stops piping broadcasts at an absent UI
  //     (the headless-LLM-burn bug we verified empirically)
  // Override with A3C_IDLE_SUICIDE_MS=<ms> if a workflow legitimately
  // needs longer silent periods. 0 disables.
  const idleSuicideMs = parseInt(process.env.A3C_IDLE_SUICIDE_MS || '', 10)
  const effectiveIdleMs = Number.isFinite(idleSuicideMs)
    ? idleSuicideMs
    : 30 * 60 * 1000
  if (effectiveIdleMs > 0) {
    oc.startIdleSuicide(effectiveIdleMs)
  }

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
    // Lock the opencode session ID for this platform connection
    // This ensures broadcasts inject into the correct session, not some random other session
    const sessionId = await oc.lockSession()
    if (!sessionId) {
      console.error('[A3C] Warning: Could not lock opencode session, broadcasts may not inject correctly')
    }
    // Start poller after selecting project
    await poller.start()
    return { content: [{ type: 'text', text: JSON.stringify(data, null, 2) }] }
  })

  server.tool(
    'task',
    'A3C task management: list pending tasks, claim one to work on, or release it if you realize you cannot finish it. Completion is automatic when your change is approved.',
    {
      action: z.enum(['list', 'claim', 'release']).describe('list=see available tasks, claim=take a task, release=abandon a claimed task'),
      task_id: z.string().optional().describe('Task ID (required for claim and release)'),
      reason: z.string().optional().describe('Why you are releasing (optional, for release)'),
    },
    async ({ action, task_id, reason }) => {
      switch (action) {
        case 'list': {
          const projectId = api.projectId
          if (!projectId) return { content: [{ type: 'text', text: 'Error: No project selected. Call select_project first.' }] }
          const data = await api.listTasks(projectId)
          return { content: [{ type: 'text', text: JSON.stringify(data, null, 2) }] }
        }
        case 'claim': {
          if (!task_id) return { content: [{ type: 'text', text: 'Error: task_id required for claim' }] }
          const data = await api.claimTask(task_id)
          return { content: [{ type: 'text', text: formatClaimResponse(data) }] }
        }
        case 'release': {
          if (!task_id) return { content: [{ type: 'text', text: 'Error: task_id required for release' }] }
          const data = await api.releaseTask(task_id, reason)
          return { content: [{ type: 'text', text: JSON.stringify(data, null, 2) }] }
        }
      }
    },
  )

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
    injected_artifact_ids: z.array(z.string()).optional().describe(
      'KnowledgeArtifact IDs echoed back from the task.claim hints bundle. ' +
      'Pass them here so the server can bump success/failure counters on ' +
      'the exact artifacts that guided this change once the audit verdict ' +
      'lands. Safe to omit if no hints were consulted.'
    ),
    injected_refs: z.array(z.object({
      id: z.string(),
      reason: z.string().optional(),
      score: z.number().optional(),
    })).optional().describe(
      'Richer variant of injected_artifact_ids, copied verbatim from the ' +
      '`injected_refs` field in the task.claim response. Preserves per-' +
      'artifact selector reason + score so the server can compute per-' +
      'reason success rates over time. Preferred over injected_artifact_ids.'
    ),
  }, async ({ task_id, description, version, writes, deletes, injected_artifact_ids, injected_refs }) => {
    const data = await api.submitChange({
      task_id,
      description: description || '',
      version,
      writes,
      deletes: deletes || [],
      injected_artifact_ids: injected_artifact_ids,
      injected_refs: injected_refs,
    })
    return { content: [{ type: 'text', text: JSON.stringify(data, null, 2) }] }
  })

  server.tool('file_sync', 'Sync platform files to local staging area', {
    version: z.string().optional().default('').describe('Current local version'),
  }, async ({ version }) => {
    const data = await api.syncFiles(version)
    
    if (!data.success) {
      return { content: [{ type: 'text', text: JSON.stringify(data, null, 2) }] }
    }

    const projectId = data.data?.project_id || api.projectId
    if (!projectId) {
      return { content: [{ type: 'text', text: 'Error: No project selected' }] }
    }

    // Staging root selection order:
    //   1. A3C_STAGING_DIR env var (absolute path; useful in tests / CI)
    //   2. workdirRoot() - A3C_HOME or process.cwd(); see config.ts
    //
    // The legacy __dirname/.. fallback used to leak the MCP install
    // directory across workdirs whenever the launcher forgot to set
    // cwd; removed so failures fail loud instead of silently scribbling
    // into the package.
    const stagingRoot = process.env.A3C_STAGING_DIR
      ? path.resolve(process.env.A3C_STAGING_DIR)
      : workdirRoot()
    const clientRoot = stagingRoot
    const stagingDir = path.resolve(path.join(clientRoot, '.a3c_staging', projectId, 'full'))

    fs.mkdirSync(stagingDir, { recursive: true })

    const files: Array<{ path: string; content: string; locked: boolean; status?: string }> = Array.isArray(data.data?.files)
      ? data.data.files
      : []
    const deletedPaths: string[] = Array.isArray(data.data?.deleted) ? data.data.deleted : []
    const incremental: boolean = !!data.data?.incremental

    let writtenCount = 0
    const writtenPaths: string[] = []
    const lockedPaths: string[] = []

    // Apply writes (added + modified)
    for (const file of files) {
      const filePath = path.join(stagingDir, file.path)
      const dir = path.dirname(filePath)
      fs.mkdirSync(dir, { recursive: true })
      fs.writeFileSync(filePath, file.content, 'utf-8')
      writtenPaths.push(file.path)
      writtenCount++
      if (file.locked) {
        lockedPaths.push(file.path)
      }
    }

    // Apply deletions: remove stale files from the staging snapshot so the
    // agent never reads content that no longer exists on the platform.
    const deletedCount: string[] = []
    for (const rel of deletedPaths) {
      const filePath = path.join(stagingDir, rel)
      try {
        if (fs.existsSync(filePath)) {
          fs.unlinkSync(filePath)
          deletedCount.push(rel)
        }
      } catch (e) {
        console.error('[file_sync] Failed to delete %s: %s', filePath, (e as any)?.message)
      }
    }

    const versionFile = path.join(clientRoot, '.a3c_version')
    fs.writeFileSync(versionFile, data.data?.version || 'v1.0', 'utf-8')

    const result = {
      success: true,
      data: {
        version: data.data?.version,
        from_version: data.data?.from_version,
        incremental,
        staging_dir: stagingDir,
        files_written: writtenCount,
        files_deleted: deletedCount.length,
        written_files: writtenPaths,
        deleted_files: deletedCount,
        locked_files: lockedPaths,
        message: incremental
          ? `Incremental sync: ${writtenCount} changed, ${deletedCount.length} removed from ${stagingDir}. Version ${data.data?.from_version} -> ${data.data?.version}.`
          : `Full sync: ${writtenCount} files written to ${stagingDir}. Version saved to .a3c_version.`,
      }
    }

    return { content: [{ type: 'text', text: JSON.stringify(result, null, 2) }] }
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

  // ===== Branch Tools =====

  server.tool('select_branch', 'Enter a feature branch to work on (required before using branch tools)', {
    branch_id: z.string().describe('Branch ID to enter (from select_project response)'),
  }, async ({ branch_id }) => {
    try {
      const data = await api.enterBranch(branch_id)
      return { content: [{ type: 'text', text: JSON.stringify(data, null, 2) }] }
    } catch (e: any) {
      const errMsg = e?.response?.data?.error?.message || e.message
      const errCode = e?.response?.data?.error?.code || ''
      return { content: [{ type: 'text', text: `Failed to enter branch [${errCode}]: ${errMsg}` }] }
    }
  })

  server.tool('branch', 'Branch operations: create, leave, list, close, sync_main', {
    action: z.enum(['create', 'leave', 'list', 'close', 'sync_main']).describe('Action to perform'),
    name: z.string().optional().describe('Branch name for create (e.g. "login-module")'),
    branch_id: z.string().optional().describe('Branch ID for close'),
  }, async ({ action, name, branch_id }) => {
    switch (action) {
      case 'create': {
        if (!name) return { content: [{ type: 'text', text: 'Error: name required for create' }] }
        try {
          const data = await api.createBranch(name)
          return { content: [{ type: 'text', text: JSON.stringify(data, null, 2) }] }
        } catch (e: any) {
          return { content: [{ type: 'text', text: `Create branch failed: ${e?.response?.data?.error?.message || e.message}` }] }
        }
      }
      case 'leave': {
        const data = await api.leaveBranch()
        return { content: [{ type: 'text', text: JSON.stringify(data, null, 2) }] }
      }
      case 'list': {
        const data = await api.listBranches()
        return { content: [{ type: 'text', text: JSON.stringify(data, null, 2) }] }
      }
      case 'close': {
        if (!branch_id) return { content: [{ type: 'text', text: 'Error: branch_id required for close' }] }
        const data = await api.closeBranch(branch_id)
        return { content: [{ type: 'text', text: JSON.stringify(data, null, 2) }] }
      }
      case 'sync_main': {
        try {
          const data = await api.syncMain()
          return { content: [{ type: 'text', text: JSON.stringify(data, null, 2) }] }
        } catch (e: any) {
          const conflictFiles = e?.response?.data?.conflict_files
          if (conflictFiles) {
            return { content: [{ type: 'text', text: `Sync conflicts detected in files: ${JSON.stringify(conflictFiles)}. Resolve conflicts manually, then retry.` }] }
          }
          return { content: [{ type: 'text', text: `Sync failed: ${e?.response?.data?.error?.message || e.message}` }] }
        }
      }
    }
  })

  // ===== PR Tools =====

  server.tool('pr_submit', 'Submit a Pull Request from current branch to main (requires self-review)', {
    title: z.string().describe('PR title'),
    description: z.string().optional().describe('PR description'),
    self_review: z.object({
      changed_functions: z.array(z.object({
        file: z.string(),
        function: z.string(),
        change_type: z.string().describe('added/modified/removed/refactored'),
        impact: z.string().describe('What this change does and why it matters'),
      })).describe('Per-function summary of what you changed'),
      overall_impact: z.string().describe('High-level description of the PR impact'),
      merge_confidence: z.enum(['high', 'medium', 'low']).describe('Your confidence that this is safe to merge'),
    }).describe('Structured self-review object (the server accepts it as JSON)'),
  }, async ({ title, description, self_review }) => {
    try {
      const data = await api.submitPR({
        title,
        description: description || '',
        // Pass as object; server accepts either object or stringified JSON.
        self_review: self_review as any,
      })
      return { content: [{ type: 'text', text: JSON.stringify(data, null, 2) }] }
    } catch (e: any) {
      return { content: [{ type: 'text', text: `PR submit failed: ${e?.response?.data?.error?.message || e.message}` }] }
    }
  })

  server.tool('pr_list', 'List all Pull Requests for current project', {}, async () => {
    const data = await api.listPRs()
    return { content: [{ type: 'text', text: JSON.stringify(data, null, 2) }] }
  })

  // ===== Feedback Tool =====

  server.tool('feedback', 'Submit task completion feedback with lessons learned. Call this after completing or failing a task. Your insights will be distilled into reusable skills for future tasks.', {
    task_id: z.string().describe('Task ID'),
    outcome: z.enum(['success', 'partial', 'failed']).describe('Task outcome'),
    approach: z.string().optional().describe('What approach did you take and why'),
    pitfalls: z.string().optional().describe('What went wrong or what was tricky'),
    key_insight: z.string().optional().describe('One key insight for future similar tasks'),
    missing_context: z.string().optional().describe('What info did you need but did not have'),
    would_do_differently: z.string().optional().describe('What would you do differently next time'),
    files_read: z.array(z.string()).optional().describe('Files that were actually useful'),
  }, async ({ task_id, outcome, approach, pitfalls, key_insight, missing_context, would_do_differently, files_read }) => {
    try {
      const data = await api.submitFeedback({
        task_id, outcome, approach, pitfalls, key_insight, missing_context, would_do_differently, files_read,
      })
      return { content: [{ type: 'text', text: `Experience recorded: ${JSON.stringify(data)}` }] }
    } catch (e: any) {
      return { content: [{ type: 'text', text: `Feedback failed: ${e?.response?.data?.error?.message || e.message}` }] }
    }
  })

  // Broadcast retry buffer.
  //
  // Keyed by header.messageID. Lifecycle:
  //   inflight     — fetched from /poll, not yet successfully injected
  //   awaiting_ack — injected into OpenCode, ack queued for the next /poll
  //
  // The platform now keeps directed messages in its Redis queue until
  // the client acks them (handler/sync.go::Poll reads acked_directed_ids
  // and calls service.AckDirectedMessages → LREM). So even if this
  // process crashes between fetch and inject, the message is still on
  // the platform side and will redeliver on the next poll. We dedup
  // re-deliveries by messageID below.
  //
  // MAX_INJECT_ATTEMPTS protects against a permanently broken inject
  // path (e.g. opencode wedged on a session id we can't replace) by
  // eventually giving up and acking the message anyway, so the queue
  // doesn't grow forever. The platform-side TTL (10 min) is the
  // ultimate safety net.
  const MAX_INJECT_ATTEMPTS = 5
  type PendingEntry = {
    msg: any
    attempts: number
    status: 'inflight' | 'awaiting_ack'
  }
  const pending = new Map<string, PendingEntry>()
  let pendingNoIdCounter = 0

  function extractMsgId(msg: any): string {
    const id = msg?.header?.messageID || msg?.header?.MessageID || msg?.header?.message_id
    if (typeof id === 'string' && id.length > 0) return id
    // Older platform builds and some test fixtures don't stamp a
    // messageID. We synthesise a process-local one so the entry is
    // deduplicated within this run, but we cannot ack it server-side
    // (the platform won't find an LREM match). Best-effort: log and
    // inject opportunistically.
    pendingNoIdCounter++
    return `__noid_${pendingNoIdCounter}`
  }

  function isAckable(msgId: string): boolean {
    return !msgId.startsWith('__noid_')
  }

  poller.setAckProvider(() => {
    const acks: string[] = []
    for (const [id, entry] of pending) {
      if (entry.status === 'awaiting_ack' && isAckable(id)) {
        acks.push(id)
      }
    }
    return acks
  })

  poller.setAckConfirmedHandler((acked) => {
    // Server has LREM'd these — drop them from local state. The
    // pending entries with status=inflight are NOT in this list and
    // will be retried on the next broadcast tick.
    for (const id of acked) {
      pending.delete(id)
    }
  })

  poller.setBroadcastHandler(async (messages) => {
    console.error('[Broadcast] Received %d messages (pending=%d)', messages.length, pending.size)

    // 1. Add fresh messages to the pending map. Skip dupes — they
    //    may already be 'awaiting_ack' (server LREM not yet applied)
    //    or 'inflight' (we got them again because last cycle's inject
    //    failed and we couldn't ack them).
    for (const msg of messages) {
      const id = extractMsgId(msg)
      if (!pending.has(id)) {
        pending.set(id, { msg, attempts: 0, status: 'inflight' })
      }
    }

    // 2. Drain pending. Any 'inflight' entry gets one inject attempt.
    //    Ack-confirmed cleanup happens in setAckConfirmedHandler above.
    const sessionId = await oc.getLatestSession()
    if (!sessionId) {
      console.error('[Broadcast] No active OpenCode session; %d msgs queued for retry', pending.size)
      return
    }

    for (const [id, entry] of pending) {
      if (entry.status === 'awaiting_ack') continue

      if (entry.attempts >= MAX_INJECT_ATTEMPTS) {
        const eventType = entry.msg?.header?.type || entry.msg?.header?.Type || 'unknown'
        console.error('[Broadcast] Giving up on %s (id=%s) after %d attempts; will ack to free queue',
          eventType, id, entry.attempts)
        if (isAckable(id)) {
          entry.status = 'awaiting_ack'
        } else {
          pending.delete(id)
        }
        continue
      }

      const eventType = entry.msg?.header?.type || entry.msg?.header?.Type || 'unknown'
      const payload = entry.msg?.payload || entry.msg
      const text = `📡 [A3C BROADCAST] Event: ${eventType}\n\n${JSON.stringify(payload, null, 2)}`
      const ok = await oc.injectMessage(sessionId, text)
      if (ok) {
        if (isAckable(id)) {
          entry.status = 'awaiting_ack'
        } else {
          // No id we can ack server-side; just drop locally.
          pending.delete(id)
        }
        console.error('[Broadcast] Injected to session %s: %s (id=%s)', sessionId, eventType, id)
      } else {
        entry.attempts++
        console.error('[Broadcast] Inject failed for %s (id=%s) attempt=%d; will retry next tick',
          eventType, id, entry.attempts)
      }
    }
  })

  const transport = new StdioServerTransport()
  await server.connect(transport)
  console.error('[A3C MCP Server] v0.2.0 Started, connecting to %s', PLATFORM_URL)
}

main().catch(console.error)
