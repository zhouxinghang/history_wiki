import type {
  EventProminence,
  HistoricalEvent,
  HistoricalYear,
  PrimaryCategory,
} from '../domain/history'

const categories: PrimaryCategory[] = [
  '政治',
  '军事',
  '文化',
  '科技',
  '社会',
  '交流',
]
const regions = ['中国', '东亚', '南亚', '中东', '欧洲', '非洲', '美洲', '全球']
const periods = ['古代', '中古', '近世', '近代', '现代']
const figures = ['示例人物甲', '示例人物乙', '示例人物丙', '示例人物丁']
const topics = ['制度变迁', '技术传播', '跨文明交流', '社会生活', '战争与和平']
const minimumCoordinate = -2599
const maximumCoordinate = 2025

/**
 * 生成稳定、无随机数的万级历史事件数据，用于浏览器性能回归测试。
 * 数据覆盖完整时间轴，并保留固定数量的筛选元数据选项。
 */
export function createPerformanceEvents(count = 10_000): HistoricalEvent[] {
  const coordinateSpan = maximumCoordinate - minimumCoordinate + 1

  return Array.from({ length: count }, (_, index) => {
    const coordinate =
      index < 120
        ? 100
        : minimumCoordinate + ((index - 120) % coordinateSpan)
    const category = categories[index % categories.length]
    const region = regions[index % regions.length]
    const period = periods[Math.floor(index / regions.length) % periods.length]
    const figure = figures[index % figures.length]
    const topic = topics[index % topics.length]
    const year = coordinateToYear(coordinate)
    const prominence = prominenceForIndex(index)
    const title = `${region}${category}史事件 ${String(index + 1).padStart(5, '0')}`

    return {
      id: `10000000-0000-7000-8000-${String(index + 1).padStart(12, '0')}`,
      title,
      summary: `${title}的确定性性能测试摘要。`,
      narrative: `${title}用于验证万级数据下的筛选、拖拽、缩放和可访问交互。`,
      time:
        index % 17 === 0 && coordinate < maximumCoordinate
          ? {
              kind: 'interval',
              start: year,
              end: coordinateToYear(Math.min(maximumCoordinate, coordinate + 1)),
            }
          : index % 11 === 0
            ? { kind: 'circa', year }
            : { kind: 'year', year },
      primaryCategory: category,
      periods: [period],
      regions: [region],
      places: [`${region}示例地点`],
      figures: [figure],
      topicTags: [topic, '万级数据'],
      prominence,
      editorialPriority: index + 1,
    }
  })
}

function coordinateToYear(coordinate: number): HistoricalYear {
  return coordinate <= 0
    ? { era: 'BCE', year: 1 - coordinate }
    : { era: 'CE', year: coordinate }
}

function prominenceForIndex(index: number): EventProminence {
  if (index % 20 === 0) return 1
  if (index % 4 === 0) return 2
  return 3
}
