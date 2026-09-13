/// <reference types="vite/client" />

import type { HistoryEventRepository } from './domain/history'

declare global {
  interface Window {
    __historyWikiDemoRepositoryFactory?: (
      defaultRepository: HistoryEventRepository,
    ) => HistoryEventRepository
  }
}
