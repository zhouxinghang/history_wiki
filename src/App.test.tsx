import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, vi } from 'vitest'
import App from './App'
import { demoEvents } from './data/demoEvents'
import { createMockHistoryEventRepository } from './data/mockHistoryEventRepository'
import type {
  HistoricalEvent,
  HistoryEventFilterMetadata,
  HistoryEventQuery,
  HistoryEventQueryResult,
  HistoryEventRepository,
  TimeRange,
} from './domain/history'

const pointEvent: HistoricalEvent = {
  id: 'point',
  title: '示例节点事件',
  summary: '节点摘要',
  narrative: '节点详情',
  time: { kind: 'year', year: { era: 'CE', year: 100 } },
  primaryCategory: '文化',
  periods: ['示例时期'],
  regions: ['示例地区'],
  places: ['示例地点'],
  figures: ['示例人物甲', '示例人物乙'],
  topicTags: ['示例主题', '跨文明'],
  prominence: 1,
  editorialPriority: 1,
}

const intervalEvent: HistoricalEvent = {
  ...pointEvent,
  id: 'interval',
  title: '示例区间事件',
  time: {
    kind: 'interval',
    start: { era: 'BCE', year: 2 },
    end: { era: 'CE', year: 2 },
  },
}

const populatedResult: HistoryEventQueryResult = {
  events: [intervalEvent, pointEvent],
  sourceTotal: 2,
  totalMatching: 2,
  returnedProminence: 1,
}

const centeredEvent: HistoricalEvent = {
  ...pointEvent,
  id: 'centered',
  title: '视窗中心事件',
  time: { kind: 'year', year: { era: 'CE', year: 1000 } },
}

const centeredResult: HistoryEventQueryResult = {
  events: [centeredEvent],
  sourceTotal: 1,
  totalMatching: 1,
  returnedProminence: 1,
}

const filterMetadata: HistoryEventFilterMetadata = {
  periodGroups: [
    { context: reference('示例地区'), periods: [reference('示例时期')] },
    { context: reference('另一文明'), periods: [reference('另一时期')] },
  ],
  regions: [reference('示例地区'), reference('另一地区')],
  figures: [reference('示例人物甲'), reference('示例人物乙')],
  primaryCategories: ['政治', '文化'],
}

function reference(name: string) {
  return { id: name, name, disambiguationLabel: null }
}

function createFakeRepository(): HistoryEventRepository & {
  getBounds: ReturnType<
    typeof vi.fn<(signal?: AbortSignal) => Promise<TimeRange | null>>
  >
  query: ReturnType<
    typeof vi.fn<(query: HistoryEventQuery) => Promise<HistoryEventQueryResult>>
  >
  getById: ReturnType<
    typeof vi.fn<(eventId: string, signal?: AbortSignal) => Promise<HistoricalEvent>>
  >
} {
  return {
    getBounds: vi.fn().mockResolvedValue({ start: 0, end: 2030 }),
    getFilterMetadata: vi.fn().mockResolvedValue(filterMetadata),
    query: vi.fn().mockResolvedValue(populatedResult),
    getById: vi.fn(async (eventId: string) => {
      const event = populatedResult.events.find((candidate) => candidate.id === eventId)
      if (!event) throw new Error('历史事件不存在。')
      return event
    }),
  }
}

function queryResult(
  events: HistoricalEvent[],
  returnedProminence: HistoryEventQueryResult['returnedProminence'] = 1,
): HistoryEventQueryResult {
  return {
    events,
    sourceTotal: events.length,
    totalMatching: events.length,
    returnedProminence,
  }
}

function createDenseEvents(count: number, year = 100): HistoricalEvent[] {
  return Array.from({ length: count }, (_, index) => ({
    ...pointEvent,
    id: `dense-${index + 1}`,
    title: `密集历史事件${index + 1}`,
    time: { kind: 'year' as const, year: { era: 'CE' as const, year } },
    editorialPriority: index + 1,
  }))
}

function lanePositionsFor(events: HistoricalEvent[]): Map<string, string> {
  return new Map(
    events.map((event) => {
      const button = screen.getByRole('button', {
        name: new RegExp(event.title),
      })
      return [event.id, button.closest('li')?.style.top ?? '']
    }),
  )
}

function createDeferredRepository() {
  const pending: Array<{
    resolve: (result: HistoryEventQueryResult) => void
  }> = []
  const query = vi.fn<(query: HistoryEventQuery) => Promise<HistoryEventQueryResult>>(
    () =>
      new Promise((resolve) => {
        pending.push({ resolve })
      }),
  )
  const repository: HistoryEventRepository = {
    getBounds: vi.fn().mockResolvedValue({ start: 0, end: 2030 }),
    getFilterMetadata: vi.fn().mockResolvedValue(filterMetadata),
    query,
    getById: vi.fn().mockResolvedValue(pointEvent),
  }

  return { repository, query, pending }
}

