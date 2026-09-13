import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import AdminApp from './AdminApp'
import App from './App'
import './styles.css'

if (import.meta.env.MODE !== 'production') {
  const { repositoryForDemoUrl } = await import('./data/demoStateRepository')
  window.__historyWikiDemoRepositoryFactory = repositoryForDemoUrl
}

const rootApp = window.location.pathname.startsWith('/admin')
  ? <AdminApp />
  : <App />

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    {rootApp}
  </StrictMode>,
)
