import '@testing-library/jest-dom/vitest'
import { repositoryForDemoUrl } from '../data/demoStateRepository'

window.__historyWikiDemoRepositoryFactory = repositoryForDemoUrl
import { cleanup } from '@testing-library/react'
import { afterEach } from 'vitest'

afterEach(() => {
  cleanup()
  window.history.replaceState({}, '', '/')
})
