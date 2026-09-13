import {
  createExplorationUrl,
  normalizeUrlViewport,
  parseExplorationUrlState,
  validateUrlQueryCriteria,
} from './explorationUrlState'
import type { HistoryEventFilterMetadata } from './history'

const metadata: HistoryEventFilterMetadata = {
  periodGroups: [
    { context: reference('中国'), periods: [reference('东汉'), reference('唐')] },
  ],
  regions: [reference('中国'), reference('中亚')],
  figures: [reference('蔡伦'), reference('张骞')],
  primaryCategories: ['政治', '科技', '交流'],
}

function reference(name: string) {
  return { id: name, name, disambiguationLabel: null }
}

describe('探索状态 URL', () => {
  it('解析可见时间范围、关键词、全部筛选维度和选中的历史事件', () => {
    const parsed = parseExplorationUrlState(
      '?from=-220&to=280&q=%E7%BA%B8&period=%E4%B8%9C%E6%B1%89&region=%E4%B8%AD%E5%9B%BD&figure=%E8%94%A1%E4%BC%A6&category=%E7%A7%91%E6%8A%80&event=cai-lun-paper',
    )

    expect(parsed).toEqual({
      hasStateParameters: true,
      viewport: { start: -220, end: 280 },
      queryCriteria: {
        searchTerm: '纸',
        filters: {
          periods: ['东汉'],
          regions: ['中国'],
          figures: ['蔡伦'],
          primaryCategories: ['科技'],
        },
      },
      selectedEventId: 'cai-lun-paper',
    })
  })

  it('忽略非法、重复及已过期的参数并把越界范围规范到数据边界', () => {
    const parsed = parseExplorationUrlState(
      '?from=oops&to=10&period=%E4%B8%9C%E6%B1%89&period=%E4%B8%9C%E6%B1%89&period=%E5%AE%8B&region=%E6%9C%AA%E7%9F%A5&figure=%E8%94%A1%E4%BC%A6&category=%E4%B8%8D%E5%AD%98%E5%9C%A8',
    )
    const criteria = validateUrlQueryCriteria(parsed.queryCriteria, metadata)

    expect(parsed.viewport).toBeUndefined()
    expect(criteria.filters).toEqual({
      periods: ['东汉'],
      regions: [],
      figures: ['蔡伦'],
      primaryCategories: [],
    })
    expect(normalizeUrlViewport({ start: -1000, end: -999 }, { start: 0, end: 200 }, 2)).toEqual({
      start: 0,
      end: 2,
    })
  })

  it('写入规范 URL 时保留不属于探索状态的参数与锚点', () => {
    const result = createExplorationUrl(
      'https://example.test/wiki?demo=empty&from=bad#timeline',
      {
        viewport: { start: -220.12345678, end: 280 },
        queryCriteria: {
          searchTerm: '丝绸之路',
          filters: {
            periods: ['西汉'],
            regions: ['中国', '中亚'],
            figures: ['张骞'],
            primaryCategories: ['交流'],
          },
        },
        selectedEventId: 'silk-road-connections',
      },
    )
    const url = new URL(result, 'https://example.test')

    expect(url.searchParams.get('demo')).toBe('empty')
    expect(url.searchParams.get('from')).toBe('-220.123457')
    expect(url.searchParams.get('to')).toBe('280')
    expect(url.searchParams.get('q')).toBe('丝绸之路')
    expect(url.searchParams.getAll('period')).toEqual(['西汉'])
    expect(url.searchParams.getAll('region')).toEqual(['中国', '中亚'])
    expect(url.searchParams.getAll('figure')).toEqual(['张骞'])
    expect(url.searchParams.getAll('category')).toEqual(['交流'])
    expect(url.searchParams.get('event')).toBe('silk-road-connections')
    expect(url.hash).toBe('#timeline')
  })
})
