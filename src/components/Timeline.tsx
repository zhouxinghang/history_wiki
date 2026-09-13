import {
  useCallback,
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
} from 'react'
import {
  coordinateToHistoricalYear,
  formatHistoricalYear,
  formatTimeExpression,
  rangesIntersect,
  timeExpressionRange,
  type HistoricalEvent,
  type TimeRange,
} from '../domain/history'
import {
  constrainTimelineRange,
  createTimelineTicks,
  focusTimelineCluster,
  layoutTimelineEvents,
  TIMELINE_MINIMUM_SPAN,
  timelineGranularityLabel,
  type TimelineEventCluster,
} from '../domain/timeline'
import { TimelineGeometry } from './TimelineGeometry'
import { timelineRenderMetrics } from './timelineRenderMetrics'

interface TimelineProps {
  events: HistoricalEvent[]
  range: TimeRange
  allRange: TimeRange
  selectedEventId?: string
  onSelectEvent: (
    event: HistoricalEvent,
    trigger: HTMLButtonElement,
  ) => void
  onRangeChange: (range: TimeRange) => void
  onViewAll: () => void
  onToday: () => void
  onDismissDetail?: () => void
}

interface SinglePointerGesture {
  lastX: number
  range: TimeRange
}

interface PinchGesture {
  distance: number
  anchorCoordinate: number
  range: TimeRange
}

const { canvasWidth, laneTop, laneHeight } = timelineRenderMetrics
const clusterCardWidth = 112
const clusterPreviewMaxWidth = 320
const clusterPreviewGutter = 12
const clusterPreviewEventLimit = 12
const overlapPageSize = 40

