import { useEffect, useRef, useCallback, useState } from 'react'
import { useAppStore, BroadcastEvent, ActivityItem } from '../stores/appStore'
import { dashboardApi } from '../api/endpoints'

export function useSSE(projectId: string | null) {
  const esRef = useRef<EventSource | null>(null)
  const setProject = useAppStore((s) => s.setProject)
  const addChatMessage = useAppStore((s) => s.addChatMessage)
  const upsertChatMessage = useAppStore((s) => s.upsertChatMessage)
  const removeChatMessage = useAppStore((s) => s.removeChatMessage)
  const addBroadcastEvent = useAppStore((s) => s.addBroadcastEvent)
  const addActivity = useAppStore((s) => s.addActivity)
  const addPendingChange = useAppStore((s) => s.addPendingChange)
  const [isConnected, setIsConnected] = useState(false)
  // Per-session buffers for live token streaming. Each native-runtime
  // session's AGENT_TEXT_DELTA events append into `streamBuffers[sid]`
  // and render through a single chat message keyed `stream-${sid}`.
  // When CHAT_UPDATE arrives carrying the same session_id we drop the
  // streaming placeholder and let addChatMessage install the final
  // version as a fresh row. Ref rather than state so typewriter
  // updates don't cascade through React reconciliation.
  const streamBuffers = useRef<Record<string, string>>({})

  const refreshState = useCallback(async () => {
    if (!projectId) return
    const res = await dashboardApi.getState(projectId)
    if (res.success) {
      setProject({
        id: projectId,
        name: res.data.name || 'Untitled Project',
        direction: res.data.direction || null,
        milestone: res.data.milestone || null,
        milestoneId: res.data.milestone_id || null,
        version: res.data.version || 'v1.0',
        tasks: res.data.tasks || [],
        locks: res.data.locks || [],
        agents: res.data.agents || [],
      })
    }
  }, [projectId, setProject])

  const connect = useCallback(() => {
    if (!projectId) return
    const key = localStorage.getItem('a3c_access_key')
    const url = `/api/v1/events?key=${key}&project_id=${projectId}`

    const es = new EventSource(url)
    esRef.current = es

    es.onopen = () => {
      console.log('[SSE] Connected')
      setIsConnected(true)
    }

    // High-frequency events (~30 Hz during live streaming) that must
    // NOT push a new entry into the activity feed or broadcast buffer
    // — both are bounded in-memory arrays, flooding them would crowd
    // out everything interesting.
    const highFreq = new Set(['AGENT_TEXT_DELTA', 'AGENT_TURN'])

    const handleEvent = (eventType: string, e: MessageEvent) => {
      try {
        const data = JSON.parse(e.data)
        // Don't console.log AGENT_TEXT_DELTA either — one line per
        // token destroys dev-tools performance.
        if (!highFreq.has(eventType)) {
          console.log(`[SSE] ${eventType}:`, data)
        }

        if (!highFreq.has(eventType)) {
          const event: BroadcastEvent = {
            id: `${Date.now()}-${Math.random().toString(36).slice(2)}`,
            type: eventType,
            payload: data.payload || {},
            timestamp: Date.now(),
          }
          addBroadcastEvent(event)

          const agentName = data.payload?.agent_name || 'System'
          const action = getActionText(eventType)
          const activity: ActivityItem = {
            id: event.id,
            agentName: String(agentName),
            action,
            target: getTargetText(eventType, data.payload),
            timestamp: Date.now(),
          }
          addActivity(activity)
        }

        switch (eventType) {
          case 'CHANGE_PENDING_CONFIRM':
            addPendingChange({
              change_id: String(data.payload?.change_id || ''),
              agent_id: String(data.payload?.agent_id || ''),
              task_id: String(data.payload?.task_id || ''),
              description: String(data.payload?.description || ''),
            })
            addChatMessage({
              id: `${Date.now()}-pending`,
              role: 'system',
              content: `Change pending your confirmation: ${data.payload?.description || data.payload?.change_id}`,
              timestamp: Date.now(),
            })
            refreshState()
            break
          case 'CONTEXT_CLEARED':
            addChatMessage({
              id: `${Date.now()}-cleared`,
              role: 'system',
              content: 'Session context cleared.',
              timestamp: Date.now(),
            })
            break
          case 'CHAT_UPDATE':
            if (data.payload?.role === 'agent' && data.payload?.content) {
              // Native-runtime sessions carry session_id. We upsert
              // under the SAME stream-${sid} id the typewriter was
              // writing into, so the message stays in place and
              // flips from "in-progress" to "final" with zero flicker.
              // Opencode sessions have no session_id here — we fall
              // back to appending a fresh row with a unique id.
              const sid = data.payload?.session_id
                ? String(data.payload.session_id)
                : ''
              if (sid) {
                delete streamBuffers.current[sid]
                upsertChatMessage({
                  id: `stream-${sid}`,
                  role: 'agent',
                  content: String(data.payload.content),
                  timestamp: Date.now(),
                })
              } else {
                addChatMessage({
                  id: `${Date.now()}-chat`,
                  role: 'agent',
                  content: String(data.payload.content),
                  timestamp: Date.now(),
                })
              }
            }
            break
          case 'AGENT_TEXT_DELTA': {
            // Native-runtime live typewriter. Deltas arrive frequently
            // (per-token for Anthropic, per-chunk for OpenAI); we keep
            // a session-scoped buffer + repeatedly upsert the same
            // message. Cheap because React only re-renders when the
            // chatMessages array reference changes, which it does
            // once per delta — measured fine for the delta rates we
            // see in practice (~30 Hz).
            const sid = String(data.payload?.session_id || '')
            const delta = String(data.payload?.delta || '')
            if (!sid || !delta) break
            streamBuffers.current[sid] =
              (streamBuffers.current[sid] || '') + delta
            upsertChatMessage({
              id: `stream-${sid}`,
              role: 'agent',
              content: streamBuffers.current[sid],
              timestamp: Date.now(),
            })
            break
          }
          case 'AGENT_DONE':
            // Final marker for native-runtime sessions. The chat
            // panel already has the complete reply from CHAT_UPDATE;
            // this event is mostly for activity feed + "session
            // running" badges. Refresh state so task/lock changes
            // triggered by the session show up.
            refreshState()
            break
          case 'AGENT_ERROR':
            // Surface as a system message so operators can see the
            // failure without digging through logs.
            addChatMessage({
              id: `${Date.now()}-agent-error`,
              role: 'system',
              content: `Agent error: ${data.payload?.error || 'unknown'}`,
              timestamp: Date.now(),
            })
            // Clean up any streaming placeholder for the failed session.
            if (data.payload?.session_id) {
              const sid = String(data.payload.session_id)
              removeChatMessage(`stream-${sid}`)
              delete streamBuffers.current[sid]
            }
            break
          case 'AGENT_TURN':
            // Per-iteration telemetry. Not user-facing today; kept in
            // the broadcast buffer so dashboards can plot it later.
            break
          case 'TOOL_CALL':
            addChatMessage({
              id: `${Date.now()}-tool`,
              role: 'system',
              content: `Tool called: ${data.payload?.tool || 'unknown'}`,
              timestamp: Date.now(),
            })
            refreshState()
            break
          case 'DIRECTION_CHANGE':
          case 'MILESTONE_UPDATE':
          case 'MILESTONE_SWITCH':
          case 'VERSION_UPDATE':
          case 'VERSION_ROLLBACK':
          case 'AUDIT_RESULT':
          case 'TASK_CLAIMED':
          case 'TASK_COMPLETED':
          case 'FILE_LOCKED':
          case 'FILE_UNLOCKED':
          case 'AGENT_JOIN':
          case 'AGENT_LEAVE':
            refreshState()
            break
          default:
            refreshState()
        }
      } catch (err) {
        console.error('[SSE] Parse error:', err)
      }
    }

    const eventTypes = [
      'DIRECTION_CHANGE', 'MILESTONE_UPDATE', 'MILESTONE_SWITCH',
      'VERSION_UPDATE', 'VERSION_ROLLBACK', 'AUDIT_RESULT',
      'TASK_CLAIMED', 'TASK_COMPLETED', 'FILE_LOCKED', 'FILE_UNLOCKED',
      'AGENT_JOIN', 'AGENT_LEAVE', 'CHANGE_PENDING', 'CHANGE_APPROVED',
      'CHANGE_PENDING_CONFIRM', 'CONTEXT_CLEARED', 'CHAT_UPDATE', 'TOOL_CALL',
      // Native-runtime additions (Phase 2). Frontend-only extensions;
      // the backend currently emits them for sessions whose role is
      // routed to the native runner via an "llm_" endpoint override.
      'AGENT_TEXT_DELTA', 'AGENT_TURN', 'AGENT_DONE', 'AGENT_ERROR',
    ]
    eventTypes.forEach((type) => {
      es.addEventListener(type, (e) => handleEvent(type, e))
    })

    es.onerror = () => {
      console.error('[SSE] Connection error')
      setIsConnected(false)
      es.close()
      setTimeout(connect, 5000)
    }
  }, [projectId, setProject, addChatMessage, upsertChatMessage, removeChatMessage, addBroadcastEvent, addActivity, addPendingChange, refreshState])

  const disconnect = useCallback(() => {
    if (esRef.current) {
      esRef.current.close()
      esRef.current = null
      setIsConnected(false)
    }
  }, [])

  useEffect(() => {
    if (projectId) {
      connect()
    } else {
      disconnect()
    }
    return disconnect
  }, [projectId, connect, disconnect])

  return { connect, disconnect, refreshState, isConnected }
}

