import type {
  CanonicalEntityReference,
  EventProminence,
  HistoricalEvent,
  HistoryEventFilterMetadata,
  HistoryEventQuery,
  HistoryEventQueryResult,
  HistoryEventRepository,
  PrimaryCategory,
  TimeExpression,
} from '../domain/history'
import { HistoryEventQueryTooLargeError, resolveHistoryEventQueryPadding } from '../domain/history'

interface HttpRepositoryOptions {
  baseUrl?: string
  fetch?: typeof fetch
  /**
   * 每次查询在可视范围两侧额外读取的比例；默认 0.5，即事件窗口约为可视范围的两倍。
   * 设为 0 时只返回可视范围内的历史事件。
   */
  paddingRatio?: number
}

interface ProblemDetails {
  title?: string
  detail?: string
  code?: string
}

interface ApiHistoricalEvent {
  id: string
  title: string
  summary: string
  narrative: string
  time: TimeExpression
  primaryCategory: PrimaryCategory
  periods: CanonicalEntityReference[]
  regions: CanonicalEntityReference[]
  places: CanonicalEntityReference[]
  figures: CanonicalEntityReference[]
  topicTags: CanonicalEntityReference[]
  prominence: EventProminence
  displayOrder: number
}

interface ApiEventQueryResult {
  events: ApiHistoricalEvent[]
  sourceTotal: number
  totalMatching: number
  returnedProminence: EventProminence
  coveredFrom?: number
  coveredTo?: number
}

interface ApiEventFilterMetadata {
  periodGroups: Array<{
    context: CanonicalEntityReference
    periods: CanonicalEntityReference[]
  }>
  regions: CanonicalEntityReference[]
  figures: CanonicalEntityReference[]
  primaryCategories: PrimaryCategory[]
}

const defaultPaddingRatio = 0.5

export class HttpRepositoryError extends Error {
  readonly status: number
  readonly code?: string

  constructor(message: string, status: number, code?: string) {
    super(message)
    this.name = 'HttpRepositoryError'
    this.status = status
    this.code = code
  }
}

export function createHttpHistoryEventRepository(
  options: HttpRepositoryOptions = {},
): HistoryEventRepository {
  const baseUrl = options.baseUrl?.replace(/\/$/, '') ?? ''
  const request = options.fetch ?? globalThis.fetch.bind(globalThis)
  const paddingRatio = options.paddingRatio ?? defaultPaddingRatio

  return {
    async getBounds(signal) {
      const response = await getJSON<{
        hasEvents: boolean
        start: number | null
        end: number | null
      }>(request, `${baseUrl}/api/v1/event-bounds`, signal)

      if (!response.hasEvents) return null
      if (!isFiniteNumber(response.start) || !isFiniteNumber(response.end)) {
        throw new Error('历史事件服务返回了无效的时间边界。')
      }
      return { start: response.start, end: response.end }
    },

    async getFilterMetadata(signal) {
      const response = await getJSON<ApiEventFilterMetadata>(
        request,
        `${baseUrl}/api/v1/event-metadata`,
        signal,
      )
      return mapFilterMetadata(response)
    },

    async query(query) {
      const padding = resolveHistoryEventQueryPadding(query, paddingRatio)
      try {
        return mapQueryResult(
          await requestEventQuery(request, baseUrl, query, padding),
        )
      } catch (error) {
        if (padding > 0 && error instanceof HistoryEventQueryTooLargeError) {
          // 预取窗口可能因过于密集而超限；回退到仅可视范围，保持原有可用性。
          return mapQueryResult(
            await requestEventQuery(request, baseUrl, query, 0),
          )
        }
        throw error
      }
    },

    async getById(eventId, signal) {
      const response = await getJSON<ApiHistoricalEvent>(
        request,
        `${baseUrl}/api/v1/events/${encodeURIComponent(eventId)}`,
        signal,
      )
      return mapHistoricalEvent(response)
    },
  }
}

