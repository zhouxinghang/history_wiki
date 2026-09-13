import type { HistoryEventRepository } from '../domain/history'
import { createMockHistoryEventRepository } from './mockHistoryEventRepository'
import { createPerformanceEvents } from './performanceEvents'

export const demoStateParameter = 'demo'

export type DemoState = 'loading' | 'error' | 'empty' | 'performance'

let performanceRepository: HistoryEventRepository | undefined

export function repositoryForDemoUrl(
  defaultRepository: HistoryEventRepository,
  search: string = window.location.search,
): HistoryEventRepository {
  const demoState = new URLSearchParams(search).get(demoStateParameter)

  switch (demoState) {
    case 'loading':
      return createLoadingRepository()
    case 'error':
      return createErrorRepository()
    case 'empty':
      return createMockHistoryEventRepository([], { latencyMs: 90 })
    case 'performance':
      performanceRepository ??= createMockHistoryEventRepository(
        createPerformanceEvents(),
        { latencyMs: 0 },
      )
      return performanceRepository
    default:
      return defaultRepository
  }
}

function createLoadingRepository(): HistoryEventRepository {
  return {
    getBounds: waitForAbort,
    getFilterMetadata: waitForAbort,
    query: ({ signal }) => waitForAbort(signal),
    getById: (_eventId, signal) => waitForAbort(signal),
  }
}

function createErrorRepository(): HistoryEventRepository {
  const error = new Error('演示数据服务暂时不可用，请稍后重试。')

  return {
    getBounds: async () => {
      throw error
    },
    getFilterMetadata: async () => {
      throw error
    },
    query: async () => {
      throw error
    },
    getById: async () => {
      throw error
    },
  }
}

function waitForAbort<T>(signal?: AbortSignal): Promise<T> {
  return new Promise((_, reject) => {
    if (signal?.aborted) {
      reject(new DOMException('请求已取消', 'AbortError'))
      return
    }

    signal?.addEventListener(
      'abort',
      () => reject(new DOMException('请求已取消', 'AbortError')),
      { once: true },
    )
  })
}
