import { useAppStore } from '../stores/appStore'
import { dashboardApi, projectApi } from '../api/endpoints'

export function useDashboard() {
  const { setProject, setSelectedProjectId, selectedProjectId, setLoading } = useAppStore()

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