import type {
  HistoryEventQuery,
  HistoryEventQueryResult,
  HistoryEventRepository,
  TimeRange,
} from '../domain/history'
import { constrainTimelineRange } from '../domain/timeline'

export interface CachingRepositoryOptions {
  /**
   * 视窗连续变化时合并查询请求的等待时间。调用方（App）在视窗变化时会
   * abort 上一次查询，因此期间的等待会被取消，只有最后一次会真正发出。
   */
  debounceMs?: number
  /** 精确命中的查询结果缓存时长；超过后重新向数据源确认。 */
  cacheTtlMs?: number
  /** 最多缓存的查询结果数量。 */
  maximumCacheEntries?: number
  /** 单条结果事件数超过该值时不缓存，避免长期占用内存。 */
  maximumCachedEvents?: number
  /** 查询空闲后预取相邻范围的等待时间；设为 0 关闭预取。 */
  prefetchDelayMs?: number
}

const defaultOptions: Required<CachingRepositoryOptions> = {
  debounceMs: 120,
  cacheTtlMs: 30_000,
  maximumCacheEntries: 12,
  maximumCachedEvents: 2_000,
  prefetchDelayMs: 450,
}

interface CacheEntry {
  storedAt: number
  result: HistoryEventQueryResult
}

/**
 * 包裹公开历史事件数据源，合并连续的视窗查询并缓存精确命中的结果。
 *
 * 只缓存与请求范围完全一致的结果，因此 `totalMatching`、`returnedProminence`
 * 等语义保持不变；更宽的区间由后台预取补齐，而不是扩大单次查询范围。
 * 显著度由数据源依据可见范围跨度决定（ADR-0002），这里不做任何阈值判断。
 */
