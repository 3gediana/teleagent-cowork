import { BrowserRouter, Routes, Route, Navigate } from 'react-router-dom'
import { useAppStore } from './stores/appStore'
import OverviewPage from './pages/OverviewPage'
import TaskPage from './pages/TaskPage'
import SubmissionPage from './pages/SubmissionPage'
import ActivityPage from './pages/ActivityPage'
import SettingsPage from './pages/SettingsPage'
import PRPage from './pages/PRPage'
import ChiefPage from './pages/ChiefPage'
import KnowledgePage from './pages/KnowledgePage'
import TagReviewPage from './pages/TagReviewPage'
import LLMEndpointsPage from './pages/LLMEndpointsPage'
import AgentPoolPage from './pages/AgentPoolPage'
import LoopCheckPage from './pages/LoopCheckPage'
import WorkspacePickerPage from './pages/WorkspacePickerPage'
import FirstRunBootstrapPage from './pages/FirstRunBootstrapPage'
import { Layout } from './components/Layout'

function AuthenticatedApp() {
  const accessKey = useAppStore((s) => s.accessKey)
  const selectedProjectId = useAppStore((s) => s.selectedProjectId)

  // No access key in localStorage → we haven't bootstrapped on this
  // browser yet.  The workspace picker used to cover this state by
  // accident (pre-auth /project/list returned [] for unauthed callers)
  // but after the auth tightening every request now 401s, leaving the
  // user at a dead end.  Route them through the bootstrap panel first.
  if (!accessKey) {
    return <FirstRunBootstrapPage />
  }

  // No project selected → render the picker INSIDE the normal shell so
  // the first-contact UI shares the same chrome (sidebar, header) as
  // the rest of Mission Control.  The sidebar itself hides its nav in
  // this state and the header degrades to brand-only.
  if (!selectedProjectId) {
    return (
      <Layout>
        <WorkspacePickerPage />
      </Layout>
    )
  }

  return (
    <Layout>
      <Routes>
        <Route path="/" element={<OverviewPage />} />
        <Route path="/tasks" element={<TaskPage />} />
        <Route path="/submissions" element={<SubmissionPage />} />
        <Route path="/activity" element={<ActivityPage />} />
        <Route path="/prs" element={<PRPage />} />
        <Route path="/chief" element={<ChiefPage />} />
        <Route path="/pool" element={<AgentPoolPage />} />
        <Route path="/knowledge" element={<KnowledgePage />} />
        <Route path="/tags" element={<TagReviewPage />} />
        <Route path="/llm" element={<LLMEndpointsPage />} />
        <Route path="/loopcheck" element={<LoopCheckPage />} />
        <Route path="/settings" element={<SettingsPage />} />
        <Route path="*" element={<Navigate to="/" replace />} />
      </Routes>
    </Layout>
  )
}

function App() {
  return (
    <BrowserRouter>
      <AuthenticatedApp />
    </BrowserRouter>
  )
}

export default App