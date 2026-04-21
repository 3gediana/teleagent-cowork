import { useEffect, useRef, useCallback, useState } from 'react'
import { useAppStore, BroadcastEvent, ActivityItem } from '../stores/appStore'
import { dashboardApi } from '../api/endpoints'

export function useSSE(projectId: string | null) {
  const esRef = useRef<EventSource | null>(null)
  const setProject = useAppStore((s) => s.setProject)
  const addChatMessage = useAppStore((s) => s.addChatMessage)
  const addBroadcastEvent = useAppStore((s) => s.addBroadcastEvent)
  const addActivity = useAppStore((s) => s.addActivity)
  const addPendingChange = useAppStore((s) => s.addPendingChange)
  const [isConnected, setIsConnected] = useState(false)

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

    const handleEvent = (eventType: string, e: MessageEvent) => {
      try {
        const data = JSON.parse(e.data)
        console.log(`[SSE] ${eventType}:`, data)

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
              addChatMessage({
                id: `${Date.now()}-chat`,
                role: 'agent',
                content: String(data.payload.content),
                timestamp: Date.now(),
              })
            }
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
      'CHANGE_PENDING_CONFIRM', 'CONTEXT_CLEARED', 'CHAT_UPDATE', 'TOOL_CALL'
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
  }, [projectId, setProject, addChatMessage, addBroadcastEvent, addActivity, addPendingChange, refreshState])

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