export function createCachingHistoryEventRepository(
  repository: HistoryEventRepository,
  options: CachingRepositoryOptions = {},
): HistoryEventRepository {
  const config = { ...defaultOptions, ...options }
  const cache = new Map<string, CacheEntry>()
  let bounds: TimeRange | null | undefined
  let previousRange: TimeRange | null = null
  let prefetchTimer: number | null = null
  let prefetchController: AbortController | null = null

  function readCachedResult(key: string): HistoryEventQueryResult | null {
    const entry = cache.get(key)
    if (!entry) return null
    if (Date.now() - entry.storedAt > config.cacheTtlMs) {
      cache.delete(key)
      return null
    }
    // 重新插入以维护 LRU 顺序。
    cache.delete(key)
    cache.set(key, entry)
    return entry.result
  }

  function writeCachedResult(
    key: string,
    result: HistoryEventQueryResult,
  ): boolean {
    if (result.events.length > config.maximumCachedEvents) return false
    cache.delete(key)
    cache.set(key, { storedAt: Date.now(), result })
    while (cache.size > config.maximumCacheEntries) {
      const oldest = cache.keys().next().value
      if (oldest === undefined) break
      cache.delete(oldest)
    }
    return true
  }

  function cancelPrefetch(): void {
    if (prefetchTimer !== null) {
      window.clearTimeout(prefetchTimer)
      prefetchTimer = null
    }
    prefetchController?.abort()
    prefetchController = null
  }

  function noteForegroundRange(range: TimeRange): number {
    let direction = 0
    if (previousRange) {
      const delta =
        range.start + range.end - previousRange.start - previousRange.end
      if (delta > 0) direction = 1
      else if (delta < 0) direction = -1
    }
    previousRange = range
    return direction
  }

  function prefetchTargets(range: TimeRange, direction: number): TimeRange[] {
    if (!bounds) return []
    const span = range.end - range.start
    if (span <= 0) return []

    const center = (range.start + range.end) / 2
    const candidates: TimeRange[] = [
      // 缩小按钮（0.72 → 1/0.72 ≈ 1.389）与滚轮缩小后的中心范围。
      { start: center - span * 0.7, end: center + span * 0.7 },
      // 查看全部或回到边界。
      { ...bounds },
    ]

    // 沿最近一次平移方向预取相邻视窗，让“区间快到头”时已有数据可用。
    if (direction > 0) {
      candidates.push({ start: range.end, end: range.end + span })
    } else if (direction < 0) {
      candidates.push({ start: range.start - span, end: range.start })
    }

    const unique = new Map<string, TimeRange>()
    for (const candidate of candidates) {
      const constrained = constrainTimelineRange(candidate, bounds)
      const key = createQueryKey({ visibleRange: constrained })
      unique.set(key, constrained)
    }
    return [...unique.values()]
  }

  async function runPrefetch(
    query: HistoryEventQuery,
    direction: number,
    controller: AbortController,
  ): Promise<void> {
    const criteria = { searchTerm: query.searchTerm, filters: query.filters }
    for (const target of prefetchTargets(query.visibleRange, direction)) {
      if (controller.signal.aborted) return
      if (sameRange(target, query.visibleRange)) continue
      const targetQuery: HistoryEventQuery = {
        ...criteria,
        visibleRange: target,
        signal: controller.signal,
      }
      const key = createQueryKey(targetQuery)
      if (cache.has(key)) continue
      try {
        const result = await repository.query(targetQuery)
        if (controller.signal.aborted) return
        if (!writeCachedResult(key, result)) return
      } catch {
        // 预取失败或被取消都不影响前台结果。
        return
      }
    }
  }

  function schedulePrefetch(query: HistoryEventQuery, direction: number): void {
    cancelPrefetch()
    if (config.prefetchDelayMs <= 0) return
    const controller = new AbortController()
    prefetchController = controller
    prefetchTimer = window.setTimeout(() => {
      prefetchTimer = null
      void runPrefetch(query, direction, controller)
    }, config.prefetchDelayMs)
  }

  return {
    async getBounds(signal) {
      const result = await repository.getBounds(signal)
      bounds = result
      return result
    },

    getFilterMetadata(signal) {
      return repository.getFilterMetadata(signal)
    },

    async query(query) {
      const key = createQueryKey(query)
      const cached = readCachedResult(key)
      if (cached) return cached

      cancelPrefetch()
      await waitForDelay(config.debounceMs, query.signal)
      const afterDelay = readCachedResult(key)
      if (afterDelay) return afterDelay

      const result = await repository.query(query)
      if (writeCachedResult(key, result)) {
        schedulePrefetch(query, noteForegroundRange(query.visibleRange))
      }
      return result
    },

    getById(eventId, signal) {
      return repository.getById(eventId, signal)
    },
  }
}

function sameRange(left: TimeRange, right: TimeRange): boolean {
  return left.start === right.start && left.end === right.end
}

function createQueryKey(
  query: Pick<
    HistoryEventQuery,
    'visibleRange' | 'searchTerm' | 'filters' | 'padding'
  >,
): string {
  return JSON.stringify({
    from: normalizeCoordinate(query.visibleRange.start),
    to: normalizeCoordinate(query.visibleRange.end),
    padding: query.padding ?? null,
    search: query.searchTerm?.trim() ?? '',
    periods: sortedValues(query.filters?.periods),
    regions: sortedValues(query.filters?.regions),
    figures: sortedValues(query.filters?.figures),
    categories: sortedValues(query.filters?.primaryCategories),
  })
}

function sortedValues(values?: readonly string[]): string[] {
  return values ? [...new Set(values)].sort() : []
}

function normalizeCoordinate(coordinate: number): number {
  return Number(coordinate.toFixed(6))
}

function waitForDelay(ms: number, signal?: AbortSignal): Promise<void> {
  if (ms <= 0) return Promise.resolve()
  return new Promise((resolve, reject) => {
    if (signal?.aborted) {
      reject(createAbortError())
      return
    }
    const timer = window.setTimeout(() => {
      signal?.removeEventListener('abort', handleAbort)
      resolve()
    }, ms)
    const handleAbort = () => {
      window.clearTimeout(timer)
      reject(createAbortError())
    }
    signal?.addEventListener('abort', handleAbort, { once: true })
  })
}

function createAbortError(): Error {
  return new DOMException('请求已取消', 'AbortError')
}