describe('历史事件总览应用', () => {
  beforeEach(() => setMobileViewport(false))

  afterEach(() => {
    vi.unstubAllGlobals()
    window.history.replaceState({}, '', '/')
  })

  it('使用服务端返回的真实时间边界作为完整时间线边界', async () => {
    const repository = createFakeRepository()
    render(<App repository={repository} />)

    expect(screen.getByRole('status')).toHaveTextContent('正在连接历史事件服务')
    expect(await screen.findByText('示例节点事件')).toBeVisible()
    expect(repository.query).toHaveBeenCalledTimes(1)

    const query = repository.query.mock.calls[0][0]
    expect(query.visibleRange).toEqual({ start: 0, end: 2030 })
    expect(query.searchTerm).toBe('')
    expect(query.filters).toEqual({
      periods: [],
      regions: [],
      figures: [],
      primaryCategories: [],
    })
    expect(query.signal).toBeInstanceOf(AbortSignal)
    expect(query.signal?.aborted).toBe(false)
    expect(query).not.toHaveProperty('prominence')
  })

  it('放大后依次呈现地标、重要和细节历史事件', async () => {
    const user = userEvent.setup()
    const repository = createMockHistoryEventRepository(demoEvents, {
      latencyMs: 0,
    })
    render(<App repository={repository} />)

    expect(await screen.findByText('当前尺度显示 L1–L1 事件')).toBeVisible()
    expect(screen.queryByText('东汉改进造纸工艺的记载')).not.toBeInTheDocument()
    expect(screen.queryByText('希波战争')).not.toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: '放大时间线' }))
    expect(await screen.findByText('当前尺度显示 L1–L2 事件')).toBeVisible()
    expect(await screen.findByText('东汉改进造纸工艺的记载')).toBeVisible()
    expect(screen.queryByText('希波战争')).not.toBeInTheDocument()

    for (let index = 0; index < 4; index += 1) {
      await user.click(screen.getByRole('button', { name: '放大时间线' }))
    }

    expect(await screen.findByText('当前尺度显示 L1–L3 事件')).toBeVisible()
    expect(await screen.findByText('希波战争')).toBeVisible()
    expect(screen.getByText('罗马帝国时期')).toBeVisible()
  })

  it('使用有限轨道稳定错层可直接展示的密集历史事件', async () => {
    const repository = createFakeRepository()
    const events = createDenseEvents(5, 1000)
    repository.query.mockResolvedValue(queryResult(events))
    const user = userEvent.setup()
    render(<App repository={repository} />)

    await screen.findByRole('button', { name: /密集历史事件1/ })
    const initialLanes = lanePositionsFor(events)

    expect(new Set(initialLanes.values()).size).toBe(5)

    await user.click(screen.getByRole('button', { name: '放大时间线' }))
    await waitFor(() => expect(repository.query).toHaveBeenCalledTimes(2))
    expect(lanePositionsFor(events)).toEqual(initialLanes)
  })

  it('激活聚合簇后放大居中，并在最大缩放通过键盘展开重叠列表', async () => {
    const repository = createFakeRepository()
    const events = createDenseEvents(6)
    repository.query.mockResolvedValue(queryResult(events))
    const user = userEvent.setup()
    render(<App repository={repository} />)

    const cluster = await screen.findByRole('button', {
      name: /聚合簇，6 条历史事件，公元100年，激活以放大并居中到对应历史区域/,
    })
    expect(cluster).toHaveTextContent('6')

    await user.click(cluster)
    await waitFor(() => expect(repository.query).toHaveBeenCalledTimes(2))
    expect(lastVisibleRange(repository)).toEqual({ start: 99, end: 101 })

    const maximumZoomCluster = screen.getByRole('button', {
      name: /聚合簇，6 条历史事件，公元100年，已达到最大缩放，激活以展开重叠历史事件列表/,
    })
    maximumZoomCluster.focus()
    await user.keyboard('{Enter}')

    const overlapList = screen.getByRole('region', {
      name: /公元100年仍有 6 条历史事件重叠/,
    })
    const overlapEventButtons = within(overlapList).getAllByRole('button', {
      name: /查看历史事件详情/,
    })
    expect(overlapEventButtons).toHaveLength(6)

    overlapEventButtons[0].focus()
    await user.keyboard('{Enter}')
    expect(screen.getByRole('dialog', { name: events[0].title })).toBeVisible()
  })

  it('快速连续改变视窗时取消旧请求并忽略其迟到响应', async () => {
    const user = userEvent.setup()
    const { repository, query, pending } = createDeferredRepository()
    const zoomedEvent = {
      ...pointEvent,
      time: { kind: 'year', year: { era: 'CE', year: 1000 } } as const,
    }
    const latestEvent = { ...zoomedEvent, id: 'latest', title: '最新视窗事件' }
    const staleEvent = { ...zoomedEvent, id: 'stale', title: '过期响应事件' }
    render(<App repository={repository} />)

    await waitFor(() => expect(query).toHaveBeenCalledTimes(1))
    await act(async () => {
      pending[0].resolve(queryResult([pointEvent]))
    })
    expect(await screen.findByText('示例节点事件')).toBeVisible()

    await user.click(screen.getByRole('button', { name: '放大时间线' }))
    await waitFor(() => expect(query).toHaveBeenCalledTimes(2))
    const staleSignal = query.mock.calls[1][0].signal
    expect(staleSignal?.aborted).toBe(false)

    await user.click(screen.getByRole('button', { name: '放大时间线' }))
    await waitFor(() => expect(query).toHaveBeenCalledTimes(3))
    expect(staleSignal?.aborted).toBe(true)

    await act(async () => {
      pending[2].resolve(queryResult([latestEvent], 3))
    })
    expect(await screen.findByText('最新视窗事件')).toBeVisible()

    await act(async () => {
      pending[1].resolve(queryResult([staleEvent], 2))
    })
    expect(screen.queryByText('过期响应事件')).not.toBeInTheDocument()
    expect(screen.getByText('最新视窗事件')).toBeVisible()
  })

  it('初次查询历史事件时使用独立的可访问加载播报', async () => {
    const repository = createFakeRepository()
    const pendingQuery = deferred<HistoryEventQueryResult>()
    repository.query.mockReturnValueOnce(pendingQuery.promise)

    render(<App repository={repository} />)

    await waitFor(() => expect(repository.query).toHaveBeenCalledTimes(1))
    expect(screen.getByRole('status')).toHaveTextContent('正在载入历史事件')
    expect(screen.getByRole('status')).toHaveTextContent(
      '正在查询当前时间范围内的历史事件',
    )

    pendingQuery.resolve(populatedResult)
    expect(await screen.findByText('示例节点事件')).toBeVisible()
  })

  it('后续查询保留已有时间线并播报更新状态', async () => {
    const repository = createFakeRepository()
    const pendingQuery = deferred<HistoryEventQueryResult>()
    repository.query.mockReturnValueOnce(Promise.resolve(centeredResult))
    repository.query.mockReturnValueOnce(pendingQuery.promise)
    const user = userEvent.setup()

    render(<App repository={repository} />)
    await screen.findByText('视窗中心事件')

    await user.click(screen.getByRole('button', { name: '放大时间线' }))

    expect(
      await screen.findByText('正在更新当前范围的历史事件…'),
    ).toBeVisible()
    expect(screen.getByText('视窗中心事件')).toBeVisible()

    pendingQuery.resolve(centeredResult)
    await waitFor(() =>
      expect(
        screen.queryByText('正在更新当前范围的历史事件…'),
      ).not.toBeInTheDocument(),
    )
  })

  it('以可访问名称区分瞬时节点与持续性区间', async () => {
    render(<App repository={createFakeRepository()} />)

    expect(
      await screen.findByRole('button', {
        name: /示例节点事件.*公元100年.*瞬时历史事件/,
      }),
    ).toBeVisible()
    expect(
      screen.getByRole('button', {
        name: /示例区间事件.*公元前2年—公元2年.*持续性历史事件/,
      }),
    ).toBeVisible()
  })

  it('桌面端点击历史事件后在右侧面板展示完整字段，并以 Escape 关闭及返回焦点', async () => {
    const user = userEvent.setup()
    render(<App repository={createFakeRepository()} />)

    const trigger = await screen.findByRole('button', {
      name: /示例节点事件.*瞬时历史事件/,
    })
    await user.click(trigger)

    const dialog = screen.getByRole('dialog', { name: '示例节点事件' })
    const detail = within(dialog)
    expect(dialog).not.toHaveAttribute('aria-modal')
    expect(dialog.parentElement).toHaveClass('event-detail-layer--desktop')
    expect(detail.getByText('公元100年')).toBeVisible()
    expect(detail.getByText('节点摘要')).toBeVisible()
    expect(detail.getByText('节点详情')).toBeVisible()
    expect(detail.getByText('主分类')).toBeVisible()
    expect(detail.getByText('文化')).toBeVisible()
    expect(detail.getByText('历史时期')).toBeVisible()
    expect(detail.getByText('示例时期')).toBeVisible()
    expect(detail.getByText('地区')).toBeVisible()
    expect(detail.getByText('示例地区')).toBeVisible()
    expect(detail.getByText('地点')).toBeVisible()
    expect(detail.getByText('示例地点')).toBeVisible()
    expect(detail.getByText('历史人物')).toBeVisible()
    expect(detail.getByText('示例人物甲、示例人物乙')).toBeVisible()
    expect(detail.getByText('主题标签')).toBeVisible()
    expect(detail.getByText('示例主题、跨文明')).toBeVisible()
    expect(detail.queryByText('产品演示数据')).not.toBeInTheDocument()
    expect(detail.queryByText(/不作为专业史学引文/)).not.toBeInTheDocument()
    await waitFor(() => expect(detail.getByRole('heading', { level: 2 })).toHaveFocus())

    await user.keyboard('{Escape}')

    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    await waitFor(() => expect(trigger).toHaveFocus())
  })

  it('桌面端点击时间线空白区域时关闭历史事件详情', async () => {
    const user = userEvent.setup()
    const historyBack = vi.spyOn(window.history, 'back')
    render(<App repository={createFakeRepository()} />)

    const trigger = await screen.findByRole('button', {
      name: /示例节点事件.*瞬时历史事件/,
    })
    await user.click(trigger)
    expect(screen.getByRole('dialog', { name: '示例节点事件' })).toBeVisible()

    await user.click(
      screen.getByRole('region', { name: '可交互历史时间线画布' }),
    )

    await waitFor(() =>
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument(),
    )
    expect(historyBack).not.toHaveBeenCalled()
    expect(new URLSearchParams(window.location.search).get('event')).toBeNull()
  })

  it('聚焦历史事件时可按 Enter 打开详情', async () => {
    const user = userEvent.setup()
    render(<App repository={createFakeRepository()} />)

    const trigger = await screen.findByRole('button', {
      name: /示例节点事件.*瞬时历史事件/,
    })
    trigger.focus()
    await user.keyboard('{Enter}')

    expect(screen.getByRole('dialog', { name: '示例节点事件' })).toBeVisible()
  })

  it('移动端使用模态底部抽屉、遮罩与明确操作，并将焦点限制在详情内', async () => {
    setMobileViewport(true)
    const user = userEvent.setup()
    render(<App repository={createFakeRepository()} />)

    const trigger = await screen.findByRole('button', {
      name: /示例节点事件.*瞬时历史事件/,
    })
    await user.click(trigger)

    const dialog = screen.getByRole('dialog', { name: '示例节点事件' })
    const closeButton = within(dialog).getByRole('button', {
      name: '关闭历史事件详情',
    })
    const returnButton = within(dialog).getByRole('button', {
      name: '返回时间线',
    })
    const backdrop = document.querySelector<HTMLButtonElement>(
      '.event-detail-backdrop',
    )

    expect(dialog).toHaveAttribute('aria-modal', 'true')
    expect(dialog.parentElement).toHaveClass('event-detail-layer--mobile')
    expect(backdrop).toBeInTheDocument()
    expect(document.querySelector('.app-shell')).toHaveAttribute('inert')
    expect(document.querySelector('.app-shell')).toHaveAttribute('aria-hidden', 'true')
    expect(document.body.style.overflow).toBe('hidden')

    await waitFor(() => expect(closeButton).toHaveFocus())
    await user.keyboard('{Shift>}{Tab}{/Shift}')
    expect(returnButton).toHaveFocus()
    await user.tab()
    expect(closeButton).toHaveFocus()

    await user.click(returnButton)
    await waitFor(() => expect(trigger).toHaveFocus())
    expect(document.body.style.overflow).toBe('')

    await user.click(trigger)
    await user.click(
      document.querySelector<HTMLButtonElement>('.event-detail-backdrop')!,
    )
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    await waitFor(() => expect(trigger).toHaveFocus())
  })

  it('数据读取失败时允许重试', async () => {
    const repository = createFakeRepository()
    vi.mocked(repository.getBounds)
      .mockRejectedValueOnce(new Error('网络暂不可用'))
      .mockResolvedValueOnce({ start: 0, end: 200 })
    const user = userEvent.setup()

    render(<App repository={repository} />)
    expect(await screen.findByRole('alert')).toHaveTextContent('网络暂不可用')

    await user.click(screen.getByRole('button', { name: '重新读取' }))
    expect(await screen.findByText('示例节点事件')).toBeVisible()
  })

  it('历史事件查询失败时可单独重试并恢复时间线', async () => {
    const repository = createFakeRepository()
    repository.query
      .mockRejectedValueOnce(new Error('查询服务繁忙'))
      .mockResolvedValueOnce(populatedResult)
    const user = userEvent.setup()

    render(<App repository={repository} />)

    expect(await screen.findByRole('alert')).toHaveTextContent(
      '暂时无法查询历史事件',
    )
    expect(screen.getByRole('alert')).toHaveTextContent('查询服务繁忙')

    await user.click(screen.getByRole('button', { name: '重新读取' }))

    expect(await screen.findByText('示例节点事件')).toBeVisible()
    expect(repository.getBounds).toHaveBeenCalledTimes(1)
    expect(repository.query).toHaveBeenCalledTimes(2)
  })

  it('后续查询失败时保留已有结果并可重试当前范围', async () => {
    const repository = createFakeRepository()
    repository.query
      .mockResolvedValueOnce(centeredResult)
      .mockRejectedValueOnce(new Error('当前范围查询超时'))
      .mockResolvedValueOnce(centeredResult)
    const user = userEvent.setup()

    render(<App repository={repository} />)
    await screen.findByText('视窗中心事件')

    await user.click(screen.getByRole('button', { name: '放大时间线' }))

    expect(await screen.findByRole('alert')).toHaveTextContent(
      '更新当前范围失败：当前范围查询超时',
    )
    expect(screen.getByText('视窗中心事件')).toBeVisible()

    await user.click(screen.getByRole('button', { name: '重试' }))

    await waitFor(() => expect(repository.query).toHaveBeenCalledTimes(3))
    await waitFor(() => expect(screen.queryByRole('alert')).not.toBeInTheDocument())
  })

  it('数据源为空时仅提供重新读取数据源的恢复操作', async () => {
    const repository = createFakeRepository()
    repository.query
      .mockResolvedValueOnce({
        events: [],
        sourceTotal: 0,
        totalMatching: 0,
        returnedProminence: 3,
      })
      .mockResolvedValueOnce(populatedResult)
    const user = userEvent.setup()

    render(<App repository={repository} />)

    expect(
      await screen.findByText('还没有已发布的历史事件'),
    ).toBeVisible()
    expect(
      screen.queryByRole('button', { name: '清空搜索与筛选' }),
    ).not.toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: '重新读取历史事件' }))

    expect(await screen.findByText('示例节点事件')).toBeVisible()
    expect(repository.getBounds).toHaveBeenCalledTimes(2)
  })

  it('空数据库不查询虚构时间范围并展示真实空状态', async () => {
    const repository = createFakeRepository()
    repository.getBounds.mockResolvedValue(null)

    render(<App repository={repository} />)

    expect(await screen.findByText('还没有已发布的历史事件')).toBeVisible()
    expect(
      screen.getByText('数据库当前为空。可以稍后重新读取，查看是否已有历史事件。'),
    ).toBeVisible()
    expect(screen.getByText('暂无已发布历史事件')).toBeVisible()
    expect(repository.query).not.toHaveBeenCalled()
    expect(
      screen.queryByRole('region', { name: '可交互历史时间线画布' }),
    ).not.toBeInTheDocument()
  })

  it('默认通过 HTTP Repository 读取空数据库而不是加载演示事件', async () => {
    const fetch = vi.fn<typeof globalThis.fetch>(async (input) => {
      const pathname = new URL(String(input), 'http://localhost').pathname
      if (pathname === '/api/v1/event-bounds') {
        return new Response(
          JSON.stringify({ hasEvents: false, start: null, end: null }),
          { status: 200, headers: { 'Content-Type': 'application/json' } },
        )
      }
      if (pathname === '/api/v1/event-metadata') {
        return new Response(
          JSON.stringify({
            periodGroups: [],
            regions: [],
            figures: [],
            primaryCategories: [],
          }),
          { status: 200, headers: { 'Content-Type': 'application/json' } },
        )
      }
      throw new Error(`unexpected request: ${pathname}`)
    })
    vi.stubGlobal('fetch', fetch)

    render(<App />)

    expect(await screen.findByText('还没有已发布的历史事件')).toBeVisible()
    expect(fetch).toHaveBeenCalledTimes(2)
    expect(screen.queryByText('夏王朝的传统纪年')).not.toBeInTheDocument()
  })

  it('查询条件无结果时可直接清空条件并重新查询', async () => {
    const repository = createFakeRepository()
    repository.query.mockImplementation(async (query) =>
      query.filters?.regions?.length
        ? {
            events: [],
            sourceTotal: 2,
            totalMatching: 0,
            returnedProminence: 1,
          }
        : populatedResult,
    )
    const user = userEvent.setup()

    render(
      <App
        repository={repository}
        initialQueryCriteria={{ filters: { regions: ['未收录地区'] } }}
      />,
    )

    expect(
      await screen.findByText('没有符合当前条件的历史事件'),
    ).toBeVisible()

    await user.click(screen.getByRole('button', { name: '清空搜索与筛选' }))

    expect(await screen.findByText('示例节点事件')).toBeVisible()
    expect(repository.query).toHaveBeenCalledTimes(2)
    expect(repository.query.mock.calls[1][0].filters).toEqual({
      periods: [],
      regions: [],
      figures: [],
      primaryCategories: [],
    })
  })

  it('在桌面筛选侧栏中实时传递关键词、同维或选项与跨维且条件', async () => {
    const user = userEvent.setup()
    const events: HistoricalEvent[] = [
      {
        ...pointEvent,
        id: 'east-trade',
        title: '东方贸易节点',
        regions: ['示例地区'],
        figures: ['示例人物甲'],
        primaryCategory: '文化',
      },
      {
        ...pointEvent,
        id: 'south-trade',
        title: '南方贸易节点',
        periods: ['另一时期'],
        regions: ['另一地区'],
        figures: ['示例人物乙'],
        primaryCategory: '文化',
      },
      {
        ...pointEvent,
        id: 'politics',
        title: '政治节点',
        regions: ['示例地区'],
        primaryCategory: '政治',
      },
    ]
    const repository = createMockHistoryEventRepository(events, { latencyMs: 0 })
    const filterMetadata = await repository.getFilterMetadata()
    const selectedRegionIDs = ['示例地区', '另一地区'].map((name) => {
      const region = filterMetadata.regions.find((candidate) => candidate.name === name)
      if (!region) throw new Error(`missing test region: ${name}`)
      return region.id
    })
    const query = vi.spyOn(repository, 'query')

    render(<App repository={repository} />)
    expect(await screen.findByText('东方贸易节点')).toBeVisible()

    await user.type(screen.getByRole('searchbox', { name: '关键词' }), '贸易')
    expect(await screen.findByText('南方贸易节点')).toBeVisible()
    expect(screen.queryByText('政治节点')).not.toBeInTheDocument()

    await user.click(screen.getByRole('checkbox', { name: '示例地区' }))
    await waitFor(() =>
      expect(screen.queryByText('南方贸易节点')).not.toBeInTheDocument(),
    )
    await user.click(screen.getByRole('checkbox', { name: '另一地区' }))
    expect(await screen.findByText('南方贸易节点')).toBeVisible()

    await user.click(screen.getByRole('checkbox', { name: '文化' }))
    await waitFor(() => {
      const latestQuery = query.mock.calls.at(-1)?.[0]
      expect(latestQuery).toMatchObject({
        searchTerm: '贸易',
        filters: {
          periods: [],
          regions: selectedRegionIDs,
          figures: [],
          primaryCategories: ['文化'],
        },
      })
    })
    expect(screen.getByText('东方贸易节点')).toBeVisible()
    expect(screen.getByText('南方贸易节点')).toBeVisible()
  })

  it('通过筛选交互进入无结果状态后可一键清空并恢复', async () => {
    const user = userEvent.setup()
    const repository = createMockHistoryEventRepository([pointEvent], {
      latencyMs: 0,
    })
    const query = vi.spyOn(repository, 'query')
    render(<App repository={repository} />)

    expect(await screen.findByText('示例节点事件')).toBeVisible()
    const search = screen.getByRole('searchbox', { name: '关键词' })
    await user.type(search, '完全不存在')
    expect(
      await screen.findByText('没有符合当前条件的历史事件'),
    ).toBeVisible()

    await user.click(screen.getByRole('button', { name: '清空搜索与筛选' }))

    expect(await screen.findByText('示例节点事件')).toBeVisible()
    expect(search).toHaveValue('')
    expect(query.mock.calls.at(-1)?.[0]).toMatchObject({
      searchTerm: '',
      filters: {
        periods: [],
        regions: [],
        figures: [],
        primaryCategories: [],
      },
    })
  })

  it('移动端在可恢复焦点的模态底部抽屉中提供搜索与筛选', async () => {
    setMobileViewport(true)
    const user = userEvent.setup()
    render(<App repository={createFakeRepository()} />)
    await screen.findByText('示例节点事件')

    const trigger = screen.getByRole('button', { name: /搜索与筛选/ })
    await user.click(trigger)

    const dialog = screen.getByRole('dialog', { name: '搜索与筛选' })
    expect(dialog).toHaveClass('filter-drawer')
    expect(dialog).toHaveAttribute('aria-modal', 'true')
    expect(within(dialog).getByRole('searchbox', { name: '关键词' })).toBeVisible()
    expect(document.querySelector('.app-shell')).toHaveAttribute('inert')
    expect(document.body.style.overflow).toBe('hidden')

    const closeButton = within(dialog).getByRole('button', { name: '完成' })
    await waitFor(() => expect(closeButton).toHaveFocus())
    await user.keyboard('{Shift>}{Tab}{/Shift}')
    expect(dialog).toContainElement(document.activeElement as HTMLElement)
    await user.tab()
    expect(closeButton).toHaveFocus()

    await user.click(closeButton)
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    await waitFor(() => expect(trigger).toHaveFocus())
    expect(document.body.style.overflow).toBe('')
  })

  it('通过 demo URL 参数直接预览持续加载状态', () => {
    window.history.replaceState({}, '', '/?demo=loading')

    render(<App />)

    expect(screen.getByRole('status')).toHaveTextContent('正在连接历史事件服务')
  })

  it('通过 demo URL 参数直接预览错误状态', async () => {
    window.history.replaceState({}, '', '/?demo=error')

    render(<App />)

    expect(await screen.findByRole('alert')).toHaveTextContent(
      '演示数据服务暂时不可用',
    )
  })

  it('通过 demo URL 参数直接预览数据源空状态', async () => {
    window.history.replaceState({}, '', '/?demo=empty')

    render(<App />)

    expect(
      await screen.findByText('还没有已发布的历史事件'),
    ).toBeVisible()
  })

  it('桌面端滚轮以指针对应的历史时间为锚点缩放', async () => {
    const repository = createFakeRepository()
    render(<App repository={repository} />)
    await screen.findByText('示例节点事件')

    const viewport = screen.getByRole('region', {
      name: '可交互历史时间线画布',
    })
    mockViewportBounds(viewport)
    fireEvent.wheel(viewport, { clientX: 250, deltaY: -100 })

    await waitFor(() => expect(repository.query).toHaveBeenCalledTimes(2))
    const range = lastVisibleRange(repository)
    const span = range.end - range.start

    expect(span).toBeCloseTo(1624)
    expect(range.start + span * 0.25).toBeCloseTo(507.5)
  })

  it('缩放按钮、查看全部和回到今天可调整视窗', async () => {
    const repository = createFakeRepository()
    const user = userEvent.setup()
    const yearSpy = vi.spyOn(Date.prototype, 'getFullYear').mockReturnValue(2042)
    render(<App repository={repository} />)
    await screen.findByText('示例节点事件')

    await user.click(screen.getByRole('button', { name: '放大时间线' }))
    await waitFor(() => expect(repository.query).toHaveBeenCalledTimes(2))
    expect(lastVisibleRange(repository).end - lastVisibleRange(repository).start).toBeCloseTo(
      1461.6,
    )

    await user.click(screen.getByRole('button', { name: '缩小时间线' }))
    await waitFor(() => expect(repository.query).toHaveBeenCalledTimes(3))
    expect(lastVisibleRange(repository)).toEqual({ start: 0, end: 2030 })

    await user.click(screen.getByRole('button', { name: '放大时间线' }))
    await waitFor(() => expect(repository.query).toHaveBeenCalledTimes(4))

    await user.click(screen.getByRole('button', { name: '查看全部' }))
    await waitFor(() => expect(repository.query).toHaveBeenCalledTimes(5))
    expect(lastVisibleRange(repository)).toEqual({ start: 0, end: 2030 })

    await user.click(screen.getByRole('button', { name: '回到今天' }))
    await waitFor(() => expect(repository.query).toHaveBeenCalledTimes(6))
    const todayRange = lastVisibleRange(repository)
    expect(todayRange).toEqual({ start: 1910, end: 2030 })
    expect(todayRange.end - todayRange.start).toBe(120)
    yearSpy.mockRestore()
  })

  it('桌面拖拽和移动端双指手势分别支持平移与缩放', async () => {
    const repository = createFakeRepository()
    const user = userEvent.setup()
    render(<App repository={repository} />)
    await screen.findByText('示例节点事件')

    const viewport = screen.getByRole('region', {
      name: '可交互历史时间线画布',
    })
    mockViewportBounds(viewport)

    await user.click(screen.getByRole('button', { name: '放大时间线' }))
    await waitFor(() => expect(repository.query).toHaveBeenCalledTimes(2))

    fireEvent.pointerDown(viewport, {
      pointerId: 1,
      pointerType: 'mouse',
      button: 0,
      clientX: 500,
    })
    fireEvent.pointerMove(viewport, {
      pointerId: 1,
      pointerType: 'mouse',
      clientX: 900,
    })
    fireEvent.pointerUp(viewport, {
      pointerId: 1,
      pointerType: 'mouse',
      clientX: 900,
    })

    await waitFor(() => expect(repository.query).toHaveBeenCalledTimes(3))
    expect(lastVisibleRange(repository).start).toBe(0)
    expect(lastVisibleRange(repository).end).toBeCloseTo(1461.6)

    await user.click(screen.getByRole('button', { name: '查看全部' }))
    await waitFor(() => expect(repository.query).toHaveBeenCalledTimes(4))

    await user.click(screen.getByRole('button', { name: '放大时间线' }))
    await waitFor(() => expect(repository.query).toHaveBeenCalledTimes(5))

    fireEvent.pointerDown(viewport, {
      pointerId: 10,
      pointerType: 'touch',
      clientX: 400,
    })
    fireEvent.pointerMove(viewport, {
      pointerId: 10,
      pointerType: 'touch',
      clientX: 100,
    })
    fireEvent.pointerUp(viewport, {
      pointerId: 10,
      pointerType: 'touch',
      clientX: 100,
    })

    await waitFor(() => expect(repository.query).toHaveBeenCalledTimes(6))
    expect(lastVisibleRange(repository).start).toBeCloseTo(568.4)
    expect(lastVisibleRange(repository).end).toBe(2030)

    await user.click(screen.getByRole('button', { name: '查看全部' }))
    await waitFor(() => expect(repository.query).toHaveBeenCalledTimes(7))

    fireEvent.pointerDown(viewport, {
      pointerId: 11,
      pointerType: 'touch',
      clientX: 300,
    })
    fireEvent.pointerDown(viewport, {
      pointerId: 12,
      pointerType: 'touch',
      clientX: 700,
    })
    fireEvent.pointerMove(viewport, {
      pointerId: 12,
      pointerType: 'touch',
      clientX: 900,
    })
    fireEvent.pointerUp(viewport, {
      pointerId: 12,
      pointerType: 'touch',
      clientX: 900,
    })
    fireEvent.pointerUp(viewport, {
      pointerId: 11,
      pointerType: 'touch',
      clientX: 300,
    })

    await waitFor(() => expect(repository.query).toHaveBeenCalledTimes(8))
    const pinchedRange = lastVisibleRange(repository)
    expect(pinchedRange.end - pinchedRange.start).toBeCloseTo(2030 * (400 / 600))
  })

  it('直接打开分享 URL 时还原视窗、关键词、全部筛选维度和历史事件详情', async () => {
    window.history.replaceState(
      {},
      '',
      '/?from=90&to=110&q=%E7%A4%BA%E4%BE%8B&period=%E7%A4%BA%E4%BE%8B%E6%97%B6%E6%9C%9F&region=%E7%A4%BA%E4%BE%8B%E5%9C%B0%E5%8C%BA&figure=%E7%A4%BA%E4%BE%8B%E4%BA%BA%E7%89%A9%E7%94%B2&category=%E6%96%87%E5%8C%96&event=point',
    )
    const repository = createFakeRepository()

    render(<App repository={repository} />)

    expect(
      await screen.findByRole('dialog', { name: '示例节点事件' }),
    ).toBeVisible()
    expect(repository.query).toHaveBeenCalledWith(
      expect.objectContaining({
        visibleRange: { start: 90, end: 110 },
        searchTerm: '示例',
        filters: {
          periods: ['示例时期'],
          regions: ['示例地区'],
          figures: ['示例人物甲'],
          primaryCategories: ['文化'],
        },
      }),
    )
    expect(screen.getByRole('searchbox', { name: '关键词' })).toHaveValue('示例')
    expect(screen.getByRole('checkbox', { name: '示例时期' })).toBeChecked()
    expect(screen.getByRole('checkbox', { name: '示例地区' })).toBeChecked()
    expect(screen.getByRole('checkbox', { name: '示例人物甲' })).toBeChecked()
    expect(screen.getByRole('checkbox', { name: '文化' })).toBeChecked()
  })

  it('搜索、组合筛选、缩放和打开详情会持续写入可分享 URL', async () => {
    const user = userEvent.setup()
    const repository = createFakeRepository()
    render(<App repository={repository} />)
    await screen.findByText('示例节点事件')

    await user.type(screen.getByRole('searchbox', { name: '关键词' }), '节点')
    await user.click(screen.getByRole('checkbox', { name: '示例时期' }))
    await user.click(screen.getByRole('checkbox', { name: '示例地区' }))
    await user.click(screen.getByRole('checkbox', { name: '示例人物甲' }))
    await user.click(screen.getByRole('checkbox', { name: '文化' }))
    await user.click(
      screen.getByRole('button', {
        name: /示例节点事件.*瞬时历史事件/,
      }),
    )
    await user.click(screen.getByRole('button', { name: '放大时间线' }))
    await waitFor(() => expect(repository.query).toHaveBeenCalledTimes(8))

    const parameters = new URLSearchParams(window.location.search)
    expect(Number(parameters.get('from'))).toBeCloseTo(284.2)
    expect(Number(parameters.get('to'))).toBeCloseTo(1745.8)
    expect(parameters.get('q')).toBe('节点')
    expect(parameters.getAll('period')).toEqual(['示例时期'])
    expect(parameters.getAll('region')).toEqual(['示例地区'])
    expect(parameters.getAll('figure')).toEqual(['示例人物甲'])
    expect(parameters.getAll('category')).toEqual(['文化'])
    expect(parameters.get('event')).toBe('point')
  })

  it('连续缩放和详情开关只替换当前记录，避免浏览器历史跳转', async () => {
    const user = userEvent.setup()
    const repository = createFakeRepository()
    repository.query.mockResolvedValue(centeredResult)
    const pushState = vi.spyOn(window.history, 'pushState')
    const replaceState = vi.spyOn(window.history, 'replaceState')
    const historyBack = vi.spyOn(window.history, 'back')
    render(<App repository={repository} />)
    await screen.findByText('视窗中心事件')

    await user.click(screen.getByRole('button', { name: '放大时间线' }))
    await user.click(screen.getByRole('button', { name: '放大时间线' }))
    await waitFor(() => expect(repository.query).toHaveBeenCalledTimes(3))
    expect(pushState).not.toHaveBeenCalled()
    expect(replaceState.mock.calls.length).toBeGreaterThanOrEqual(3)

    const trigger = screen.getByRole('button', {
      name: /视窗中心事件.*瞬时历史事件/,
    })
    await user.click(trigger)
    expect(pushState).not.toHaveBeenCalled()
    expect(new URLSearchParams(window.location.search).get('event')).toBe(
      'centered',
    )

    await user.click(
      screen.getByRole('region', { name: '可交互历史时间线画布' }),
    )
    await waitFor(() =>
      expect(screen.queryByRole('dialog', { name: '视窗中心事件' })).not.toBeInTheDocument(),
    )
    expect(new URLSearchParams(window.location.search).get('event')).toBeNull()
    expect(historyBack).not.toHaveBeenCalled()
    expect(replaceState.mock.calls.length).toBeGreaterThanOrEqual(5)
  })

  it('通过独立详情查询恢复不在当前时间线结果中的 URL 深链', async () => {
    const deepLinkedEvent: HistoricalEvent = {
      ...pointEvent,
      id: 'outside-current-query',
      title: '视窗外的已发布事件',
      places: ['地点甲', '地点乙'],
      time: { kind: 'year', year: { era: 'CE', year: 1900 } },
    }
    window.history.replaceState(
      {},
      '',
      '/?from=0&to=200&event=outside-current-query',
    )
    const repository = createFakeRepository()
    repository.query.mockResolvedValue(queryResult([pointEvent]))
    repository.getById.mockResolvedValue(deepLinkedEvent)

    render(<App repository={repository} />)

    expect(
      await screen.findByRole('dialog', { name: '视窗外的已发布事件' }),
    ).toBeVisible()
    expect(repository.getById).toHaveBeenCalledWith(
      'outside-current-query',
      expect.any(AbortSignal),
    )
    expect(screen.getByText('地点甲、地点乙')).toBeVisible()
    expect(new URLSearchParams(window.location.search).get('event')).toBe(
      'outside-current-query',
    )
  })

  it('非法或过期 URL 参数会被安全忽略并规范化', async () => {
    window.history.replaceState(
      {},
      '',
      '/?demo=custom&from=oops&to=NaN&period=%E5%B7%B2%E8%BF%87%E6%9C%9F&region=%E6%9C%AA%E7%9F%A5&figure=%E4%B8%8D%E5%AD%98%E5%9C%A8&category=invalid&event=missing',
    )
    const repository = createFakeRepository()

    render(<App repository={repository} />)

    expect(await screen.findByText('示例节点事件')).toBeVisible()
    await waitFor(() =>
      expect(new URLSearchParams(window.location.search).get('event')).toBeNull(),
    )
    expect(repository.query.mock.calls[0][0]).toMatchObject({
      visibleRange: { start: 0, end: 2030 },
      searchTerm: '',
      filters: {
        periods: [],
        regions: [],
        figures: [],
        primaryCategories: [],
      },
    })
    const parameters = new URLSearchParams(window.location.search)
    expect(parameters.get('demo')).toBe('custom')
    expect(parameters.get('from')).toBe('0')
    expect(parameters.get('to')).toBe('2030')
    expect(parameters.has('period')).toBe(false)
    expect(parameters.has('region')).toBe(false)
    expect(parameters.has('figure')).toBe(false)
    expect(parameters.has('category')).toBe(false)
  })

  it('将历史时期与其地区语境共同显示', async () => {
    render(<App repository={createFakeRepository()} />)

    expect(
      await screen.findByLabelText('按地区或文明语境显示的历史时期'),
    ).toHaveTextContent('示例地区 · 示例时期')
  })
})

function lastVisibleRange(repository: ReturnType<typeof createFakeRepository>) {
  return repository.query.mock.calls.at(-1)![0].visibleRange
}

function mockViewportBounds(element: HTMLElement) {
  vi.spyOn(element, 'getBoundingClientRect').mockReturnValue({
    x: 0,
    y: 0,
    left: 0,
    top: 0,
    right: 1000,
    bottom: 602,
    width: 1000,
    height: 602,
    toJSON: () => ({}),
  })
}

function setMobileViewport(matches: boolean) {
  vi.stubGlobal(
    'matchMedia',
    vi.fn().mockImplementation(
      (query: string): MediaQueryList =>
        ({
          matches,
          media: query,
          onchange: null,
          addEventListener: vi.fn(),
          removeEventListener: vi.fn(),
          addListener: vi.fn(),
          removeListener: vi.fn(),
          dispatchEvent: vi.fn(),
        }) as MediaQueryList,
    ),
  )
}

function deferred<T>() {
  let resolve!: (value: T | PromiseLike<T>) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise
    reject = rejectPromise
  })

  return { promise, resolve, reject }
}