export function Timeline({
  events,
  range,
  allRange,
  selectedEventId,
  onSelectEvent,
  onRangeChange,
  onViewAll,
  onToday,
  onDismissDetail,
}: TimelineProps) {
  const [layoutWidth, setLayoutWidth] = useState<number>(canvasWidth)
  const [expandedClusterId, setExpandedClusterId] = useState<string | null>(null)
  const [previewedClusterId, setPreviewedClusterId] = useState<string | null>(null)
  const [overlapPage, setOverlapPage] = useState(0)
  const tickResult = useMemo(() => createTimelineTicks(range), [range])
  const visibleEvents = useMemo(
    () =>
      events.filter((event) =>
        rangesIntersect(timeExpressionRange(event.time), range),
      ),
    [events, range],
  )
  const presentation = useMemo(
    () => layoutTimelineEvents(visibleEvents, range, layoutWidth),
    [layoutWidth, visibleEvents, range],
  )
  const periodContexts = useMemo(
    () => createPeriodContexts(visibleEvents),
    [visibleEvents],
  )
  const pointersRef = useRef(new Map<number, number>())
  const singlePointerRef = useRef<SinglePointerGesture | null>(null)
  const pinchRef = useRef<PinchGesture | null>(null)
  const previewCloseTimerRef = useRef<number | null>(null)
  const didDragRef = useRef(false)
  const viewportRef = useRef<HTMLDivElement>(null)
  const timelineItemRefs = useRef(new Map<string, HTMLButtonElement>())
  const keyboardInstructionsId = useId()

  const maxSpan = Math.max(
    TIMELINE_MINIMUM_SPAN,
    allRange.end - allRange.start,
  )
  const isAtMaximumZoom =
    range.end - range.start <= TIMELINE_MINIMUM_SPAN + Number.EPSILON
  const expandedCluster = isAtMaximumZoom
    ? presentation.clusters.find((cluster) => cluster.id === expandedClusterId)
    : undefined
  const overlapPageCount = expandedCluster
    ? Math.max(1, Math.ceil(expandedCluster.events.length / overlapPageSize))
    : 1
  const visibleOverlapEvents = useMemo(
    () =>
      expandedCluster
        ? expandedCluster.events.slice(
            overlapPage * overlapPageSize,
            (overlapPage + 1) * overlapPageSize,
          )
        : [],
    [expandedCluster, overlapPage],
  )
  const keyboardItemIds = useMemo(() => {
    const baseItems = [
      ...presentation.events.map(({ event }) => ({
        id: eventNavigationId(event.id),
        coordinate: centerOfRange(timeExpressionRange(event.time)),
      })),
      ...presentation.clusters.map((cluster) => ({
        id: clusterNavigationId(cluster.id),
        coordinate: centerOfRange(cluster.timeRange),
      })),
    ].sort(
      (left, right) =>
        left.coordinate - right.coordinate || left.id.localeCompare(right.id),
    )

    return baseItems.flatMap((item) => {
      if (item.id !== clusterNavigationId(expandedCluster?.id ?? '')) {
        return [item.id]
      }

      return [
        item.id,
        ...visibleOverlapEvents.map((event) =>
          overlapEventNavigationId(expandedCluster!.id, event.id),
        ),
      ]
    })
  }, [
    expandedCluster,
    presentation.clusters,
    presentation.events,
    visibleOverlapEvents,
  ])
  const commitRange = useCallback(
    (nextRange: TimeRange) => {
      const constrainedRange = constrainTimelineRange(nextRange, allRange)
      if (
        constrainedRange.start !== range.start ||
        constrainedRange.end !== range.end
      ) {
        onRangeChange(constrainedRange)
      }
      return constrainedRange
    },
    [allRange, onRangeChange, range.end, range.start],
  )

  useEffect(() => {
    const viewport = viewportRef.current
    if (!viewport) return

    const updateLayoutWidth = () => {
      const width = viewport.getBoundingClientRect().width
      if (width > 0) setLayoutWidth(width)
    }

    updateLayoutWidth()
    if (typeof ResizeObserver === 'function') {
      const observer = new ResizeObserver(updateLayoutWidth)
      observer.observe(viewport)
      return () => observer.disconnect()
    }

    window.addEventListener('resize', updateLayoutWidth)
    return () => window.removeEventListener('resize', updateLayoutWidth)
  }, [])

  useEffect(
    () => () => {
      if (previewCloseTimerRef.current !== null) {
        window.clearTimeout(previewCloseTimerRef.current)
      }
    },
    [],
  )

  useEffect(() => {
    const viewport = viewportRef.current
    if (!viewport) return
    const element = viewport

    function handleWheel(event: WheelEvent) {
      if (event.deltaY === 0) return
      event.preventDefault()
      const bounds = element.getBoundingClientRect()
      const anchorFraction = fractionWithin(bounds, event.clientX)
      commitRange(
        zoomRange(
          range,
          anchorFraction,
          event.deltaY < 0 ? 0.8 : 1.25,
          TIMELINE_MINIMUM_SPAN,
          maxSpan,
        ),
      )
    }

    element.addEventListener('wheel', handleWheel, { passive: false })
    return () => element.removeEventListener('wheel', handleWheel)
  }, [commitRange, maxSpan, range])

  function zoomAt(anchorFraction: number, scale: number) {
    commitRange(
      zoomRange(
        range,
        anchorFraction,
        scale,
        TIMELINE_MINIMUM_SPAN,
        maxSpan,
      ),
    )
  }

  function activateCluster(cluster: TimelineEventCluster) {
    if (previewCloseTimerRef.current !== null) {
      window.clearTimeout(previewCloseTimerRef.current)
      previewCloseTimerRef.current = null
    }
    setPreviewedClusterId(null)

    if (isAtMaximumZoom) {
      setOverlapPage(0)
      setExpandedClusterId((current) =>
        current === cluster.id ? null : cluster.id,
      )
      return
    }

    setExpandedClusterId(null)
    setOverlapPage(0)
    commitRange(focusTimelineCluster(cluster, range))
  }

  function showClusterPreview(clusterId: string) {
    if (previewCloseTimerRef.current !== null) {
      window.clearTimeout(previewCloseTimerRef.current)
      previewCloseTimerRef.current = null
    }
    setPreviewedClusterId(clusterId)
  }

  function hideClusterPreview(clusterId: string) {
    if (previewCloseTimerRef.current !== null) {
      window.clearTimeout(previewCloseTimerRef.current)
    }
    previewCloseTimerRef.current = window.setTimeout(() => {
      setPreviewedClusterId((current) =>
        current === clusterId ? null : current,
      )
      previewCloseTimerRef.current = null
    }, 120)
  }

  function handlePointerDown(event: React.PointerEvent<HTMLDivElement>) {
    if (event.pointerType === 'mouse' && event.button !== 0) return
    if (
      event.target instanceof Element &&
      event.target.closest('button, a, input, select, textarea')
    ) {
      return
    }

    event.currentTarget.setPointerCapture?.(event.pointerId)
    pointersRef.current.set(event.pointerId, event.clientX)
    didDragRef.current = false

    if (pointersRef.current.size === 1) {
      singlePointerRef.current = { lastX: event.clientX, range }
      pinchRef.current = null
      return
    }

    if (pointersRef.current.size === 2) {
      const [first, second] = [...pointersRef.current.values()]
      const bounds = event.currentTarget.getBoundingClientRect()
      const centerX = (first + second) / 2
      const anchorFraction = fractionWithin(bounds, centerX)
      pinchRef.current = {
        distance: Math.max(1, Math.abs(second - first)),
        anchorCoordinate:
          range.start + anchorFraction * (range.end - range.start),
        range,
      }
      singlePointerRef.current = null
    }
  }

  function handlePointerMove(event: React.PointerEvent<HTMLDivElement>) {
    if (!pointersRef.current.has(event.pointerId)) return

    const previousX = pointersRef.current.get(event.pointerId) ?? event.clientX
    pointersRef.current.set(event.pointerId, event.clientX)
    const bounds = event.currentTarget.getBoundingClientRect()
    if (bounds.width <= 0) return

    if (pointersRef.current.size >= 2 && pinchRef.current) {
      const [first, second] = [...pointersRef.current.values()]
      const distance = Math.max(1, Math.abs(second - first))
      const centerX = (first + second) / 2
      const anchorFraction = fractionWithin(bounds, centerX)
      const initialSpan = spanOf(pinchRef.current.range)
      const nextSpan = clamp(
        initialSpan * (pinchRef.current.distance / distance),
        TIMELINE_MINIMUM_SPAN,
        maxSpan,
      )
      const nextRange = {
        start: pinchRef.current.anchorCoordinate - anchorFraction * nextSpan,
        end:
          pinchRef.current.anchorCoordinate +
          (1 - anchorFraction) * nextSpan,
      }

      didDragRef.current = true
      commitRange(nextRange)
      return
    }

    if (pointersRef.current.size === 1 && singlePointerRef.current) {
      const deltaX = event.clientX - singlePointerRef.current.lastX
      if (Math.abs(event.clientX - previousX) > 2) didDragRef.current = true

      const span = spanOf(singlePointerRef.current.range)
      const shift = -(deltaX / bounds.width) * span
      const nextRange = commitRange({
        start: singlePointerRef.current.range.start + shift,
        end: singlePointerRef.current.range.end + shift,
      })

      singlePointerRef.current = { lastX: event.clientX, range: nextRange }
    }
  }

  function finishPointer(event: React.PointerEvent<HTMLDivElement>) {
    pointersRef.current.delete(event.pointerId)
    event.currentTarget.releasePointerCapture?.(event.pointerId)

    if (pointersRef.current.size === 1) {
      const [lastX] = [...pointersRef.current.values()]
      singlePointerRef.current = { lastX, range }
    } else {
      singlePointerRef.current = null
    }
    pinchRef.current = null

    window.setTimeout(() => {
      didDragRef.current = false
    }, 0)
  }

  function registerTimelineItem(
    itemId: string,
    element: HTMLButtonElement | null,
  ) {
    if (element) {
      timelineItemRefs.current.set(itemId, element)
    } else {
      timelineItemRefs.current.delete(itemId)
    }
  }

  function handleTimelineItemKeyDown(
    event: React.KeyboardEvent<HTMLButtonElement>,
    itemId: string,
  ) {
    const direction = {
      ArrowLeft: -1,
      ArrowUp: -1,
      ArrowRight: 1,
      ArrowDown: 1,
    }[event.key]
    const currentIndex = keyboardItemIds.indexOf(itemId)
    let nextIndex: number | undefined

    if (direction && currentIndex !== -1) {
      nextIndex = clamp(currentIndex + direction, 0, keyboardItemIds.length - 1)
    } else if (event.key === 'Home') {
      nextIndex = 0
    } else if (event.key === 'End') {
      nextIndex = keyboardItemIds.length - 1
    }

    if (nextIndex === undefined) return
    event.preventDefault()
    if (nextIndex === currentIndex) return
    const nextItem = timelineItemRefs.current.get(keyboardItemIds[nextIndex])
    if (!nextItem) return

    nextItem.focus()
  }

  function handleTimelineClick(event: React.MouseEvent<HTMLDivElement>) {
    if (!onDismissDetail || didDragRef.current) return
    if (!(event.target instanceof Element)) return
    if (event.target.closest('button, a, input, select, textarea')) return
    onDismissDetail()
  }

  return (
    <div
      className="timeline"
      role="group"
      aria-label={`历史事件时间线，范围从${formatAxisCoordinate(range.start)}到${formatAxisCoordinate(range.end)}`}
      aria-describedby={keyboardInstructionsId}
      onClick={handleTimelineClick}
    >
      <div
        className="timeline__navigation"
        role="group"
        aria-label="时间线导航工具"
      >
        <div className="timeline__zoom-controls">
          <button type="button" aria-label="放大时间线" onClick={() => zoomAt(0.5, 0.72)}>
            ＋
          </button>
          <button type="button" aria-label="缩小时间线" onClick={() => zoomAt(0.5, 1.4)}>
            −
          </button>
          <button type="button" onClick={onViewAll}>查看全部</button>
          <button type="button" onClick={onToday}>回到今天</button>
        </div>
        <div className="timeline__range-status">
          <output aria-live="polite">
            当前视窗：{formatAxisCoordinate(range.start)}—{formatAxisCoordinate(range.end)}
          </output>
          <span>当前时间尺度：{timelineGranularityLabel(tickResult.granularity)}</span>
        </div>
        <p className="timeline__gesture-hint">
          <span id={keyboardInstructionsId}>方向键浏览历史事件，Enter 打开详情</span>
          <span>滚轮缩放 · 拖拽或单指移动 · 双指缩放</span>
        </p>
      </div>

      <div
        className="timeline__period-contexts"
        aria-label="按地区或文明语境显示的历史时期"
      >
        <strong>历史时期语境</strong>
        <div>
          {periodContexts.length > 0 ? (
            periodContexts.map((context) => (
              <span key={context}>{context}</span>
            ))
          ) : (
            <span>当前视窗暂无历史时期标签</span>
          )}
        </div>
      </div>

      <div
        ref={viewportRef}
        className="timeline__viewport"
        role="region"
        aria-label="可交互历史时间线画布"
        onPointerDown={handlePointerDown}
        onPointerMove={handlePointerMove}
        onPointerUp={finishPointer}
        onPointerCancel={finishPointer}
      >
        <div className="timeline__canvas">
          <TimelineGeometry
            presentation={presentation}
            ticks={tickResult}
            range={range}
            layoutWidth={layoutWidth}
            selectedEventId={selectedEventId}
          />

          <ol className="timeline__events" aria-label="可直接展示的历史事件">
            {presentation.events.map(({ event, x, lane }) => {
              const timeLabel = formatTimeExpression(event.time)
              const shapeLabel =
                event.time.kind === 'interval' ? '持续性历史事件' : '瞬时历史事件'
              return (
                <li
                  key={event.id}
                  className="timeline__event-position"
                  style={{
                    left: `${(x / layoutWidth) * 100}%`,
                    top: `${laneTop + lane * laneHeight}px`,
                  }}
                >
                  <button
                    ref={(element) =>
                      registerTimelineItem(eventNavigationId(event.id), element)
                    }
                    type="button"
                    className={`event-card event-card--${categorySlug(event.primaryCategory)}${event.id === selectedEventId ? ' is-selected' : ''}`}
                    aria-label={`${event.title}，${timeLabel}，${shapeLabel}`}
                    aria-pressed={event.id === selectedEventId}
                    aria-keyshortcuts="ArrowLeft ArrowRight ArrowUp ArrowDown Home End Enter"
                    onKeyDown={(keyboardEvent) =>
                      handleTimelineItemKeyDown(
                        keyboardEvent,
                        eventNavigationId(event.id),
                      )
                    }
                    onClick={(clickEvent) => {
                      if (!didDragRef.current) {
                        onSelectEvent(event, clickEvent.currentTarget)
                      }
                    }}
                  >
                    <span className="event-card__meta">
                      <span className="event-card__shape" aria-hidden="true">
                        {event.time.kind === 'interval' ? '━' : '◆'}
                      </span>
                      {event.primaryCategory}
                    </span>
                    <strong>{event.title}</strong>
                    <time>{timeLabel}</time>
                  </button>
                </li>
              )
            })}
          </ol>

          <ol className="timeline__clusters" aria-label="当前可见历史事件聚合簇">
            {presentation.clusters.map((cluster, index) => {
              const timeLabel = formatClusterTimeRange(cluster)
              const overlapListId = `timeline-overlap-list-${index}`
              const previewId = `timeline-cluster-preview-${index}`
              const isExpanded = cluster.id === expandedClusterId
              const isPreviewed = cluster.id === previewedClusterId
              const previewWidth = Math.min(
                clusterPreviewMaxWidth,
                layoutWidth - clusterPreviewGutter * 2,
              )
              const previewViewportLeft = clamp(
                cluster.x - previewWidth / 2,
                clusterPreviewGutter,
                layoutWidth - previewWidth - clusterPreviewGutter,
              )
              const previewLeft =
                previewViewportLeft - (cluster.x - clusterCardWidth / 2)
              const actionLabel = isAtMaximumZoom
                ? '已达到最大缩放，激活以展开重叠历史事件列表'
                : '激活以放大并居中到对应历史区域'

              return (
                <li
                  key={cluster.id}
                  className="timeline__cluster-position"
                  style={{ left: `${(cluster.x / layoutWidth) * 100}%` }}
                  onMouseEnter={() => showClusterPreview(cluster.id)}
                  onMouseLeave={(event) => {
                    if (!event.currentTarget.contains(document.activeElement)) {
                      hideClusterPreview(cluster.id)
                    }
                  }}
                  onFocus={() => showClusterPreview(cluster.id)}
                  onBlur={(event) => {
                    if (!event.currentTarget.contains(event.relatedTarget)) {
                      hideClusterPreview(cluster.id)
                    }
                  }}
                >
                  <button
                    ref={(element) =>
                      registerTimelineItem(
                        clusterNavigationId(cluster.id),
                        element,
                      )
                    }
                    type="button"
                    className="timeline-cluster"
                    aria-label={`聚合簇，${cluster.events.length} 条历史事件，${timeLabel}，${actionLabel}`}
                    aria-expanded={isAtMaximumZoom ? isExpanded : undefined}
                    aria-controls={isAtMaximumZoom ? overlapListId : undefined}
                    aria-keyshortcuts="ArrowLeft ArrowRight ArrowUp ArrowDown Home End Enter"
                    onKeyDown={(keyboardEvent) =>
                      handleTimelineItemKeyDown(
                        keyboardEvent,
                        clusterNavigationId(cluster.id),
                      )
                    }
                    onClick={() => {
                      if (!didDragRef.current) activateCluster(cluster)
                    }}
                  >
                    <span>聚合簇</span>
                    <strong>{cluster.events.length}</strong>
                    <small>条历史事件</small>
                  </button>
                  {isPreviewed && (
                    <section
                      id={previewId}
                      className="timeline-cluster-preview"
                      role="region"
                      aria-label={`聚合历史事件预览，共 ${cluster.events.length} 条`}
                      style={{
                        left: `${previewLeft}px`,
                        width: `${previewWidth}px`,
                      }}
                      onPointerDown={(event) => event.stopPropagation()}
                      onWheelCapture={(event) => event.stopPropagation()}
                    >
                      <header>
                        <span>聚合事件预览</span>
                        <strong>{timeLabel}</strong>
                        <small>
                          选择历史事件查看详情
                          {cluster.events.length > clusterPreviewEventLimit
                            ? `；当前显示前 ${clusterPreviewEventLimit} 条`
                            : ''}
                        </small>
                      </header>
                      <ol>
                        {cluster.events
                          .slice(0, clusterPreviewEventLimit)
                          .map((event) => {
                            const eventTimeLabel = formatTimeExpression(event.time)
                            return (
                              <li key={event.id}>
                                <button
                                  type="button"
                                  aria-label={`${event.title}，${eventTimeLabel}，查看历史事件详情`}
                                  onClick={(clickEvent) => {
                                    if (previewCloseTimerRef.current !== null) {
                                      window.clearTimeout(previewCloseTimerRef.current)
                                      previewCloseTimerRef.current = null
                                    }
                                    setPreviewedClusterId(null)
                                    onSelectEvent(event, clickEvent.currentTarget)
                                  }}
                                >
                                  <span>
                                    <strong>{event.title}</strong>
                                    <time>{eventTimeLabel}</time>
                                  </span>
                                  <small>{event.primaryCategory}</small>
                                </button>
                              </li>
                            )
                          })}
                      </ol>
                      {cluster.events.length > clusterPreviewEventLimit && (
                        <p className="timeline-cluster-preview__remainder">
                          另有 {cluster.events.length - clusterPreviewEventLimit}{' '}
                          条；激活聚合簇可继续放大或分页浏览。
                        </p>
                      )}
                    </section>
                  )}
                </li>
              )
            })}
          </ol>
        </div>
      </div>

      {expandedCluster && (
        <section
          id={`timeline-overlap-list-${presentation.clusters.indexOf(expandedCluster)}`}
          className="timeline__overlap-list"
          aria-labelledby="timeline-overlap-list-title"
        >
          <div>
            <span>最大缩放回退</span>
            <h3 id="timeline-overlap-list-title">
              {formatClusterTimeRange(expandedCluster)}仍有{' '}
              {expandedCluster.events.length} 条历史事件重叠
            </h3>
            <p>
              以下列表可使用键盘浏览；激活历史事件即可阅读详情。当前显示第{' '}
              {overlapPage + 1} / {overlapPageCount} 页。
            </p>
          </div>
          {overlapPageCount > 1 && (
            <nav
              className="timeline__overlap-pagination"
              aria-label="重叠历史事件分页"
            >
              <button
                type="button"
                disabled={overlapPage === 0}
                onClick={() => setOverlapPage((page) => Math.max(0, page - 1))}
              >
                上一页
              </button>
              <output aria-live="polite">
                第 {overlapPage + 1} 页，共 {overlapPageCount} 页
              </output>
              <button
                type="button"
                disabled={overlapPage >= overlapPageCount - 1}
                onClick={() =>
                  setOverlapPage((page) =>
                    Math.min(overlapPageCount - 1, page + 1),
                  )
                }
              >
                下一页
              </button>
            </nav>
          )}
          <ol>
            {visibleOverlapEvents.map((event) => {
              const timeLabel = formatTimeExpression(event.time)
              return (
                <li key={event.id}>
                  <button
                    ref={(element) =>
                      registerTimelineItem(
                        overlapEventNavigationId(expandedCluster.id, event.id),
                        element,
                      )
                    }
                    type="button"
                    aria-label={`${event.title}，${timeLabel}，查看历史事件详情`}
                    aria-pressed={event.id === selectedEventId}
                    aria-keyshortcuts="ArrowLeft ArrowRight ArrowUp ArrowDown Home End Enter"
                    onKeyDown={(keyboardEvent) =>
                      handleTimelineItemKeyDown(
                        keyboardEvent,
                        overlapEventNavigationId(expandedCluster.id, event.id),
                      )
                    }
                    onClick={(clickEvent) =>
                      onSelectEvent(event, clickEvent.currentTarget)
                    }
                  >
                    <strong>{event.title}</strong>
                    <time>{timeLabel}</time>
                    <span>{event.primaryCategory}</span>
                  </button>
                </li>
              )
            })}
          </ol>
        </section>
      )}
    </div>
  )
}