function getActionText(type: string): string {
  const actions: Record<string, string> = {
    DIRECTION_CHANGE: 'updated direction',
    MILESTONE_UPDATE: 'updated milestone',
    MILESTONE_SWITCH: 'switched milestone',
    VERSION_UPDATE: 'created version',
    VERSION_ROLLBACK: 'rolled back version',
    AUDIT_RESULT: 'completed audit',
    TASK_CLAIMED: 'claimed task',
    TASK_COMPLETED: 'completed task',
    FILE_LOCKED: 'locked files',
    FILE_UNLOCKED: 'unlocked files',
    AGENT_JOIN: 'joined project',
    AGENT_LEAVE: 'left project',
    CHANGE_PENDING: 'submitted change',
    CHANGE_APPROVED: 'approved change',
    CHANGE_PENDING_CONFIRM: 'change awaiting confirmation',
    CONTEXT_CLEARED: 'cleared session context',
    CHAT_UPDATE: 'replied',
    TOOL_CALL: 'called tool',
    // Native-runtime events.
    AGENT_TEXT_DELTA: 'streaming',
    AGENT_TURN: 'finished turn',
    AGENT_DONE: 'completed',
    AGENT_ERROR: 'errored',
  }
  return actions[type] || 'performed action'
}

function getTargetText(type: string, payload: Record<string, unknown>): string | undefined {
  switch (type) {
    case 'VERSION_UPDATE':
    case 'VERSION_ROLLBACK':
      return String(payload.new_version || payload.version || '')
    case 'TASK_CLAIMED':
    case 'TASK_COMPLETED':
      return String(payload.task_name || payload.task_id || '')
    case 'FILE_LOCKED':
    case 'FILE_UNLOCKED':
      return payload.files ? String((payload.files as string[]).join(', ')) : undefined
    default:
      return undefined
  }
}
