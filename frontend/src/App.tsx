import { BrowserRouter, Routes, Route, Navigate } from 'react-router-dom'
import { useState } from 'react'
import { useAppStore } from './stores/appStore'
import OverviewPage from './pages/OverviewPage'
import TaskPage from './pages/TaskPage'
import SubmissionPage from './pages/SubmissionPage'
import ActivityPage from './pages/ActivityPage'
import SettingsPage from './pages/SettingsPage'
import PRPage from './pages/PRPage'
import { Layout } from './components/Layout'
import LoginPanel from './components/LoginPanel'

function AuthenticatedApp() {
  const { selectedProjectId } = useAppStore()
  const [loggedIn, setLoggedIn] = useState(false)

  if (!loggedIn) {
    return <LoginPanel onLogin={() => setLoggedIn(true)} />
  }

  if (!selectedProjectId) {
    return <Navigate to="/" replace />
  }

  return (
    <Layout>
      <Routes>
        <Route path="/" element={<OverviewPage />} />
        <Route path="/tasks" element={<TaskPage />} />
        <Route path="/submissions" element={<SubmissionPage />} />
        <Route path="/activity" element={<ActivityPage />} />
        <Route path="/prs" element={<PRPage />} />
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