function requestEventQuery(
  request: typeof fetch,
  baseUrl: string,
  query: HistoryEventQuery,
  padding: number,
): Promise<ApiEventQueryResult> {
  const parameters = createQueryParameters(query, padding)
  return getJSON<ApiEventQueryResult>(
    request,
    `${baseUrl}/api/v1/events?${parameters}`,
    query.signal,
  )
}

function mapQueryResult(
  response: ApiEventQueryResult,
): HistoryEventQueryResult {
  const {
    events,
    sourceTotal,
    totalMatching,
    returnedProminence,
    coveredFrom,
    coveredTo,
  } = response
  const result: HistoryEventQueryResult = {
    events: events.map(mapHistoricalEvent),
    sourceTotal,
    totalMatching,
    returnedProminence,
  }
  if (isFiniteNumber(coveredFrom) && isFiniteNumber(coveredTo)) {
    result.coveredRange = { start: coveredFrom, end: coveredTo }
  }
  return result
}

function createQueryParameters(
  query: HistoryEventQuery,
  padding: number,
): URLSearchParams {
  const parameters = new URLSearchParams({
    from: formatCoordinate(query.visibleRange.start),
    to: formatCoordinate(query.visibleRange.end),
  })
  if (padding > 0) parameters.set('pad', formatCoordinate(padding))
  const searchTerm = query.searchTerm?.trim()
  if (searchTerm) parameters.set('q', searchTerm)

  appendValues(parameters, 'period', query.filters?.periods)
  appendValues(parameters, 'region', query.filters?.regions)
  appendValues(parameters, 'figure', query.filters?.figures)
  appendValues(parameters, 'category', query.filters?.primaryCategories)
  return parameters
}

function appendValues(
  parameters: URLSearchParams,
  name: string,
  values: readonly string[] | undefined,
) {
  for (const value of values ?? []) parameters.append(name, value)
}

function mapFilterMetadata(
  metadata: ApiEventFilterMetadata,
): HistoryEventFilterMetadata {
  return {
    periodGroups: metadata.periodGroups.map((group) => ({
      context: group.context,
      periods: group.periods,
    })),
    regions: metadata.regions,
    figures: metadata.figures,
    primaryCategories: metadata.primaryCategories,
  }
}

function mapHistoricalEvent(event: ApiHistoricalEvent): HistoricalEvent {
  return {
    id: event.id,
    title: event.title,
    summary: event.summary,
    narrative: event.narrative,
    time: event.time,
    primaryCategory: event.primaryCategory,
    periods: event.periods.map(displayName),
    regions: event.regions.map(displayName),
    places: event.places.map(displayName),
    figures: event.figures.map(displayName),
    topicTags: event.topicTags.map(displayName),
    prominence: event.prominence,
    editorialPriority: event.displayOrder,
  }
}

function displayName(reference: CanonicalEntityReference): string {
  return reference.disambiguationLabel
    ? `${reference.name}（${reference.disambiguationLabel}）`
    : reference.name
}

async function getJSON<T>(
  request: typeof fetch,
  url: string,
  signal?: AbortSignal,
): Promise<T> {
  const response = await request(url, {
    method: 'GET',
    headers: { Accept: 'application/json, application/problem+json' },
    signal,
  })
  if (!response.ok) {
    let problem: ProblemDetails | undefined
    try {
      problem = (await response.json()) as ProblemDetails
    } catch {
      // 非 JSON 错误仍使用稳定的 HTTP 回退信息。
    }
    if (problem?.code === 'result_set_too_large') {
      throw new HistoryEventQueryTooLargeError(
        problem.detail ?? problem.title,
      )
    }
    throw new HttpRepositoryError(
      problem?.detail ?? problem?.title ?? `历史事件服务返回 HTTP ${response.status}。`,
      response.status,
      problem?.code,
    )
  }
  return (await response.json()) as T
}

function formatCoordinate(coordinate: number): string {
  return Number(coordinate.toFixed(6)).toString()
}

function isFiniteNumber(value: unknown): value is number {
  return typeof value === 'number' && Number.isFinite(value)
}
