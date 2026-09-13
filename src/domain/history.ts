export type Era = 'BCE' | 'CE'

export interface HistoricalYear {
  era: Era
  year: number
}

export interface ExactDateExpression {
  kind: 'exact-date'
  date: HistoricalYear & {
    month: number
    day: number
  }
}

export interface YearExpression {
  kind: 'year'
  year: HistoricalYear
}

export interface CircaExpression {
  kind: 'circa'
  year: HistoricalYear
}

export interface IntervalExpression {
  kind: 'interval'
  start: HistoricalYear & { circa?: boolean }
  end: HistoricalYear & { circa?: boolean }
}

export type TimeExpression =
  | ExactDateExpression
  | YearExpression
  | CircaExpression
  | IntervalExpression

export type EventProminence = 1 | 2 | 3

export type PrimaryCategory =
  | '政治'
  | '军事'
  | '文化'
  | '科技'
  | '社会'
  | '交流'

export interface HistoricalEvent {
  id: string
  title: string
  summary: string
  narrative: string
  time: TimeExpression
  primaryCategory: PrimaryCategory
  periods: string[]
  regions: string[]
  places: string[]
  figures: string[]
  topicTags: string[]
  prominence: EventProminence
  editorialPriority: number
}

export interface TimeRange {
  start: number
  end: number
}

export interface HistoryEventFilters {
  periods?: string[]
  regions?: string[]
  figures?: string[]
  primaryCategories?: PrimaryCategory[]
}

export interface HistoryEventQueryCriteria {
  searchTerm?: string
  filters?: HistoryEventFilters
}

export interface HistoryEventQuery extends HistoryEventQueryCriteria {
  visibleRange: TimeRange
  signal?: AbortSignal
}

export interface HistoryEventQueryResult {
  events: HistoricalEvent[]
  /** 数据源在任何时间范围、搜索或筛选生效前的历史事件总数。 */
  sourceTotal: number
  /** 当前时间范围、搜索和筛选条件匹配的历史事件总数。 */
  totalMatching: number
  returnedProminence: EventProminence
}

export const maximumHistoryEventQueryResults = 5_000

export class HistoryEventQueryTooLargeError extends Error {
  readonly status = 422
  readonly code = 'result_set_too_large'

  constructor(message = '显著度筛选后的历史事件超过 5,000 条，请缩小时间范围。') {
    super(message)
    this.name = 'HistoryEventQueryTooLargeError'
  }
}

export interface CanonicalEntityReference {
  id: string
  name: string
  disambiguationLabel: string | null
}

export interface HistoryPeriodFilterGroup {
  context: CanonicalEntityReference
  periods: CanonicalEntityReference[]
}

export interface HistoryEventFilterMetadata {
  periodGroups: HistoryPeriodFilterGroup[]
  regions: CanonicalEntityReference[]
  figures: CanonicalEntityReference[]
  primaryCategories: PrimaryCategory[]
}

export interface HistoryEventRepository {
  /** 空数据库没有真实时间边界，必须返回 null 而不是构造虚拟范围。 */
  getBounds(signal?: AbortSignal): Promise<TimeRange | null>
  getFilterMetadata(
    signal?: AbortSignal,
  ): Promise<HistoryEventFilterMetadata>
  query(query: HistoryEventQuery): Promise<HistoryEventQueryResult>
  getById(eventId: string, signal?: AbortSignal): Promise<HistoricalEvent>
}

export function historicalYearToCoordinate(value: HistoricalYear): number {
  assertHistoricalYear(value)
  return value.era === 'BCE' ? 1 - value.year : value.year
}

export function coordinateToHistoricalYear(coordinate: number): HistoricalYear {
  const rounded = Math.round(coordinate)
  return rounded <= 0
    ? { era: 'BCE', year: 1 - rounded }
    : { era: 'CE', year: rounded }
}

export function timeExpressionRange(expression: TimeExpression): TimeRange {
  if (expression.kind === 'interval') {
    return {
      start: historicalYearToCoordinate(expression.start),
      end: historicalYearToCoordinate(expression.end),
    }
  }

  const year =
    expression.kind === 'exact-date' ? expression.date : expression.year
  const base = historicalYearToCoordinate(year)

  if (expression.kind !== 'exact-date') {
    return { start: base, end: base }
  }

  assertExactDate(expression.date)
  const astronomicalYear = expression.date.era === 'BCE'
    ? 1 - expression.date.year
    : expression.date.year
  let dayOfYear = expression.date.day
  for (let month = 1; month < expression.date.month; month += 1) {
    dayOfYear += daysInMonth(astronomicalYear, month)
  }
  const coordinate = base + (dayOfYear - 1) / (isLeapYear(astronomicalYear) ? 366 : 365)
  return { start: coordinate, end: coordinate }
}

export function formatHistoricalYear(value: HistoricalYear): string {
  assertHistoricalYear(value)
  return value.era === 'BCE' ? `公元前${value.year}年` : `公元${value.year}年`
}

export function formatTimeExpression(expression: TimeExpression): string {
  switch (expression.kind) {
    case 'exact-date':
      return `${formatHistoricalYear(expression.date).slice(0, -1)}年${expression.date.month}月${expression.date.day}日`
    case 'year':
      return formatHistoricalYear(expression.year)
    case 'circa':
      return `约${formatHistoricalYear(expression.year)}`
    case 'interval':
      return `${expression.start.circa ? '约' : ''}${formatHistoricalYear(expression.start)}—${expression.end.circa ? '约' : ''}${formatHistoricalYear(expression.end)}`
  }
}

export function rangesIntersect(left: TimeRange, right: TimeRange): boolean {
  return left.start <= right.end && right.start <= left.end
}

export function compareHistoricalEvents(
  left: HistoricalEvent,
  right: HistoricalEvent,
): number {
  const leftRange = timeExpressionRange(left.time)
  const rightRange = timeExpressionRange(right.time)

  return (
    leftRange.start - rightRange.start ||
    left.editorialPriority - right.editorialPriority ||
    historicalEventTitleCollator.compare(left.title, right.title) ||
    compareStableIdentifier(left.id, right.id)
  )
}

const historicalEventTitleCollator = new Intl.Collator('zh-CN-u-co-pinyin', {
  usage: 'sort',
  sensitivity: 'variant',
})

function compareStableIdentifier(left: string, right: string): number {
  if (left === right) return 0
  return left < right ? -1 : 1
}

function assertHistoricalYear(value: HistoricalYear): void {
  if (!Number.isInteger(value.year) || value.year < 1 || value.year > 999999) {
    throw new RangeError('历史纪年必须是 1 到 999999 的整数，且不存在公元 0 年。')
  }
}

function assertExactDate(value: ExactDateExpression['date']): void {
  assertHistoricalYear(value)
  const astronomicalYear = value.era === 'BCE' ? 1 - value.year : value.year
  if (!Number.isInteger(value.month) || value.month < 1 || value.month > 12) {
    throw new RangeError('月份必须是 1 到 12 的整数。')
  }
  if (!Number.isInteger(value.day) || value.day < 1 || value.day > daysInMonth(astronomicalYear, value.month)) {
    throw new RangeError('日期不符合延伸公历。')
  }
}

function isLeapYear(year: number): boolean {
  return divisible(year, 4) && (!divisible(year, 100) || divisible(year, 400))
}

function divisible(value: number, divisor: number): boolean {
  return Math.abs(value) % divisor === 0
}

function daysInMonth(year: number, month: number): number {
  if (month === 2) return isLeapYear(year) ? 29 : 28
  return [4, 6, 9, 11].includes(month) ? 30 : 31
}
