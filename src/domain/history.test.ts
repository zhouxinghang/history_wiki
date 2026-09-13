import {
  compareHistoricalEvents,
  coordinateToHistoricalYear,
  formatTimeExpression,
  historicalYearToCoordinate,
  rangesIntersect,
  timeExpressionRange,
} from './history'
import type { HistoricalEvent } from './history'
import timeCoordinateVectors from '../../testdata/time-coordinate-vectors.json'

describe('历史纪年', () => {
  it('让公元前 1 年与公元 1 年相邻且不产生公元 0 年', () => {
    expect(historicalYearToCoordinate({ era: 'BCE', year: 1 })).toBe(0)
    expect(historicalYearToCoordinate({ era: 'CE', year: 1 })).toBe(1)
    expect(coordinateToHistoricalYear(0)).toEqual({ era: 'BCE', year: 1 })
    expect(coordinateToHistoricalYear(1)).toEqual({ era: 'CE', year: 1 })
  })

  it('按精度呈现精确日期、年份、约数和区间', () => {
    expect(
      formatTimeExpression({
        kind: 'exact-date',
        date: { era: 'CE', year: 1945, month: 10, day: 24 },
      }),
    ).toBe('公元1945年10月24日')
    expect(
      formatTimeExpression({ kind: 'year', year: { era: 'BCE', year: 221 } }),
    ).toBe('公元前221年')
    expect(
      formatTimeExpression({ kind: 'circa', year: { era: 'BCE', year: 2600 } }),
    ).toBe('约公元前2600年')
    expect(
      formatTimeExpression({
        kind: 'interval',
        start: { era: 'CE', year: 618 },
        end: { era: 'CE', year: 907 },
      }),
    ).toBe('公元618年—公元907年')
  })

  it('与服务端共享同一组延伸公历精确日期坐标向量', () => {
    for (const vector of timeCoordinateVectors) {
      const range = timeExpressionRange({
        kind: 'exact-date',
        date: {
          era: vector.era as 'BCE' | 'CE',
          year: vector.year,
          month: vector.month,
          day: vector.day,
        },
      })
      expect(range.start, vector.name).toBeCloseTo(vector.coordinate, 12)
      expect(range.end, vector.name).toBe(range.start)
    }
  })

  it('拒绝无公元 0 年语义和非法延伸公历日期', () => {
    expect(() => timeExpressionRange({
      kind: 'exact-date',
      date: { era: 'CE', year: 1900, month: 2, day: 29 },
    })).toThrow('日期不符合延伸公历')
    expect(() => timeExpressionRange({
      kind: 'exact-date',
      date: { era: 'CE', year: 0, month: 1, day: 1 },
    })).toThrow('不存在公元 0 年')
  })

  it('保留与视窗边界相交的区间', () => {
    expect(rangesIntersect({ start: 100, end: 200 }, { start: 200, end: 300 })).toBe(
      true,
    )
    expect(rangesIntersect({ start: 100, end: 199 }, { start: 200, end: 300 })).toBe(
      false,
    )
  })

  it('使用稳定标识消除相同时间、编辑优先级和标题的排序歧义', () => {
    const base: HistoricalEvent = {
      id: 'b',
      title: '同名历史事件',
      summary: '',
      narrative: '',
      time: { kind: 'year', year: { era: 'CE', year: 100 } },
      primaryCategory: '政治',
      periods: [],
      regions: [],
      places: [],
      figures: [],
      topicTags: [],
      prominence: 1,
      editorialPriority: 1,
    }

    expect([{ ...base }, { ...base, id: 'a' }].sort(compareHistoricalEvents)).toEqual([
      { ...base, id: 'a' },
      base,
    ])
  })
})
