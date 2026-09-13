import { useEffect, useId, useRef } from 'react'
import { formatTimeExpression, type HistoricalEvent } from '../domain/history'

interface EventDetailProps {
  event: HistoricalEvent
  isMobile: boolean
  onClose: () => void
}

const focusableSelector = [
  'a[href]',
  'button:not([disabled])',
  'input:not([disabled])',
  'select:not([disabled])',
  'textarea:not([disabled])',
  '[tabindex]:not([tabindex="-1"])',
].join(',')

export function EventDetail({ event, isMobile, onClose }: EventDetailProps) {
  const panelRef = useRef<HTMLElement>(null)
  const headingRef = useRef<HTMLHeadingElement>(null)
  const closeButtonRef = useRef<HTMLButtonElement>(null)
  const generatedId = useId()
  const titleId = `${generatedId}-title`
  const summaryId = `${generatedId}-summary`

  useEffect(() => {
    if (isMobile) {
      closeButtonRef.current?.focus({ preventScroll: true })
    } else {
      headingRef.current?.focus({ preventScroll: true })
    }
  }, [event.id, isMobile])

  useEffect(() => {
    if (!isMobile) return

    const previousOverflow = document.body.style.overflow
    document.body.style.overflow = 'hidden'
    return () => {
      document.body.style.overflow = previousOverflow
    }
  }, [isMobile])

  useEffect(() => {
    function handleKeyDown(keyboardEvent: KeyboardEvent) {
      if (keyboardEvent.key === 'Escape') {
        keyboardEvent.preventDefault()
        onClose()
        return
      }

      if (keyboardEvent.key !== 'Tab' || !isMobile || !panelRef.current) return

      const focusableElements = Array.from(
        panelRef.current.querySelectorAll<HTMLElement>(focusableSelector),
      )
      const firstElement = focusableElements.at(0)
      const lastElement = focusableElements.at(-1)
      if (!firstElement || !lastElement) return

      if (
        keyboardEvent.shiftKey &&
        (document.activeElement === firstElement ||
          !panelRef.current.contains(document.activeElement))
      ) {
        keyboardEvent.preventDefault()
        lastElement.focus()
      } else if (
        !keyboardEvent.shiftKey &&
        document.activeElement === lastElement
      ) {
        keyboardEvent.preventDefault()
        firstElement.focus()
      }
    }

    document.addEventListener('keydown', handleKeyDown)
    return () => document.removeEventListener('keydown', handleKeyDown)
  }, [isMobile, onClose])

  const timeLabel = formatTimeExpression(event.time)

  return (
    <div
      className={`event-detail-layer${isMobile ? ' event-detail-layer--mobile' : ' event-detail-layer--desktop'}`}
    >
      <button
        type="button"
        className="event-detail-backdrop"
        aria-label="关闭历史事件详情"
        tabIndex={-1}
        onClick={onClose}
      />
      <aside
        ref={panelRef}
        className="event-detail-sheet"
        role="dialog"
        aria-modal={isMobile ? 'true' : undefined}
        aria-labelledby={titleId}
        aria-describedby={summaryId}
      >
        <div className="event-detail-sheet__handle" aria-hidden="true" />
        <div className="event-detail-sheet__topline">
          <span>历史事件详情</span>
          <button
            ref={closeButtonRef}
            type="button"
            aria-label="关闭历史事件详情"
            onClick={onClose}
          >
            <span aria-hidden="true">×</span>
          </button>
        </div>

        <div className="event-detail-sheet__content">
          <div className="event-detail-sheet__time">
            <span>时间表述</span>
            <time>{timeLabel}</time>
          </div>
          <h2 ref={headingRef} id={titleId} tabIndex={-1}>
            {event.title}
          </h2>
          <p id={summaryId} className="event-detail-sheet__summary">
            {event.summary}
          </p>

          <section aria-labelledby={`${titleId}-narrative`}>
            <h3 id={`${titleId}-narrative`}>完整叙述</h3>
            <p>{event.narrative}</p>
          </section>

          <dl className="event-detail-sheet__metadata">
            <DetailField label="主分类" value={event.primaryCategory} />
            <DetailField label="历史时期" value={joinOrFallback(event.periods)} />
            <DetailField label="地区" value={joinOrFallback(event.regions)} />
            <DetailField label="地点" value={joinOrFallback(event.places)} />
            <DetailField
              label="历史人物"
              value={joinOrFallback(event.figures, '无具名历史人物')}
            />
            <DetailField
              label="主题标签"
              value={joinOrFallback(event.topicTags, '未添加主题标签')}
            />
          </dl>

        </div>

        <div className="event-detail-sheet__actions">
          <button type="button" onClick={onClose}>返回时间线</button>
        </div>
      </aside>
    </div>
  )
}

function DetailField({ label, value }: { label: string; value: string }) {
  return (
    <div>
      <dt>{label}</dt>
      <dd>{value}</dd>
    </div>
  )
}

function joinOrFallback(values: string[], fallback = '未标注'): string {
  return values.length > 0 ? values.join('、') : fallback
}
