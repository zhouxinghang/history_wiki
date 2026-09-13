import { demoEvents } from './demoEvents'
import { createMockHistoryEventRepository } from './mockHistoryEventRepository'
import type { HistoricalEvent } from '../domain/history'

describe('Mock History Event Repository', () => {
  const repository = createMockHistoryEventRepository(demoEvents, { latencyMs: 0 })
  const romanEmpireID = '00000000-0000-7000-8000-000000000012'

  it('从完整数据计算约公元前 2600 年至公元 2020 年的边界', async () => {
    expect(demoEvents.every((event) =>
      /^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/.test(event.id),
    )).toBe(true)
    await expect(repository.getBounds()).resolves.toEqual({
      start: -2599,
      end: expect.closeTo(2020.19, 1),
    })
  })

  it('在数据源内部根据视窗跨度决定返回的事件显著度', async () => {
    const broad = await repository.query({
      visibleRange: { start: -3000, end: 3000 },
    })
    const medium = await repository.query({
      visibleRange: { start: -1000, end: 1000 },
    })
    const focused = await repository.query({
      visibleRange: { start: 1000, end: 1300 },
    })

    expect(broad.returnedProminence).toBe(1)
    expect(broad.sourceTotal).toBe(demoEvents.length)
    expect(broad.events.every((event) => event.prominence === 1)).toBe(true)
    expect(medium.returnedProminence).toBe(2)
    expect(medium.events.some((event) => event.prominence === 2)).toBe(true)
    expect(focused.returnedProminence).toBe(3)
    expect(focused.events.some((event) => event.prominence === 3)).toBe(true)
  })

  it('在 1200 年和 4000 年边界应用固定显著度策略', async () => {
    await expect(repository.query({
      visibleRange: { start: 0, end: 1199.999 },
    })).resolves.toMatchObject({ returnedProminence: 3 })
    await expect(repository.query({
      visibleRange: { start: 0, end: 1200 },
    })).resolves.toMatchObject({ returnedProminence: 2 })
    await expect(repository.query({
      visibleRange: { start: 0, end: 3999.999 },
    })).resolves.toMatchObject({ returnedProminence: 2 })
    await expect(repository.query({
      visibleRange: { start: 0, end: 4000 },
    })).resolves.toMatchObject({ returnedProminence: 1 })
  })

  it('空数据源不构造虚拟边界并明确标记源数据数量为零', async () => {
    const emptyRepository = createMockHistoryEventRepository([], { latencyMs: 0 })

    await expect(emptyRepository.getBounds()).resolves.toBeNull()
    await expect(
      emptyRepository.query({ visibleRange: { start: -100, end: 100 } }),
    ).resolves.toMatchObject({
      events: [],
      sourceTotal: 0,
      totalMatching: 0,
    })
  })

  it('从完整数据源返回稳定的筛选元数据并按地区语境分组历史时期', async () => {
    const metadata = await repository.getFilterMetadata()

    expect(metadata.regions.map((region) => region.name)).toEqual(
      expect.arrayContaining(['中国', '南亚', '欧洲', '全球']),
    )
    expect(metadata.figures.map((figure) => figure.name)).toEqual(
      expect.arrayContaining(['孔子', '郑和', '尼尔·阿姆斯特朗']),
    )
    expect(metadata.primaryCategories).toEqual([
      '政治',
      '军事',
      '文化',
      '科技',
      '社会',
      '交流',
    ])
    expect(metadata.periodGroups).toContainEqual(
      expect.objectContaining({
        context: expect.objectContaining({ name: '中国' }),
        periods: expect.arrayContaining([
          expect.objectContaining({ name: '秦' }),
          expect.objectContaining({ name: '唐' }),
        ]),
      }),
    )
    expect(metadata.regions.every((region) =>
      /^[0-9a-f]{8}-[0-9a-f]{4}-5[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/.test(region.id),
    )).toBe(true)
    const romanContexts = metadata.periodGroups
      .filter((group) => group.periods.some((period) => period.name === '罗马帝国'))
      .map((group) => group.context.name)
    expect(romanContexts).toEqual(['北非', '地中海', '欧洲', '西亚'])

    await repository.query({
      visibleRange: { start: 1900, end: 1930 },
      filters: { regions: ['中国'] },
    })
    await expect(repository.getFilterMetadata()).resolves.toEqual(metadata)
  })

  it('关键词覆盖标题、摘要、完整叙述、地点、历史人物和主题标签', async () => {
    const searchableEvent: HistoricalEvent = {
      ...createEvent('searchable', '标题唯一词', 100, 1),
      summary: '摘要唯一词',
      narrative: '叙述唯一词',
      places: ['地点唯一词'],
      figures: ['人物唯一词'],
      topicTags: ['主题唯一词'],
    }
    const searchableRepository = createMockHistoryEventRepository(
      [searchableEvent],
      { latencyMs: 0 },
    )

    for (const searchTerm of [
      '标题唯一',
      '摘要唯一',
      '叙述唯一',
      '地点唯一',
      '人物唯一',
      '主题唯一',
    ]) {
      const result = await searchableRepository.query({
        visibleRange: { start: 0, end: 200 },
        searchTerm,
      })
      expect(result.events.map((event) => event.id)).toEqual(['searchable'])
    }
  })

  it('忽略拉丁字符大小写', async () => {
    const latinEvent = {
      ...createEvent('latin', 'Silk Road Connections', 100, 1),
      topicTags: ['Trade'],
    }
    const latinRepository = createMockHistoryEventRepository([latinEvent], {
      latencyMs: 0,
    })

    await expect(
      latinRepository.query({
        visibleRange: { start: 0, end: 200 },
        searchTerm: 'sILK rOAD',
      }),
    ).resolves.toMatchObject({ events: [latinEvent] })
  })

  it('将关键词作为字面子串，且不搜索地区、历史时期或主分类', async () => {
    const event = {
      ...createEvent('literal', '100%_literal', 100, 1),
      periods: ['仅在时期'],
      regions: ['仅在地区'],
    }
    const literalRepository = createMockHistoryEventRepository([event], {
      latencyMs: 0,
      periodContexts: { '仅在时期': ['仅在地区'] },
    })

    await expect(literalRepository.query({
      visibleRange: { start: 0, end: 200 }, searchTerm: '%_',
    })).resolves.toMatchObject({ events: [event] })
    for (const searchTerm of ['仅在地区', '仅在时期', '政治']) {
      await expect(literalRepository.query({
        visibleRange: { start: 0, end: 200 }, searchTerm,
      })).resolves.toMatchObject({ events: [] })
    }
  })

  it('筛选条件不会改变当前时间尺度的事件显著度策略或事件固定显著度', async () => {
    const visibleRange = { start: -1000, end: 1000 }
    const unfiltered = await repository.query({ visibleRange })
    const filtered = await repository.query({
      visibleRange,
      filters: { primaryCategories: ['政治'] },
    })

    expect(unfiltered.returnedProminence).toBe(2)
    expect(filtered.returnedProminence).toBe(unfiltered.returnedProminence)
    expect(filtered.events.every((event) => event.primaryCategory === '政治')).toBe(
      true,
    )
    expect(demoEvents.find((event) => event.id === romanEmpireID)?.prominence).toBe(1)
  })

  it('查询范围与持续性历史事件存在任意交集（含边界）时返回该事件', async () => {
    const interior = await repository.query({
      visibleRange: { start: 100, end: 120 },
      searchTerm: '罗马帝国',
    })
    const touchingBoundary = await repository.query({
      visibleRange: { start: 476, end: 500 },
      searchTerm: '罗马帝国',
    })
    const outside = await repository.query({
      visibleRange: { start: 477, end: 500 },
      searchTerm: '罗马帝国',
    })

    expect(interior.events.map((event) => event.id)).toContain(romanEmpireID)
    expect(touchingBoundary.events.map((event) => event.id)).toContain(romanEmpireID)
    expect(outside.events.map((event) => event.id)).not.toContain(romanEmpireID)
  })

  it('同维度使用“或”，跨维度及关键词使用“且”', async () => {
    const metadata = await repository.getFilterMetadata()
    const regionIDs = metadata.regions
      .filter((region) => ['中国', '南亚'].includes(region.name))
      .map((region) => region.id)
    const result = await repository.query({
      visibleRange: { start: -3000, end: 3000 },
      searchTerm: '王朝',
      filters: {
        regions: regionIDs,
        primaryCategories: ['政治'],
      },
    })

    expect(result.events.length).toBeGreaterThan(0)
    expect(
      result.events.every(
        (event) =>
          event.primaryCategory === '政治' &&
          event.regions.some((region) => ['中国', '南亚'].includes(region)),
      ),
    ).toBe(true)
  })

  it('sourceTotal 不受范围与条件影响，totalMatching 在显著度过滤前计数', async () => {
    const events = [
      { ...createEvent('l1', '匹配 L1', 100, 1), prominence: 1 as const },
      { ...createEvent('l3', '匹配 L3', 100, 2), prominence: 3 as const },
      { ...createEvent('outside', '范围外', 5000, 1), prominence: 1 as const },
    ]
    const countingRepository = createMockHistoryEventRepository(events, { latencyMs: 0 })

    await expect(countingRepository.query({
      visibleRange: { start: -2000, end: 2000 },
      searchTerm: '匹配',
    })).resolves.toMatchObject({
      events: [events[0]],
      sourceTotal: 3,
      totalMatching: 2,
      returnedProminence: 1,
    })
  })

  it('在显著度过滤后允许 5,000 条完整结果，第 5,001 条明确报错', async () => {
    const denseEvents = Array.from({ length: 5_001 }, (_, index) => ({
      ...createEvent(`dense-${String(index).padStart(4, '0')}`, `密集事件 ${index}`, 100, index),
      prominence: 3 as const,
    }))
    const allowed = createMockHistoryEventRepository(denseEvents.slice(0, 5_000), {
      latencyMs: 0,
    })
    const tooLarge = createMockHistoryEventRepository(denseEvents, { latencyMs: 0 })
    const startedAt = performance.now()

    await expect(allowed.query({ visibleRange: { start: 0, end: 200 } }))
      .resolves.toMatchObject({ sourceTotal: 5_000, totalMatching: 5_000 })
    await expect(tooLarge.query({ visibleRange: { start: 0, end: 200 } }))
      .rejects.toMatchObject({ status: 422, code: 'result_set_too_large' })
    expect(performance.now() - startedAt).toBeLessThan(1_000)
  })

  it('按时间、编辑优先级、标题和稳定标识返回确定顺序', async () => {
    const events: HistoricalEvent[] = [
      createEvent('same-b', 'C', 100, 1),
      createEvent('priority-two', 'A', 100, 2),
      createEvent('title-b', 'B', 100, 1),
      createEvent('same-a', 'C', 100, 1),
      createEvent('title-a', 'A', 100, 1),
      createEvent('earlier', 'Z', 99, 3),
    ]
    const orderedRepository = createMockHistoryEventRepository(events, {
      latencyMs: 0,
    })

    const result = await orderedRepository.query({
      visibleRange: { start: 0, end: 200 },
    })

    expect(result.events.map((event) => event.id)).toEqual([
      'earlier',
      'title-a',
      'title-b',
      'same-a',
      'same-b',
      'priority-two',
    ])
  })

  it('收到取消信号后以 AbortError 结束尚未完成的查询', async () => {
    const delayedRepository = createMockHistoryEventRepository(demoEvents, {
      latencyMs: 100,
    })
    const controller = new AbortController()
    const request = delayedRepository.query({
      visibleRange: { start: -3000, end: 3000 },
      signal: controller.signal,
    })

    controller.abort()

    await expect(request).rejects.toMatchObject({ name: 'AbortError' })

    const alreadyAborted = new AbortController()
    alreadyAborted.abort()
    await expect(
      delayedRepository.query({
        visibleRange: { start: -3000, end: 3000 },
        signal: alreadyAborted.signal,
      }),
    ).rejects.toMatchObject({ name: 'AbortError' })
  })
})

function createEvent(
  id: string,
  title: string,
  year: number,
  editorialPriority: number,
): HistoricalEvent {
  return {
    id,
    title,
    summary: title,
    narrative: title,
    time: { kind: 'year', year: { era: 'CE', year } },
    primaryCategory: '政治',
    periods: [],
    regions: [],
    places: [],
    figures: [],
    topicTags: [],
    prominence: 1,
    editorialPriority,
  }
}
