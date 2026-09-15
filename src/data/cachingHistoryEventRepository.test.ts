import { vi } from 'vitest'
import type {
  HistoricalEvent,
  HistoryEventQuery,
  HistoryEventQueryResult,
  HistoryEventRepository,
} from '../domain/history'
import { createCachingHistoryEventRepository } from './cachingHistoryEventRepository'

const event: HistoricalEvent = {
  id: 'event',
  title: '示例事件',
  summary: '摘要',
  narrative: '正文',
  time: { kind: 'year', year: { era: 'CE', year: 100 } },
  primaryCategory: '文化',
  periods: [],
  regions: [],
  places: [],
  figures: [],
  topicTags: [],
  prominence: 1,
  editorialPriority: 1,
}

function resultFor(): HistoryEventQueryResult {
  return {
    events: [event],
    sourceTotal: 1,
    totalMatching: 1,
    returnedProminence: 1,
  }
}

function createInnerRepository() {
  const query = vi.fn<HistoryEventRepository['query']>(async () => resultFor())
  const repository: HistoryEventRepository = {
    getBounds: vi.fn().mockResolvedValue({ start: 0, end: 1_000 }),
    getFilterMetadata: vi.fn().mockResolvedValue({
      periodGroups: [],
      regions: [],
      figures: [],
      primaryCategories: [],
    }),
    query,
    getById: vi.fn().mockResolvedValue(event),
  }
  return { repository, query }
}

function delay(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms))
}

describe('缓存与预取的历史事件 Repository', () => {
  it('合并连续视窗变化，只向数据源发出最后一次查询', async () => {
    const { repository, query } = createInnerRepository()
    const caching = createCachingHistoryEventRepository(repository, {
      debounceMs: 40,
      prefetchDelayMs: 0,
    })
    await caching.getBounds()

    const controller = new AbortController()
    const first = caching.query({
      visibleRange: { start: 0, end: 100 },
      signal: controller.signal,
    })
    controller.abort()
    await expect(first).rejects.toMatchObject({ name: 'AbortError' })

    const second = caching.query({ visibleRange: { start: 20, end: 120 } })
    await expect(second).resolves.toMatchObject({ totalMatching: 1 })
    expect(query).toHaveBeenCalledTimes(1)
    expect(query.mock.calls[0][0].visibleRange).toEqual({ start: 20, end: 120 })
  })

  it('完全相同的范围与条件直接命中缓存', async () => {
    const { repository, query } = createInnerRepository()
    const caching = createCachingHistoryEventRepository(repository, {
      debounceMs: 0,
      prefetchDelayMs: 0,
    })

    const request: HistoryEventQuery = {
      visibleRange: { start: 0, end: 100 },
      searchTerm: '示例',
      filters: {
        periods: [],
        regions: ['region-b', 'region-a'],
        figures: [],
        primaryCategories: ['文化'],
      },
    }
    await caching.query(request)
    await caching.query({ ...request, searchTerm: '  示例  ' })

    expect(query).toHaveBeenCalledTimes(1)
  })

  it('筛选条件变化时不命中缓存', async () => {
    const { repository, query } = createInnerRepository()
    const caching = createCachingHistoryEventRepository(repository, {
      debounceMs: 0,
      prefetchDelayMs: 0,
    })

    await caching.query({
      visibleRange: { start: 0, end: 100 },
      filters: { regions: ['region-a'] },
    })
    await caching.query({
      visibleRange: { start: 0, end: 100 },
      filters: { regions: ['region-b'] },
    })

    expect(query).toHaveBeenCalledTimes(2)
  })

  it('空闲后预取缩小后的范围，命中预取结果时不再请求数据源', async () => {
    const { repository, query } = createInnerRepository()
    const caching = createCachingHistoryEventRepository(repository, {
      debounceMs: 0,
      prefetchDelayMs: 20,
    })
    await caching.getBounds()

    await caching.query({ visibleRange: { start: 0, end: 100 } })
    await delay(60)

    const prefetched = query.mock.calls
      .map((call) => call[0].visibleRange)
      .find((range) => range.start === 0 && range.end === 140)
    expect(prefetched).toEqual({ start: 0, end: 140 })

    const callsAfterPrefetch = query.mock.calls.length
    await caching.query({ visibleRange: { start: 0, end: 140 } })
    expect(query).toHaveBeenCalledTimes(callsAfterPrefetch)
  })

  it('沿平移方向预取相邻视窗', async () => {
    const { repository, query } = createInnerRepository()
    const caching = createCachingHistoryEventRepository(repository, {
      debounceMs: 0,
      prefetchDelayMs: 20,
    })
    await caching.getBounds()

    await caching.query({ visibleRange: { start: 0, end: 100 } })
    await delay(60)
    await caching.query({ visibleRange: { start: 100, end: 200 } })
    await delay(60)

    const ranges = query.mock.calls.map((call) => call[0].visibleRange)
    expect(ranges).toContainEqual({ start: 200, end: 300 })
  })

  it('缓存过期后重新向数据源确认', async () => {
    const { repository, query } = createInnerRepository()
    const caching = createCachingHistoryEventRepository(repository, {
      debounceMs: 0,
      cacheTtlMs: 20,
      prefetchDelayMs: 0,
    })

    await caching.query({ visibleRange: { start: 0, end: 100 } })
    await delay(40)
    await caching.query({ visibleRange: { start: 0, end: 100 } })

    expect(query).toHaveBeenCalledTimes(2)
  })

  it('过大的结果不进入缓存，避免长期占用内存', async () => {
    const { repository, query } = createInnerRepository()
    query.mockResolvedValue({
      events: [{ ...event }],
      sourceTotal: 5,
      totalMatching: 5,
      returnedProminence: 1,
    })
    const caching = createCachingHistoryEventRepository(repository, {
      debounceMs: 0,
      maximumCachedEvents: 0,
      prefetchDelayMs: 0,
    })

    await caching.query({ visibleRange: { start: 0, end: 100 } })
    await caching.query({ visibleRange: { start: 0, end: 100 } })

    expect(query).toHaveBeenCalledTimes(2)
  })
})
