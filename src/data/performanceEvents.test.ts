import { createPerformanceEvents } from './performanceEvents'
import { timeExpressionRange } from '../domain/history'

describe('万级性能数据', () => {
  it('生成 10,000 条可重复且覆盖完整时间范围的历史事件', () => {
    const first = createPerformanceEvents()
    const second = createPerformanceEvents()

    expect(first).toHaveLength(10_000)
    expect(second).toEqual(first)
    expect(new Set(first.map((event) => event.id)).size).toBe(10_000)
    expect(first.every((event) =>
      /^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/.test(event.id),
    )).toBe(true)
    expect(Math.min(...first.map((event) => timeExpressionRange(event.time).start))).toBe(
      -2599,
    )
    expect(Math.max(...first.map((event) => timeExpressionRange(event.time).end))).toBe(
      2025,
    )
  })
})
