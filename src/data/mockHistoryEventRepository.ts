import {
  compareHistoricalEvents,
  HistoryEventQueryTooLargeError,
  maximumHistoryEventQueryResults,
  rangesIntersect,
  timeExpressionRange,
  type EventProminence,
  type CanonicalEntityReference,
  type HistoryEventFilterMetadata,
  type HistoricalEvent,
  type HistoryEventQuery,
  type HistoryEventRepository,
  type PrimaryCategory,
  type TimeRange,
} from '../domain/history'
import { demoEvents, demoPeriodContexts } from './demoEvents'

export interface MockRepositoryOptions {
  latencyMs?: number
  /** 模拟服务端 period_regions；key/value 均为展示名称。 */
  periodContexts?: Readonly<Record<string, readonly string[]>>
  /** 已知的规范实体元数据，可用于同名消歧测试。 */
  filterMetadata?: HistoryEventFilterMetadata
}

interface PreparedEvent {
  event: HistoricalEvent
  range: TimeRange
  searchableText: string
  periodIDs: string[]
  regionIDs: string[]
  figureIDs: string[]
}

interface FilterIdentityMaps {
  periods: ReadonlyMap<string, string>
  regions: ReadonlyMap<string, string>
  figures: ReadonlyMap<string, string>
}

const landmarkOnlyMinimumSpan = 4_000
const importantEventsMinimumSpan = 1_200
const primaryCategoryOrder: PrimaryCategory[] = [
  '政治',
  '军事',
  '文化',
  '科技',
  '社会',
  '交流',
]

export function createMockHistoryEventRepository(
  events: HistoricalEvent[] = demoEvents,
  options: MockRepositoryOptions = {},
): HistoryEventRepository {
  const source = [...events].sort(compareHistoricalEvents)
  const eventsById = new Map(source.map((event) => [event.id, event]))
  const periodContexts = options.periodContexts ?? demoPeriodContexts
  const filterMetadata = options.filterMetadata ??
    createFilterMetadata(source, periodContexts)
  const filterIdentityMaps = createFilterIdentityMaps(filterMetadata)
  const preparedSource = source.map((event) =>
    prepareEvent(event, filterIdentityMaps),
  )
  const bounds = createBounds(preparedSource)
  const latencyMs = options.latencyMs ?? 90

  return {
    async getBounds(signal) {
      await wait(latencyMs, signal)
      assertNotAborted(signal)

      return bounds
    },

    async getFilterMetadata(signal) {
      await wait(latencyMs, signal)
      assertNotAborted(signal)
      return filterMetadata
    },

    async query(query: HistoryEventQuery) {
      await wait(latencyMs, query.signal)
      assertNotAborted(query.signal)

      const returnedProminence = prominenceForSpan(
        query.visibleRange.end - query.visibleRange.start,
      )
      const normalizedSearch = normalizeSearchTerm(query.searchTerm)
      let totalMatching = 0
      const returnedEvents: HistoricalEvent[] = []

      for (const prepared of preparedSource) {
        if (
          !rangesIntersect(prepared.range, query.visibleRange) ||
          !matchesSearch(prepared, normalizedSearch) ||
          !matchesFilters(prepared, query.filters)
        ) {
          continue
        }

        totalMatching += 1
        if (prepared.event.prominence <= returnedProminence) {
          returnedEvents.push(prepared.event)
          if (returnedEvents.length > maximumHistoryEventQueryResults) {
            throw new HistoryEventQueryTooLargeError()
          }
        }
      }

      return {
        events: returnedEvents,
        sourceTotal: source.length,
        totalMatching,
        returnedProminence,
      }
    },

    async getById(eventId, signal) {
      await wait(latencyMs, signal)
      assertNotAborted(signal)
      const event = eventsById.get(eventId)
      if (!event) throw new Error('历史事件不存在。')
      return event
    },
  }
}

export const mockHistoryEventRepository = createMockHistoryEventRepository()

function createFilterMetadata(
  events: HistoricalEvent[],
  periodContexts?: Readonly<Record<string, readonly string[]>>,
): HistoryEventFilterMetadata {
  const periodsByContext = new Map<string, Set<string>>()
  const regions = new Set<string>()
  const figures = new Set<string>()
  const primaryCategories = new Set<HistoricalEvent['primaryCategory']>()

  for (const event of events) {
    for (const period of event.periods) {
      const contexts = periodContexts?.[period] ?? ['未指定语境']
      for (const context of contexts) {
        const periods = periodsByContext.get(context) ?? new Set<string>()
        periods.add(period)
        periodsByContext.set(context, periods)
      }
    }
    event.regions.forEach((region) => regions.add(region))
    event.figures.forEach((figure) => figures.add(figure))
    primaryCategories.add(event.primaryCategory)
  }

  return {
    periodGroups: [...periodsByContext]
      .map(([context, periods]) => ({
        context: createReference('region', context),
        periods: sortChinese(periods).map((period) =>
          createReference('period', period),
        ),
      }))
      .sort((left, right) => left.context.name.localeCompare(right.context.name, 'zh-CN')),
    regions: sortChinese(regions).map((region) =>
      createReference('region', region),
    ),
    figures: sortChinese(figures).map((figure) =>
      createReference('figure', figure),
    ),
    primaryCategories: primaryCategoryOrder.filter((category) =>
      primaryCategories.has(category),
    ),
  }
}

