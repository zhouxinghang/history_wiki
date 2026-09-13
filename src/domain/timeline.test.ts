import type { HistoricalEvent } from './history'
import {
  constrainTimelineRange,
  createTimelineTicks,
  focusTimelineCluster,
  layoutTimelineEvents,
  TIMELINE_LANE_COUNT,
} from './timeline'

describe('时间线刻度', () => {
  it('随可见跨度在年份、年代和世纪粒度之间切换', () => {
    expect(createTimelineTicks({ start: 1990, end: 2020 }).granularity).toBe('year')
    expect(createTimelineTicks({ start: 1700, end: 2020 }).granularity).toBe(
      'decade',
    )
    expect(createTimelineTicks({ start: -2600, end: 2020 }).granularity).toBe(
      'century',
    )
  })

  it('跨越纪元时不产生公元 0 年标签', () => {
    const labels = createTimelineTicks({ start: -15, end: 15 }).ticks.map(
      (tick) => tick.label,
    )

    expect(labels).not.toContain('公元0年')
    expect(labels).toContain('公元前1年')
  })
})

describe('时间线范围', () => {
  const limits = { start: -2599, end: 2030 }

  it('平移到最早历史事件之前时保持跨度并停在下限', () => {
    expect(
      constrainTimelineRange({ start: -2800, end: -2300 }, limits),
    ).toEqual({ start: -2599, end: -2099 })
  })

  it('平移到公元 2030 年之后时保持跨度并停在上限', () => {
    expect(
      constrainTimelineRange({ start: 1900, end: 2100 }, limits),
    ).toEqual({ start: 1830, end: 2030 })
  })

  it('视窗跨度超过完整范围时返回完整范围', () => {
    expect(
      constrainTimelineRange({ start: -4000, end: 3000 }, limits),
    ).toEqual(limits)
  })
})

describe('密集历史事件布局', () => {
  it('不受输入顺序影响地将可展示历史事件稳定分配到有限轨道', () => {
    const events = Array.from({ length: TIMELINE_LANE_COUNT }, (_, index) =>
      createEvent(`event-${index}`, 100, index),
    )

    const forward = layoutTimelineEvents(events, { start: 0, end: 200 }, 1200)
    const reversed = layoutTimelineEvents(
      [...events].reverse(),
      { start: 0, end: 200 },
      1200,
    )

    expect(forward.clusters).toHaveLength(0)
    expect(forward.events.map(({ event, lane }) => [event.id, lane])).toEqual(
      reversed.events.map(({ event, lane }) => [event.id, lane]),
    )
    expect(new Set(forward.events.map(({ lane }) => lane)).size).toBe(
      TIMELINE_LANE_COUNT,
    )
  })

  it('所有轨道均碰撞时按屏幕空间形成带准确数量的聚合簇', () => {
    const events = Array.from({ length: TIMELINE_LANE_COUNT + 2 }, (_, index) =>
      createEvent(`dense-${index}`, 100, index),
    )

    const presentation = layoutTimelineEvents(
      events,
      { start: 0, end: 2000 },
      1200,
    )

    expect(presentation.events).toHaveLength(0)
    expect(presentation.clusters).toHaveLength(1)
    expect(presentation.clusters[0].events).toHaveLength(events.length)
    expect(presentation.clusters[0].focusRange).toEqual({ start: 100, end: 100 })
  })

  it('随实际可用屏幕宽度变化决定是否聚合', () => {
    const events = Array.from({ length: TIMELINE_LANE_COUNT + 1 }, (_, index) =>
      createEvent(`responsive-${index}`, 300 + index * 50, index),
    )
    const range = { start: 0, end: 1000 }

    expect(layoutTimelineEvents(events, range, 1200).clusters).toHaveLength(0)
    expect(layoutTimelineEvents(events, range, 600).clusters).toHaveLength(1)
  })

  it('聚合簇聚焦范围放大并居中，且不小于最大缩放跨度', () => {
    const presentation = layoutTimelineEvents(
      Array.from({ length: 6 }, (_, index) =>
        createEvent(`dense-${index}`, 100, index),
      ),
      { start: 0, end: 2000 },
      1200,
    )

    expect(
      focusTimelineCluster(presentation.clusters[0], { start: 0, end: 2000 }),
    ).toEqual({ start: 99, end: 101 })
  })

  it('对 10,000 条同年历史事件只产出有限的可绘制节点', () => {
    const events = Array.from({ length: 10_000 }, (_, index) =>
      createEvent(`large-${index}`, 100, index),
    )

    const presentation = layoutTimelineEvents(
      events,
      { start: 99, end: 101 },
      1200,
    )

    expect(presentation.events).toHaveLength(0)
    expect(presentation.clusters).toHaveLength(1)
    expect(presentation.clusters[0].events).toHaveLength(10_000)
    expect(presentation.clusters[0].id.length).toBeLessThan(80)
  })
})

function createEvent(
  id: string,
  year: number,
  editorialPriority: number,
): HistoricalEvent {
  return {
    id,
    title: `密集事件${editorialPriority}`,
    summary: '摘要',
    narrative: '详情',
    time: { kind: 'year', year: { era: 'CE', year } },
    primaryCategory: '政治',
    periods: [],
    regions: ['示例地区'],
    places: [],
    figures: [],
    topicTags: [],
    prominence: 1,
    editorialPriority,
  }
}
