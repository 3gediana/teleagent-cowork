import { useEffect, useRef, useCallback } from 'react'
import { useAppStore } from '../stores/appStore'
import { dashboardApi } from '../api/endpoints'

export function useSSE(projectId: string | null) {
  const esRef = useRef<EventSource | null>(null)
  const setProject = useAppStore((s) => s.setProject)
  const addChatMessage = useAppStore((s) => s.addChatMessage)

  const connect = useCallback(() => {
    if (!projectId) return
    const key = localStorage.getItem('a3c_access_key')
    const url = `/api/v1/events?key=${key}&project_id=${projectId}`

    const es = new EventSource(url)
    esRef.current = es

    const handleEvent = (eventType: string, e: MessageEvent) => {
      try {
        const data = JSON.parse(e.data)
        console.log(`[SSE] ${eventType}:`, data)

        switch (eventType) {
          case 'DIRECTION_CHANGE':
            addChatMessage({
              id: Date.now().toString(),
              role: 'system',
              content: `Direction updated: ${data.payload?.content || data.payload?.reason || 'Updated'}`,
              timestamp: Date.now(),
            })
            dashboardApi.getState(projectId).then((res: any) => {
              if (res.success) {
                setProject({
                  id: projectId,
                  name: '',
                  direction: res.data.direction || null,
                  milestone: res.data.milestone || null,
                  milestoneId: res.data.milestone_id || null,
                  version: res.data.version || 'v1.0',
                  tasks: res.data.tasks || [],
                  locks: res.data.locks || [],
                  agents: res.data.agents || [],
                })
              }
            })
            break

          case 'MILESTONE_UPDATE':
            addChatMessage({
              id: Date.now().toString(),
              role: 'system',
              content: `Milestone update: ${data.payload?.content || data.payload?.reason || 'Updated'}`,
              timestamp: Date.now(),
            })
            dashboardApi.getState(projectId).then((res: any) => {
              if (res.success) {
                setProject({
                  id: projectId,
                  name: '',
                  direction: res.data.direction || null,
                  milestone: res.data.milestone || null,
                  milestoneId: res.data.milestone_id || null,
                  version: res.data.version || 'v1.0',
                  tasks: res.data.tasks || [],
                  locks: res.data.locks || [],
                  agents: res.data.agents || [],
                })
              }
            })
            break

          case 'MILESTONE_SWITCH':
            addChatMessage({
              id: Date.now().toString(),
              role: 'system',
              content: `Milestone switched! New: ${data.payload?.content || ''}, Version: ${data.payload?.new_version || ''}`,
              timestamp: Date.now(),
            })
            dashboardApi.getState(projectId).then((res: any) => {
              if (res.success) {
                setProject({
                  id: projectId,
                  name: '',
                  direction: res.data.direction || null,
                  milestone: res.data.milestone || null,
                  milestoneId: res.data.milestone_id || null,
                  version: res.data.version || 'v1.0',
                  tasks: res.data.tasks || [],
                  locks: res.data.locks || [],
                  agents: res.data.agents || [],
                })
              }
            })
            break

          case 'VERSION_UPDATE':
            addChatMessage({
              id: Date.now().toString(),
              role: 'system',
              content: `Version updated to ${data.payload?.content || data.payload?.new_version || ''}`,
              timestamp: Date.now(),
            })
            dashboardApi.getState(projectId).then((res: any) => {
              if (res.success) {
                setProject({
                  id: projectId,
                  name: '',
                  direction: res.data.direction || null,
                  milestone: res.data.milestone || null,
                  milestoneId: res.data.milestone_id || null,
                  version: res.data.version || 'v1.0',
                  tasks: res.data.tasks || [],
                  locks: res.data.locks || [],
                  agents: res.data.agents || [],
                })
              }
            })
            break

          case 'VERSION_ROLLBACK':
            addChatMessage({
              id: Date.now().toString(),
              role: 'system',
              content: `Version rolled back to ${data.payload?.content || ''}`,
              timestamp: Date.now(),
            })
            dashboardApi.getState(projectId).then((res: any) => {
              if (res.success) {
                setProject({
                  id: projectId,
                  name: '',
                  direction: res.data.direction || null,
                  milestone: res.data.milestone || null,
                  milestoneId: res.data.milestone_id || null,
                  version: res.data.version || 'v1.0',
                  tasks: res.data.tasks || [],
                  locks: res.data.locks || [],
                  agents: res.data.agents || [],
                })
              }
            })
            break

          case 'AUDIT_RESULT':
            addChatMessage({
              id: Date.now().toString(),
              role: 'system',
              content: `[Audit] Change ${data.payload?.change_id || ''}: ${data.payload?.result || 'reviewed'}${data.payload?.new_version ? ', version: ' + data.payload.new_version : ''}`,
              timestamp: Date.now(),
            })
            dashboardApi.getState(projectId).then((res: any) => {
              if (res.success) {
                setProject({
                  id: projectId,
                  name: '',
                  direction: res.data.direction || null,
                  milestone: res.data.milestone || null,
                  milestoneId: res.data.milestone_id || null,
                  version: res.data.version || 'v1.0',
                  tasks: res.data.tasks || [],
                  locks: res.data.locks || [],
                  agents: res.data.agents || [],
                })
              }
            })
            break
        }
      } catch (err) {
        console.error('[SSE] Parse error:', err)
      }
    }

    const eventTypes = ['DIRECTION_CHANGE', 'MILESTONE_UPDATE', 'MILESTONE_SWITCH', 'VERSION_UPDATE', 'VERSION_ROLLBACK', 'AUDIT_RESULT']
    eventTypes.forEach((type) => {
      es.addEventListener(type, (e) => handleEvent(type, e))
    })

    es.onerror = () => {
      es.close()
      setTimeout(connect, 5000)
    }
  }, [projectId, setProject, addChatMessage])

  const disconnect = useCallback(() => {
    if (esRef.current) {
      esRef.current.close()
      esRef.current = null
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

  return { connect, disconnect }
}