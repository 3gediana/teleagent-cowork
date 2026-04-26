import { useAppStore } from '../stores/appStore'
import { dashboardApi, projectApi } from '../api/endpoints'

export function useDashboard() {
  const { setProject, setSelectedProjectId, selectedProjectId, setLoading, setAutoMode } = useAppStore()

  const refreshState = async () => {
    if (!selectedProjectId) return
    setLoading(true)
    try {
      const res = await dashboardApi.getState(selectedProjectId)
      if (res.success) {
        setProject({
          id: selectedProjectId,
          name: '',
          direction: res.data.direction || null,
          milestone: res.data.milestone || null,
          milestoneId: res.data.milestone_id || null,
          version: res.data.version || 'v1.0',
          tasks: res.data.tasks || [],
          locks: res.data.locks || [],
          agents: res.data.agents || [],
        })
        // Keep the store's AutoMode flag aligned with the server's truth
        // on every refresh. Without this, the toggle in the header would
        // drift whenever another operator flipped it.
        if (typeof res.data.auto_mode === 'boolean') {
          setAutoMode(res.data.auto_mode)
        }
      }
    } finally {
      setLoading(false)
    }
  }

  const selectProject = async (projectId: string) => {
    setSelectedProjectId(projectId)
    const res = await dashboardApi.getState(projectId)
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
      if (typeof res.data.auto_mode === 'boolean') {
        setAutoMode(res.data.auto_mode)
      }
    }
  }

  const createProject = async (name: string, description?: string, githubRepo?: string, importExisting?: boolean) => {
    const res = await projectApi.create(name, description, githubRepo, importExisting)
    if (res.success) {
      return res.data
    }
    return null
  }

  return { refreshState, selectProject, createProject }
}