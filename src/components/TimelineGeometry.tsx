import type { HistoricalEvent, TimeRange } from '../domain/history'
import type {
  TimelineEventPresentation,
  TimelineTicks,
} from '../domain/timeline'
import { timelineRenderMetrics } from './timelineRenderMetrics'

interface TimelineGeometryProps {
  presentation: TimelineEventPresentation
  ticks: TimelineTicks
  range: TimeRange
  layoutWidth: number
  selectedEventId?: string
}

/**
 * 时间线的图形绘制边界。布局层只产出 TimelineEventPresentation；若未来
 * 实测需要 Canvas，可替换此组件而不改变布局、HTML 交互层和无障碍语义。
 */
export function TimelineGeometry({
  presentation,
  ticks,
  range,
  layoutWidth,
  selectedEventId,
}: TimelineGeometryProps) {
  const { canvasWidth, canvasHeight, axisY, laneTop, laneHeight, clusterY } =
    timelineRenderMetrics

  return (
    <svg
      className="timeline__geometry"
      viewBox={`0 0 ${canvasWidth} ${canvasHeight}`}
      preserveAspectRatio="none"
      aria-hidden="true"
    >
      <defs>
        <linearGradient id="axis-glow" x1="0" x2="1">
          <stop offset="0" stopColor="#b48951" stopOpacity="0.12" />
          <stop offset="0.5" stopColor="#173c5b" stopOpacity="0.9" />
          <stop offset="1" stopColor="#b48951" stopOpacity="0.12" />
        </linearGradient>
      </defs>
      <line
        x1="28"
        x2={canvasWidth - 28}
        y1={axisY}
        y2={axisY}
        stroke="url(#axis-glow)"
        strokeWidth="2"
      />

      {ticks.ticks.map((tick) => {
        const x = coordinateToX(tick.coordinate, range, canvasWidth)
        return (
          <g key={tick.coordinate}>
            <line
              x1={x}
              x2={x}
              y1={axisY - 8}
              y2={canvasHeight - 18}
              className="timeline__grid-line"
            />
            <line
              x1={x}
              x2={x}
              y1={axisY - 8}
              y2={axisY + 8}
              className="timeline__tick"
            />
            <text x={x} y={39} className="timeline__tick-label">
              {tick.label}
            </text>
          </g>
        )
      })}

      {presentation.events.map(({ event, x, startX, endX, lane }) => {
        const eventY = laneTop + lane * laneHeight
        const isInterval = event.time.kind === 'interval'
        const selected = event.id === selectedEventId
        const canvasX = layoutToCanvasX(x, layoutWidth, canvasWidth)
        const canvasStartX = layoutToCanvasX(startX, layoutWidth, canvasWidth)
        const canvasEndX = layoutToCanvasX(endX, layoutWidth, canvasWidth)

        return (
          <g
            key={event.id}
            className={`timeline__event-geometry timeline__event-geometry--${categorySlug(event.primaryCategory)}${selected ? ' is-selected' : ''}`}
          >
            {isInterval ? (
              <>
                <rect
                  x={canvasStartX}
                  y={eventY - 15}
                  width={Math.max(canvasEndX - canvasStartX, 4)}
                  height="8"
                  rx="4"
                  className="timeline__interval"
                />
                <line
                  x1={canvasX}
                  x2={canvasX}
                  y1={axisY + 3}
                  y2={eventY - 15}
                  className="timeline__stem"
                />
              </>
            ) : (
              <>
                <circle cx={canvasX} cy={axisY} r={selected ? 7 : 5} />
                <line
                  x1={canvasX}
                  x2={canvasX}
                  y1={axisY + 5}
                  y2={eventY - 7}
                  className="timeline__stem"
                />
              </>
            )}
          </g>
        )
      })}

      {presentation.clusters.map((cluster) => {
        const canvasX = layoutToCanvasX(cluster.x, layoutWidth, canvasWidth)
        return (
          <g key={cluster.id} className="timeline__cluster-geometry">
            <circle cx={canvasX} cy={axisY} r="6" />
            <line
              x1={canvasX}
              x2={canvasX}
              y1={axisY + 6}
              y2={clusterY - 5}
              className="timeline__stem"
            />
          </g>
        )
      })}
    </svg>
  )
}

function coordinateToX(
  coordinate: number,
  range: TimeRange,
  canvasWidth: number,
): number {
  return ((coordinate - range.start) / (range.end - range.start)) * canvasWidth
}

function layoutToCanvasX(
  x: number,
  layoutWidth: number,
  canvasWidth: number,
): number {
  return (x / layoutWidth) * canvasWidth
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
