import type {
  CanonicalEntityReference,
  HistoricalEvent,
  HistoryEventFilterMetadata,
  HistoryEventRepository,
  PrimaryCategory,
} from '../domain/history'
import { createHttpHistoryEventRepository } from './httpHistoryEventRepository'
import { createMockHistoryEventRepository } from './mockHistoryEventRepository'

const ids = {
  eventA: '00000000-0000-7000-8000-000000000019',
  eventB: '00000000-0000-7000-8000-000000000020',
  eventC: '00000000-0000-7000-8000-000000000021',
  period: '00000000-0000-7000-8000-000000000101',
  context: '00000000-0000-7000-8000-000000000102',
  region: '00000000-0000-7000-8000-000000000103',
  figure: '00000000-0000-7000-8000-000000000104',
}

const metadata: HistoryEventFilterMetadata = {
  periodGroups: [{
    context: reference(ids.context, '真实时期语境'),
    periods: [reference(ids.period, '共同历史时期', '东亚')],
  }],
  regions: [reference(ids.region, '当前地区名称')],
  figures: [reference(ids.figure, '当前人物名称')],
  primaryCategories: ['政治', '交流'],
}

const eventA: HistoricalEvent = {
  id: ids.eventA,
  title: '阿尔法历史事件',
  summary: '一条已发布事件。',
  narrative: '字面符号 100%_literal 位于正文。',
  time: {
    kind: 'interval',
    start: { era: 'BCE', year: 5 },
    end: { era: 'CE', year: 5 },
  },
  primaryCategory: '交流',
  periods: ['共同历史时期（东亚）'],
  regions: ['当前地区名称'],
  places: ['Silk Road 地点'],
  figures: ['当前人物名称'],
  topicTags: ['主题唯一词'],
  prominence: 3,
  editorialPriority: 7,
}

const eventB: HistoricalEvent = {
  ...eventA,
  id: ids.eventB,
  title: '北京事件',
  summary: '北京事件摘要',
  narrative: '北京事件正文',
  time: { kind: 'year', year: { era: 'CE', year: 20 } },
  prominence: 1,
  editorialPriority: 7,
}

const eventC: HistoricalEvent = {
  ...eventB,
  id: ids.eventC,
  title: '北京事件',
  prominence: 3,
}

const contractEvents = [eventC, eventB, eventA]

interface RepositoryFixture {
  create(events?: HistoricalEvent[]): HistoryEventRepository
}

describe.each<RepositoryFixture & { name: string }>([
  {
    name: 'Mock Repository',
    create: (events = contractEvents) => createMockHistoryEventRepository(events, {
      latencyMs: 0,
      filterMetadata: metadataForEvents(events),
    }),
  },
  {
    name: 'HTTP Repository',
    create: (events = contractEvents) => createHttpRepositoryFixture(events),
  },
])('$name 公开读取契约', ({ create }) => {
  it('返回真实边界和稳定 ID 元数据，详情不依赖当前时间查询', async () => {
    const repository = create()

    await expect(repository.getBounds()).resolves.toEqual({ start: -4, end: 20 })
    await expect(repository.getFilterMetadata()).resolves.toEqual(metadata)
    const outside = await repository.query({ visibleRange: { start: 100, end: 200 } })
    expect(outside.events).toEqual([])
    await expect(repository.getById(ids.eventA)).resolves.toEqual(eventA)
  })

  it('使用闭区间相交、稳定 ID 筛选及同维 OR/跨维 AND', async () => {
    const repository = create()
    const touching = await repository.query({
      visibleRange: { start: 5, end: 6 },
      searchTerm: 'sILK rOAD',
      filters: {
        periods: ['00000000-0000-7000-8000-000000000199', ids.period],
        regions: [ids.region],
        figures: [ids.figure],
        primaryCategories: ['政治', '交流'],
      },
    })
    expect(touching.events.map((event) => event.id)).toEqual([ids.eventA])

    const outside = await repository.query({
      visibleRange: { start: 5.000001, end: 6 },
      searchTerm: 'sILK rOAD',
      filters: { regions: [ids.region] },
    })
    expect(outside.events).toEqual([])
  })

  it('只在约定字段做字面子串搜索', async () => {
    const repository = create()
    await expect(repository.query({
      visibleRange: { start: -10, end: 10 }, searchTerm: '%_',
    })).resolves.toMatchObject({ events: [eventA] })
    for (const searchTerm of ['当前地区名称', '共同历史时期', '交流']) {
      await expect(repository.query({
        visibleRange: { start: -10, end: 10 }, searchTerm,
      })).resolves.toMatchObject({ events: [] })
    }
  })

  it('在显著度前计算 totalMatching，并按中文标题和 UUID 确定排序', async () => {
    const repository = create()
    const broad = await repository.query({ visibleRange: { start: -2000, end: 2000 } })
    expect(broad).toMatchObject({
      sourceTotal: 3,
      totalMatching: 3,
      returnedProminence: 1,
      events: [eventB],
    })

    const focused = await repository.query({ visibleRange: { start: -10, end: 30 } })
    expect(focused.events.map((event) => event.id)).toEqual([
      ids.eventA,
      ids.eventB,
      ids.eventC,
    ])
  })

  it('预取返回更宽的事件窗口，计数仍以可视范围为准', async () => {
    const repository = create()
    const result = await repository.query({
      visibleRange: { start: 15, end: 25 },
      padding: 10,
    })

    expect(result).toMatchObject({
      totalMatching: 2,
      returnedProminence: 3,
      coveredRange: { start: 5, end: 35 },
    })
    expect(result.events.map((event) => event.id)).toEqual([
      ids.eventA,
      ids.eventB,
      ids.eventC,
    ])
  })

  it('显著度过滤后第 5,001 条以稳定错误拒绝', async () => {
    const dense = Array.from({ length: 5_001 }, (_, index) => ({
      ...eventA,
      id: `10000000-0000-7000-8000-${String(index).padStart(12, '0')}`,
      title: `密集事件 ${index}`,
      time: { kind: 'year' as const, year: { era: 'CE' as const, year: 100 } },
      prominence: 3 as const,
      editorialPriority: index,
    }))
    const repository = create(dense)

    await expect(repository.query({ visibleRange: { start: 0, end: 200 } }))
      .rejects.toMatchObject({ status: 422, code: 'result_set_too_large' })
  })
})

