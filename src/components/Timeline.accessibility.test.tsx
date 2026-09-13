import { fireEvent, render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { vi } from 'vitest'
import { Timeline } from './Timeline'
import type { HistoricalEvent } from '../domain/history'

function eventAt(id: string, title: string, year: number): HistoricalEvent {
  return {
    id,
    title,
    summary: `${title}摘要`,
    narrative: `${title}详情`,
    time: { kind: 'year', year: { era: 'CE', year } },
    primaryCategory: '文化',
    periods: ['示例时期'],
    regions: ['示例地区'],
    places: ['示例地点'],
    figures: [],
    topicTags: [],
    prominence: 1,
    editorialPriority: 1,
  }
}

function renderTimeline(
  events: HistoricalEvent[],
  options: {
    range?: { start: number; end: number }
    selectedEventId?: string
  } = {},
) {
  const range = options.range ?? { start: 0, end: 400 }
  const onSelectEvent = vi.fn()

  render(
    <Timeline
      events={events}
      range={range}
      allRange={range}
      selectedEventId={options.selectedEventId}
      onSelectEvent={onSelectEvent}
      onRangeChange={vi.fn()}
      onViewAll={vi.fn()}
      onToday={vi.fn()}
    />,
  )

  return { onSelectEvent }
}

describe('时间线无障碍键盘路径', () => {
  it('按历史时间稳定排序，并可用方向键、Home、End 和 Enter 探索', async () => {
    const user = userEvent.setup()
    const early = eventAt('early', '早期历史事件', 50)
    const middle = eventAt('middle', '中期历史事件', 200)
    const late = eventAt('late', '晚期历史事件', 350)
    const { onSelectEvent } = renderTimeline([late, early, middle])

    const earlyButton = screen.getByRole('button', { name: /早期历史事件/ })
    const middleButton = screen.getByRole('button', { name: /中期历史事件/ })
    const lateButton = screen.getByRole('button', { name: /晚期历史事件/ })

    earlyButton.focus()
    await user.keyboard('{ArrowRight}')
    expect(middleButton).toHaveFocus()

    await user.keyboard('{ArrowDown}')
    expect(lateButton).toHaveFocus()

    await user.keyboard('{Home}')
    expect(earlyButton).toHaveFocus()

    await user.keyboard('{End}')
    expect(lateButton).toHaveFocus()

    await user.keyboard('{ArrowLeft}{Enter}')
    expect(middleButton).toHaveFocus()
    expect(onSelectEvent).toHaveBeenCalledWith(middle, middleButton)
  })

  it('点击历史事件时不会被时间线拖拽手势截获', async () => {
    const user = userEvent.setup()
    const event = eventAt('clickable', '可点击历史事件', 100)
    const { onSelectEvent } = renderTimeline([event])
    const viewport = screen.getByRole('region', {
      name: '可交互历史时间线画布',
    })
    const setPointerCapture = vi.fn()
    Object.defineProperty(viewport, 'setPointerCapture', {
      configurable: true,
      value: setPointerCapture,
    })
    const eventButton = screen.getByRole('button', {
      name: /可点击历史事件/,
    })

    fireEvent.pointerDown(eventButton, {
      pointerId: 1,
      pointerType: 'mouse',
      button: 0,
    })
    expect(setPointerCapture).not.toHaveBeenCalled()

    await user.click(eventButton)
    expect(onSelectEvent).toHaveBeenCalledWith(event, eventButton)
  })

  it('将聚合簇和最大缩放重叠列表接入同一条方向键焦点路径', async () => {
    const user = userEvent.setup()
    const events = Array.from({ length: 6 }, (_, index) => ({
      ...eventAt(`dense-${index + 1}`, `重叠历史事件${index + 1}`, 100),
      editorialPriority: index + 1,
    }))
    const { onSelectEvent } = renderTimeline(events, {
      range: { start: 99, end: 101 },
    })

    const cluster = screen.getByRole('button', {
      name: /聚合簇，6 条历史事件.*激活以展开重叠历史事件列表/,
    })
    await user.click(cluster)

    const overlapList = screen.getByRole('region', {
      name: /仍有 6 条历史事件重叠/,
    })
    const overlapButtons = within(overlapList).getAllByRole('button')

    cluster.focus()
    await user.keyboard('{ArrowDown}')
    expect(overlapButtons[0]).toHaveFocus()

    await user.keyboard('{ArrowUp}')
    expect(cluster).toHaveFocus()

    await user.keyboard('{End}{Enter}')
    expect(overlapButtons.at(-1)).toHaveFocus()
    expect(onSelectEvent).toHaveBeenCalledWith(
      events.at(-1),
      overlapButtons.at(-1),
    )
  })

  it('悬浮聚合簇时展示其中的历史事件，并可直接查看详情', async () => {
    const user = userEvent.setup()
    const events = Array.from({ length: 6 }, (_, index) => ({
      ...eventAt(`dense-${index + 1}`, `悬浮历史事件${index + 1}`, 100),
      editorialPriority: index + 1,
    }))
    const { onSelectEvent } = renderTimeline(events)
    const cluster = screen.getByRole('button', {
      name: /聚合簇，6 条历史事件/,
    })

    expect(
      screen.queryByRole('region', { name: /聚合历史事件预览/ }),
    ).not.toBeInTheDocument()

    await user.hover(cluster)

    const preview = screen.getByRole('region', {
      name: '聚合历史事件预览，共 6 条',
    })
    const eventButtons = within(preview).getAllByRole('button', {
      name: /查看历史事件详情/,
    })
    expect(eventButtons).toHaveLength(6)
    expect(within(preview).getByText('悬浮历史事件1')).toBeVisible()

    await user.click(eventButtons[0])
    expect(onSelectEvent).toHaveBeenCalledWith(events[0], eventButtons[0])
  })

  it('限制大型聚合簇的常态 DOM 数量，并用分页保持全部历史事件可达', async () => {
    const user = userEvent.setup()
    const events = Array.from({ length: 95 }, (_, index) => ({
      ...eventAt(`large-${index + 1}`, `大型聚合事件${index + 1}`, 100),
      editorialPriority: index + 1,
    }))
    renderTimeline(events, { range: { start: 99, end: 101 } })
    const cluster = screen.getByRole('button', {
      name: /聚合簇，95 条历史事件/,
    })

    await user.hover(cluster)
    const preview = screen.getByRole('region', {
      name: '聚合历史事件预览，共 95 条',
    })
    expect(within(preview).getAllByRole('button')).toHaveLength(12)
    expect(within(preview).getByText(/另有 83 条/)).toBeVisible()

    await user.click(cluster)
    const overlapList = screen.getByRole('region', {
      name: /仍有 95 条历史事件重叠/,
    })
    expect(within(overlapList).getAllByRole('listitem')).toHaveLength(40)
    expect(
      within(overlapList).getByText('第 1 页，共 3 页'),
    ).toBeVisible()

    await user.click(within(overlapList).getByRole('button', { name: '下一页' }))
    expect(within(overlapList).getAllByRole('listitem')).toHaveLength(40)
    expect(within(overlapList).getByText('大型聚合事件41')).toBeVisible()

    await user.click(within(overlapList).getByRole('button', { name: '下一页' }))
    expect(within(overlapList).getAllByRole('listitem')).toHaveLength(15)
    expect(within(overlapList).getByText('大型聚合事件95')).toBeVisible()
  })

  it('为时间线、导航控件、历史事件及选中状态提供可访问语义', () => {
    const event = eventAt('selected', '已选历史事件', 100)
    renderTimeline([event], { selectedEventId: event.id })

    const timeline = screen.getByRole('group', {
      name: /历史事件时间线，范围从公元前1年到公元400年/,
    })
    expect(timeline).toHaveAccessibleDescription(
      '方向键浏览历史事件，Enter 打开详情',
    )
    expect(
      screen.getByRole('group', { name: '时间线导航工具' }),
    ).toBeVisible()
    expect(
      screen.getByRole('region', { name: '可交互历史时间线画布' }),
    ).not.toHaveAttribute('tabindex')

    const eventButton = screen.getByRole('button', {
      name: /已选历史事件，公元100年，瞬时历史事件/,
    })
    expect(eventButton).toHaveAttribute('aria-pressed', 'true')
    expect(eventButton).toHaveAttribute(
      'aria-keyshortcuts',
      'ArrowLeft ArrowRight ArrowUp ArrowDown Home End Enter',
    )
  })
})