function createPeriodContexts(events: HistoricalEvent[]): string[] {
  const contexts = new Set<string>()

  for (const event of events) {
    const region = event.regions[0] ?? '相关文明'
    for (const period of event.periods) {
      contexts.add(`${region} · ${period}`)
    }
  }

  return [...contexts]
}

function formatClusterTimeRange(cluster: TimelineEventCluster): string {
  const start = formatAxisCoordinate(cluster.timeRange.start)
  const end = formatAxisCoordinate(cluster.timeRange.end)
  return start === end ? start : `从${start}到${end}`
}

function zoomRange(
  range: TimeRange,
  anchorFraction: number,
  scale: number,
  minimumSpan: number,
  maximumSpan: number,
): TimeRange {
  const currentSpan = spanOf(range)
  const normalizedAnchor = clamp(anchorFraction, 0, 1)
  const nextSpan = clamp(currentSpan * scale, minimumSpan, maximumSpan)
  const anchor = range.start + normalizedAnchor * currentSpan

  return {
    start: anchor - normalizedAnchor * nextSpan,
    end: anchor + (1 - normalizedAnchor) * nextSpan,
  }
}

function fractionWithin(bounds: DOMRect, clientX: number): number {
  if (bounds.width <= 0) return 0.5
  return clamp((clientX - bounds.left) / bounds.width, 0, 1)
}

function spanOf(range: TimeRange): number {
  return Math.max(TIMELINE_MINIMUM_SPAN, range.end - range.start)
}

function formatAxisCoordinate(coordinate: number): string {
  return formatHistoricalYear(coordinateToHistoricalYear(coordinate))
}

function clamp(value: number, min: number, max: number): number {
  return Math.min(max, Math.max(min, value))
}

function categorySlug(category: HistoricalEvent['primaryCategory']): string {
  const slugs: Record<HistoricalEvent['primaryCategory'], string> = {
    政治: 'politics',
    军事: 'military',
    文化: 'culture',
    科技: 'technology',
    社会: 'society',
    交流: 'exchange',
  }
  return slugs[category]
}

function eventNavigationId(eventId: string): string {
  return `event:${eventId}`
}

function clusterNavigationId(clusterId: string): string {
  return `cluster:${clusterId}`
}

function overlapEventNavigationId(clusterId: string, eventId: string): string {
  return `cluster:${clusterId}:event:${eventId}`
}

function centerOfRange(range: TimeRange): number {
  return (range.start + range.end) / 2
}