function createHttpRepositoryFixture(events: HistoricalEvent[]): HistoryEventRepository {
  const fixtureMetadata = metadataForEvents(events)
  const backend = createMockHistoryEventRepository(events, {
    latencyMs: 0,
    filterMetadata: fixtureMetadata,
  })
  const fetch = vi.fn<typeof globalThis.fetch>(async (input) => {
    const url = new URL(String(input), 'https://history.test')
    try {
      if (url.pathname === '/api/v1/event-bounds') {
        const bounds = await backend.getBounds()
        return jsonResponse(bounds
          ? { hasEvents: true, ...bounds }
          : { hasEvents: false, start: null, end: null })
      }
      if (url.pathname === '/api/v1/event-metadata') {
        return jsonResponse(fixtureMetadata)
      }
      if (url.pathname.startsWith('/api/v1/events/')) {
        const eventID = decodeURIComponent(url.pathname.slice('/api/v1/events/'.length))
        return jsonResponse(toApiEvent(await backend.getById(eventID), fixtureMetadata))
      }
      const result = await backend.query({
        visibleRange: {
          start: Number(url.searchParams.get('from')),
          end: Number(url.searchParams.get('to')),
        },
        padding: url.searchParams.has('pad')
          ? Number(url.searchParams.get('pad'))
          : undefined,
        searchTerm: url.searchParams.get('q') ?? undefined,
        filters: {
          periods: url.searchParams.getAll('period'),
          regions: url.searchParams.getAll('region'),
          figures: url.searchParams.getAll('figure'),
          primaryCategories: url.searchParams.getAll('category') as PrimaryCategory[],
        },
      })
      return jsonResponse({
        events: result.events.map((event) => toApiEvent(event, fixtureMetadata)),
        sourceTotal: result.sourceTotal,
        totalMatching: result.totalMatching,
        returnedProminence: result.returnedProminence,
        ...(result.coveredRange
          ? {
              coveredFrom: result.coveredRange.start,
              coveredTo: result.coveredRange.end,
            }
          : {}),
      })
    } catch (error) {
      if (error instanceof Error && 'code' in error && error.code === 'result_set_too_large') {
        return new Response(JSON.stringify({
          title: '查询结果过多', detail: error.message, code: error.code,
        }), { status: 422, headers: { 'Content-Type': 'application/problem+json' } })
      }
      throw error
    }
  })
  return createHttpHistoryEventRepository({ fetch, paddingRatio: 0 })
}

function metadataForEvents(events: HistoricalEvent[]): HistoryEventFilterMetadata {
  return events === contractEvents || events.length === contractEvents.length
    ? metadata
    : { periodGroups: [], regions: [], figures: [], primaryCategories: [] }
}

function toApiEvent(event: HistoricalEvent, fixtureMetadata: HistoryEventFilterMetadata) {
  return {
    id: event.id,
    slug: event.id,
    title: event.title,
    summary: event.summary,
    narrative: event.narrative,
    time: event.time,
    primaryCategory: event.primaryCategory,
    periods: mapReferences(event.periods, fixtureMetadata.periodGroups.flatMap((group) => group.periods)),
    regions: mapReferences(event.regions, fixtureMetadata.regions),
    places: event.places.map((name, index) => reference(`20000000-0000-7000-8000-${String(index).padStart(12, '0')}`, name)),
    figures: mapReferences(event.figures, fixtureMetadata.figures),
    topicTags: event.topicTags.map((name, index) => reference(`30000000-0000-7000-8000-${String(index).padStart(12, '0')}`, name)),
    prominence: event.prominence,
    displayOrder: event.editorialPriority,
  }
}

function mapReferences(
  names: readonly string[],
  references: readonly CanonicalEntityReference[],
): CanonicalEntityReference[] {
  return names.map((name, index) => references.find((candidate) =>
    displayName(candidate) === name,
  ) ?? reference(`40000000-0000-7000-8000-${String(index).padStart(12, '0')}`, name))
}

function displayName(value: CanonicalEntityReference): string {
  return value.disambiguationLabel
    ? `${value.name}（${value.disambiguationLabel}）`
    : value.name
}

function reference(
  id: string,
  name: string,
  disambiguationLabel: string | null = null,
): CanonicalEntityReference {
  return { id, name, disambiguationLabel }
}

function jsonResponse(body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { 'Content-Type': 'application/json' },
  })
}
