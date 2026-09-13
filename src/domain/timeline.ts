import {
  compareHistoricalEvents,
  coordinateToHistoricalYear,
  formatHistoricalYear,
  timeExpressionRange,
  type HistoricalEvent,
  type TimeRange,
} from './history'

export type TimelineTickGranularity = 'year' | 'decade' | 'century'

export interface TimelineTick {
  coordinate: number
  label: string
}

export interface TimelineTicks {
  granularity: TimelineTickGranularity
  ticks: TimelineTick[]
}

export interface TimelineEventLayout {
  event: HistoricalEvent
  x: number
  startX: number
  endX: number
  lane: number
}

export interface TimelineEventCluster {
  id: string
  events: HistoricalEvent[]
  x: number
  startX: number
  endX: number
  focusRange: TimeRange
  timeRange: TimeRange
}

export interface TimelineEventPresentation {
  events: TimelineEventLayout[]
  clusters: TimelineEventCluster[]
}

interface LayoutCandidate extends TimelineEventLayout {
  coordinate: number
}

interface MutableTimelineEventCluster extends TimelineEventCluster {
  positionTotal: number
}

export const TIMELINE_MINIMUM_SPAN = 2
export const TIMELINE_LANE_COUNT = 5

const eventCardWidth = 174
const eventEdgeInset = eventCardWidth / 2
const clusterJoinDistance = eventCardWidth / TIMELINE_LANE_COUNT
const clusterMergeDistance = 112

const stepCandidates: Record<TimelineTickGranularity, number[]> = {
  year: [1, 2, 5, 10],
  decade: [10, 20, 50, 100, 200],
  century: [100, 200, 500, 1000, 2000],
}

export function createTimelineTicks(
  range: TimeRange,
  targetCount = 8,
): TimelineTicks {
  const span = Math.max(1, range.end - range.start)
  const granularity = granularityForSpan(span)
  const step = chooseStep(span / targetCount, stepCandidates[granularity])
  const first = Math.ceil(range.start / step) * step
  const ticks: TimelineTick[] = []

  for (let coordinate = first; coordinate <= range.end; coordinate += step) {
    ticks.push({
      coordinate,
      label: formatTick(coordinate, granularity),
    })
  }

  return { granularity, ticks }
}

export function timelineGranularityLabel(
  granularity: TimelineTickGranularity,
): string {
  return {
    year: '年份',
    decade: '年代',
    century: '世纪',
  }[granularity]
}

export function constrainTimelineRange(
  range: TimeRange,
  limits: TimeRange,
): TimeRange {
  const maximumSpan = Math.max(0, limits.end - limits.start)
  const span = Math.min(Math.max(0, range.end - range.start), maximumSpan)

  if (span >= maximumSpan) return { ...limits }
  if (range.start < limits.start) {
    return { start: limits.start, end: limits.start + span }
  }
  if (range.end > limits.end) {
    return { start: limits.end - span, end: limits.end }
  }

  return { start: range.start, end: range.start + span }
}

/**
 * 将可直接展示的历史事件稳定分配到有限轨道；只有所有轨道都会发生
 * 屏幕空间碰撞时，才把冲突的历史事件收进聚合簇。
 */
export function layoutTimelineEvents(
  events: HistoricalEvent[],
  range: TimeRange,
  canvasWidth: number,
): TimelineEventPresentation {
  const sortedEvents = [...events].sort(compareHistoricalEvents)
  const lanes = Array.from(
    { length: TIMELINE_LANE_COUNT },
    () => [] as LayoutCandidate[],
  )
  const candidates: LayoutCandidate[] = []
  const clusteredEventIds = new Set<string>()
  const clusters: MutableTimelineEventCluster[] = []

  for (const event of sortedEvents) {
    const candidate = createLayoutCandidate(event, range, canvasWidth)
    const nearbyCluster = clusters.at(-1)

    if (
      nearbyCluster &&
      candidate.x - nearbyCluster.endX < clusterJoinDistance
    ) {
      addCandidateToCluster(nearbyCluster, candidate)
      clusteredEventIds.add(event.id)
      continue
    }

    const lane = lanes.findIndex((laneEvents) => {
      const previous = laneEvents.at(-1)
      return !previous || candidate.x - previous.x >= eventCardWidth
    })

    if (lane !== -1) {
      candidate.lane = lane
      lanes[lane].push(candidate)
      candidates.push(candidate)
      continue
    }

    const conflictingCandidates = lanes.map((laneEvents) => laneEvents.pop()!)
    const clusterCandidates = [...conflictingCandidates, candidate]
    for (const conflictingCandidate of conflictingCandidates) {
      clusteredEventIds.add(conflictingCandidate.event.id)
    }
    clusteredEventIds.add(event.id)

    const nextCluster = createCluster(clusterCandidates)
    const previousCluster = clusters.at(-1)
    if (
      previousCluster &&
      nextCluster.x - previousCluster.x < clusterMergeDistance
    ) {
      for (const clusterCandidate of clusterCandidates) {
        addCandidateToCluster(previousCluster, clusterCandidate)
      }
    } else {
      clusters.push(nextCluster)
    }
  }

  return {
    events: candidates
      .filter((candidate) => !clusteredEventIds.has(candidate.event.id))
      .map((candidate) => ({
        event: candidate.event,
        x: candidate.x,
        startX: candidate.startX,
        endX: candidate.endX,
        lane: candidate.lane,
      })),
    clusters: clusters.map(finalizeCluster),
  }
}