function createReference(
  kind: 'period' | 'region' | 'figure',
  name: string,
): CanonicalEntityReference {
  return {
    id: stableMockUUID(`${kind}:${name}`),
    name,
    disambiguationLabel: null,
  }
}

function sortChinese(values: Iterable<string>): string[] {
  return [...values].sort((left, right) => left.localeCompare(right, 'zh-CN'))
}

function prominenceForSpan(span: number): EventProminence {
  if (span >= landmarkOnlyMinimumSpan) return 1
  if (span >= importantEventsMinimumSpan) return 2
  return 3
}

function prepareEvent(
  event: HistoricalEvent,
  identities: FilterIdentityMaps,
): PreparedEvent {
  return {
    event,
    range: timeExpressionRange(event.time),
    searchableText: [
      event.title,
      event.summary,
      event.narrative,
      ...event.places,
      ...event.figures,
      ...event.topicTags,
    ]
      .filter(Boolean)
      .join('\n')
      .toLocaleLowerCase('en-US'),
    periodIDs: resolveReferenceIDs(event.periods, identities.periods),
    regionIDs: resolveReferenceIDs(event.regions, identities.regions),
    figureIDs: resolveReferenceIDs(event.figures, identities.figures),
  }
}

function createBounds(source: PreparedEvent[]): TimeRange | null {
  if (source.length === 0) return null

  let start = Number.POSITIVE_INFINITY
  let end = Number.NEGATIVE_INFINITY
  for (const prepared of source) {
    start = Math.min(start, prepared.range.start)
    end = Math.max(end, prepared.range.end)
  }
  return { start, end }
}

function normalizeSearchTerm(searchTerm?: string): string {
  return searchTerm?.trim().toLocaleLowerCase('en-US') ?? ''
}

function matchesSearch(
  prepared: PreparedEvent,
  normalizedSearch: string,
): boolean {
  if (!normalizedSearch) return true
  return prepared.searchableText.includes(normalizedSearch)
}

function matchesFilters(
  prepared: PreparedEvent,
  filters: HistoryEventQuery['filters'],
): boolean {
  if (!filters) return true

  return (
    intersectsSelection(prepared.periodIDs, filters.periods) &&
    intersectsSelection(prepared.regionIDs, filters.regions) &&
    intersectsSelection(prepared.figureIDs, filters.figures) &&
    intersectsSelection(
      [prepared.event.primaryCategory],
      filters.primaryCategories,
    )
  )
}

function createFilterIdentityMaps(
  metadata: HistoryEventFilterMetadata,
): FilterIdentityMaps {
  return {
    periods: referencesByDisplayName(
      metadata.periodGroups.flatMap((group) => group.periods),
    ),
    regions: referencesByDisplayName(metadata.regions),
    figures: referencesByDisplayName(metadata.figures),
  }
}

function referencesByDisplayName(
  references: readonly CanonicalEntityReference[],
): ReadonlyMap<string, string> {
  return new Map(references.map((reference) => [
    displayName(reference),
    reference.id,
  ]))
}

function resolveReferenceIDs(
  displayNames: readonly string[],
  identities: ReadonlyMap<string, string>,
): string[] {
  return displayNames.flatMap((name) => {
    const id = identities.get(name)
    return id ? [id] : []
  })
}

function displayName(reference: CanonicalEntityReference): string {
  return reference.disambiguationLabel
    ? `${reference.name}（${reference.disambiguationLabel}）`
    : reference.name
}

function stableMockUUID(value: string): string {
  const hashes = [0x811c9dc5, 0x9e3779b9, 0x85ebca6b, 0xc2b2ae35]
  for (const character of value) {
    const code = character.codePointAt(0) ?? 0
    for (let index = 0; index < hashes.length; index += 1) {
      hashes[index] = Math.imul(hashes[index] ^ code, 0x01000193 + index * 2)
    }
  }
  const hex = hashes
    .map((hash) => (hash >>> 0).toString(16).padStart(8, '0'))
    .join('')
    .split('')
  hex[12] = '5'
  hex[16] = ((Number.parseInt(hex[16], 16) & 0x3) | 0x8).toString(16)
  const normalized = hex.join('')
  return [
    normalized.slice(0, 8),
    normalized.slice(8, 12),
    normalized.slice(12, 16),
    normalized.slice(16, 20),
    normalized.slice(20),
  ].join('-')
}

function intersectsSelection<T>(values: T[], selected?: T[]): boolean {
  return !selected?.length || selected.some((value) => values.includes(value))
}

function assertNotAborted(signal?: AbortSignal): void {
  if (signal?.aborted) {
    throw new DOMException('请求已取消', 'AbortError')
  }
}

function wait(duration: number, signal?: AbortSignal): Promise<void> {
  assertNotAborted(signal)

  if (duration <= 0) {
    return Promise.resolve()
  }

  return new Promise((resolve, reject) => {
    const timer = window.setTimeout(() => {
      signal?.removeEventListener('abort', handleAbort)
      resolve()
    }, duration)

    const handleAbort = () => {
      window.clearTimeout(timer)
      reject(new DOMException('请求已取消', 'AbortError'))
    }

    signal?.addEventListener('abort', handleAbort, { once: true })
  })
}