export function focusTimelineCluster(
  cluster: TimelineEventCluster,
  currentRange: TimeRange,
): TimeRange {
  const currentSpan = Math.max(
    TIMELINE_MINIMUM_SPAN,
    currentRange.end - currentRange.start,
  )
  const clusterSpan = Math.max(
    0,
    cluster.focusRange.end - cluster.focusRange.start,
  )
  const nextSpan = Math.max(
    TIMELINE_MINIMUM_SPAN,
    Math.min(
      currentSpan * 0.5,
      Math.max(TIMELINE_MINIMUM_SPAN, clusterSpan * 2.4),
    ),
  )
  const center = (cluster.focusRange.start + cluster.focusRange.end) / 2

  return {
    start: center - nextSpan / 2,
    end: center + nextSpan / 2,
  }
}

function createLayoutCandidate(
  event: HistoricalEvent,
  range: TimeRange,
  canvasWidth: number,
): LayoutCandidate {
  const eventRange = timeExpressionRange(event.time)
  const rawStartX = coordinateToCanvasX(eventRange.start, range, canvasWidth)
  const rawEndX = coordinateToCanvasX(eventRange.end, range, canvasWidth)
  const coordinate = (eventRange.start + eventRange.end) / 2

  return {
    event,
    coordinate,
    x: clamp((rawStartX + rawEndX) / 2, eventEdgeInset, canvasWidth - eventEdgeInset),
    startX: clamp(rawStartX, 28, canvasWidth - 28),
    endX: clamp(rawEndX, 28, canvasWidth - 28),
    lane: -1,
  }
}

function createCluster(
  candidates: LayoutCandidate[],
): MutableTimelineEventCluster {
  const cluster: MutableTimelineEventCluster = {
    id: '',
    events: [],
    positionTotal: 0,
    x: 0,
    startX: Infinity,
    endX: -Infinity,
    focusRange: { start: Infinity, end: -Infinity },
    timeRange: { start: Infinity, end: -Infinity },
  }

  for (const candidate of candidates) addCandidateToCluster(cluster, candidate)
  return cluster
}

function addCandidateToCluster(
  cluster: MutableTimelineEventCluster,
  candidate: LayoutCandidate,
): void {
  const eventRange = timeExpressionRange(candidate.event.time)
  cluster.events.push(candidate.event)
  cluster.positionTotal += candidate.x
  cluster.x = cluster.positionTotal / cluster.events.length
  cluster.startX = Math.min(cluster.startX, candidate.x)
  cluster.endX = Math.max(cluster.endX, candidate.x)
  cluster.focusRange = {
    start: Math.min(cluster.focusRange.start, candidate.coordinate),
    end: Math.max(cluster.focusRange.end, candidate.coordinate),
  }
  cluster.timeRange = {
    start: Math.min(cluster.timeRange.start, eventRange.start),
    end: Math.max(cluster.timeRange.end, eventRange.end),
  }
}

function finalizeCluster(
  cluster: MutableTimelineEventCluster,
): TimelineEventCluster {
  const events = [...cluster.events].sort(compareHistoricalEvents)
  const firstId = events[0]?.id ?? 'empty'
  const lastId = events.at(-1)?.id ?? 'empty'

  return {
    id: `${firstId}\u001f${lastId}\u001f${events.length}\u001f${hashEventIds(events)}`,
    events,
    x: cluster.x,
    startX: cluster.startX,
    endX: cluster.endX,
    focusRange: cluster.focusRange,
    timeRange: cluster.timeRange,
  }
}

function hashEventIds(events: HistoricalEvent[]): string {
  let hash = 2_166_136_261
  for (const event of events) {
    for (let index = 0; index < event.id.length; index += 1) {
      hash ^= event.id.charCodeAt(index)
      hash = Math.imul(hash, 16_777_619)
    }
    hash ^= 0
    hash = Math.imul(hash, 16_777_619)
  }
  return (hash >>> 0).toString(36)
}

function coordinateToCanvasX(
  coordinate: number,
  range: TimeRange,
  canvasWidth: number,
): number {
  return ((coordinate - range.start) / (range.end - range.start)) * canvasWidth
}

function granularityForSpan(span: number): TimelineTickGranularity {
  if (span <= 40) return 'year'
  if (span <= 600) return 'decade'
  return 'century'
}

function chooseStep(roughStep: number, candidates: number[]): number {
  const candidate = candidates.find((step) => step >= roughStep)
  if (candidate) return candidate

  const largest = candidates.at(-1) ?? 1
  return Math.ceil(roughStep / largest) * largest
}

function clamp(value: number, min: number, max: number): number {
  return Math.min(max, Math.max(min, value))
}

function formatTick(
  coordinate: number,
  granularity: TimelineTickGranularity,
): string {
  const historicalYear = coordinateToHistoricalYear(coordinate)

  if (granularity === 'year' || historicalYear.year < 10) {
    return formatHistoricalYear(historicalYear)
  }

  const eraPrefix = historicalYear.era === 'BCE' ? '公元前' : '公元'
  if (granularity === 'decade') {
    const decade = Math.floor(historicalYear.year / 10) * 10
    return `${eraPrefix}${decade}年代`
  }

  return `${eraPrefix}${Math.ceil(historicalYear.year / 100)}世纪`
}
