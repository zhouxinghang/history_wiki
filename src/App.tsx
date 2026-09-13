import { useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { EventDetail } from './components/EventDetail'
import {
  SearchFilters,
  type FilterDimension,
} from './components/SearchFilters'
import { Timeline } from './components/Timeline'
import { createHttpHistoryEventRepository } from './data/httpHistoryEventRepository'
import {
  createExplorationUrl,
  normalizeUrlViewport,
  parseExplorationUrlState,
  validateUrlQueryCriteria,
  type ExplorationUrlState,
} from './domain/explorationUrlState'
import {
  coordinateToHistoricalYear,
  formatHistoricalYear,
  type HistoricalEvent,
  type HistoryEventFilterMetadata,
  type HistoryEventFilters,
  type HistoryEventQueryCriteria,
  type HistoryEventQueryResult,
  type HistoryEventRepository,
  type PrimaryCategory,
  type TimeRange,
} from './domain/history'
import {
  constrainTimelineRange,
  TIMELINE_MINIMUM_SPAN,
} from './domain/timeline'

interface AppProps {
  repository?: HistoryEventRepository
  initialQueryCriteria?: HistoryEventQueryCriteria
}

type DataStatus = 'loading' | 'ready' | 'updating' | 'error'
type ErrorStage = 'bounds' | 'query'

interface LoadError {
  message: string
  stage: ErrorStage
}

const explorationHistoryStateKey = 'historyWikiExploration'
const filterDrawerFocusableSelector = [
  'a[href]',
  'button:not([disabled])',
  'input:not([disabled])',
  'select:not([disabled])',
  'textarea:not([disabled])',
  '[tabindex]:not([tabindex="-1"])',
].join(',')
const emptyFilters: HistoryEventFilters = {
  periods: [],
  regions: [],
  figures: [],
  primaryCategories: [],
}

export default function App({
  repository,
  initialQueryCriteria = {},
}: AppProps) {
  const [initialUrlState] = useState(() =>
    parseExplorationUrlState(window.location.search),
  )
  const initialQueryCriteriaRef = useRef(initialQueryCriteria)
  const activeRepository = useMemo(
    () => {
      const defaultRepository = createHttpHistoryEventRepository()
      return (
        repository ??
        window.__historyWikiDemoRepositoryFactory?.(defaultRepository) ??
        defaultRepository
      )
    },
    [repository],
  )
  const [loadAttempt, setLoadAttempt] = useState(0)
  const [queryAttempt, setQueryAttempt] = useState(0)
  const [queryCriteria, setQueryCriteria] = useState<HistoryEventQueryCriteria>(
    () =>
      normalizeQueryCriteria(
        initialUrlState.hasStateParameters
          ? initialUrlState.queryCriteria
          : initialQueryCriteria,
      ),
  )
  const [status, setStatus] = useState<DataStatus>('loading')
  const [loadError, setLoadError] = useState<LoadError | null>(null)
  const [bounds, setBounds] = useState<TimeRange | null>(null)
  const [filterMetadata, setFilterMetadata] =
    useState<HistoryEventFilterMetadata | null>(null)
  const [allRange, setAllRange] = useState<TimeRange | null>(null)
  const [viewport, setViewport] = useState<TimeRange | null>(null)
  const [queryResult, setQueryResult] =
    useState<HistoryEventQueryResult | null>(null)
  const [queryRevision, setQueryRevision] = useState(0)
  const [catalogLoaded, setCatalogLoaded] = useState(false)
  const [selectedEvent, setSelectedEvent] = useState<HistoricalEvent | null>(null)
  const selectedEventIdRef = useRef(initialUrlState.selectedEventId)
  const [isFilterDrawerOpen, setIsFilterDrawerOpen] = useState(false)
  const selectedEventTriggerRef = useRef<HTMLButtonElement | null>(null)
  const filterDrawerTriggerRef = useRef<HTMLButtonElement | null>(null)
  const isMobileViewport = useMediaQuery('(max-width: 820px)')
  const hasMobileModal =
    isMobileViewport && Boolean(selectedEvent || isFilterDrawerOpen)

  useEffect(() => {
    const controller = new AbortController()
    let active = true

    async function loadCatalog() {
      try {
        const [nextBounds, nextFilterMetadata] = await Promise.all([
          activeRepository.getBounds(controller.signal),
          activeRepository.getFilterMetadata(controller.signal),
        ])
        if (!active) return
        setCatalogLoaded(true)
        setFilterMetadata(nextFilterMetadata)
        if (!nextBounds) {
          setStatus('ready')
          setLoadError(null)
          setBounds(null)
          setAllRange(null)
          setViewport(null)
          setQueryResult({
            events: [],
            sourceTotal: 0,
            totalMatching: 0,
            returnedProminence: 1,
          })
          setSelectedEvent(null)
          selectedEventIdRef.current = undefined
          selectedEventTriggerRef.current = null
          return
        }
        const timelineRange = createTimelineRange(nextBounds)
        const locationState = parseExplorationUrlState(window.location.search)
        const nextCriteria = normalizeQueryCriteria(
          locationState.hasStateParameters
            ? validateUrlQueryCriteria(
                locationState.queryCriteria,
                nextFilterMetadata,
              )
            : initialQueryCriteriaRef.current,
        )
        const nextViewport = normalizeUrlViewport(
          locationState.viewport,
          timelineRange,
          TIMELINE_MINIMUM_SPAN,
        )
        setStatus('loading')
        setLoadError(null)
        setBounds(nextBounds)
        setAllRange(timelineRange)
        setQueryResult(null)
        setSelectedEvent(null)
        selectedEventIdRef.current = locationState.selectedEventId
        selectedEventTriggerRef.current = null
        setQueryCriteria(nextCriteria)
        setViewport(nextViewport)
        replaceExplorationUrl({
          viewport: nextViewport,
          queryCriteria: nextCriteria,
          selectedEventId: locationState.selectedEventId,
        })
      } catch (error) {
        if (!active || isAbortError(error)) return
        setCatalogLoaded(false)
        setStatus('error')
        setLoadError({
          stage: 'bounds',
          message: error instanceof Error ? error.message : '发生未知错误',
        })
      }
    }

    void loadCatalog()
    return () => {
      active = false
      controller.abort()
    }
  }, [activeRepository, loadAttempt])

  useEffect(() => {
    if (!viewport) return

    const controller = new AbortController()
    let active = true

    async function loadEvents() {
      try {
        const result = await activeRepository.query({
          visibleRange: viewport as TimeRange,
          ...queryCriteria,
          signal: controller.signal,
        })
        if (!active) return
        const desiredEventId = selectedEventIdRef.current
        let restoredEvent = desiredEventId
          ? result.events.find((event) => event.id === desiredEventId) ?? null
          : null
        if (desiredEventId && !restoredEvent) {
          try {
            restoredEvent = await activeRepository.getById(
              desiredEventId,
              controller.signal,
            )
          } catch (error) {
            if (isAbortError(error)) throw error
          }
        }
        if (!active) return
        setQueryResult(result)
        setQueryRevision((revision) => revision + 1)
        setSelectedEvent(restoredEvent)
        if (desiredEventId && !restoredEvent) {
          selectedEventIdRef.current = undefined
          replaceExplorationUrl(
            { viewport: viewport as TimeRange, queryCriteria },
            false,
          )
        }
        setLoadError(null)
        setStatus('ready')
      } catch (error) {
        if (!active || isAbortError(error)) return
        setStatus('error')
        setLoadError({
          stage: 'query',
          message: error instanceof Error ? error.message : '发生未知错误',
        })
      }
    }

    void loadEvents()
    return () => {
      active = false
      controller.abort()
    }
  }, [activeRepository, queryAttempt, queryCriteria, viewport])

  useEffect(() => {
    if (!allRange || !filterMetadata) return

    function restoreFromUrl() {
      const locationState = parseExplorationUrlState(window.location.search)
      const nextCriteria = normalizeQueryCriteria(
        validateUrlQueryCriteria(locationState.queryCriteria, filterMetadata!),
      )
      const nextViewport = normalizeUrlViewport(
        locationState.viewport,
        allRange!,
        TIMELINE_MINIMUM_SPAN,
      )
      const trigger = selectedEventTriggerRef.current

      setStatus(queryResult ? 'updating' : 'loading')
      setLoadError(null)
      setQueryCriteria(nextCriteria)
      setViewport(nextViewport)
      setSelectedEvent(null)
      selectedEventIdRef.current = locationState.selectedEventId
      setIsFilterDrawerOpen(false)
      replaceExplorationUrl({
        viewport: nextViewport,
        queryCriteria: nextCriteria,
        selectedEventId: locationState.selectedEventId,
      })

      if (!locationState.selectedEventId) {
        window.setTimeout(() => {
          if (trigger?.isConnected) trigger.focus()
        }, 0)
      }
    }

    window.addEventListener('popstate', restoreFromUrl)
    return () => window.removeEventListener('popstate', restoreFromUrl)
  }, [allRange, filterMetadata, queryResult])

  const rangeSummary = useMemo(() => {
    if (!bounds) return catalogLoaded ? '暂无已发布历史事件' : null
    return `${formatCoordinate(bounds.start)} — ${formatCoordinate(bounds.end)}`
  }, [bounds, catalogLoaded])

  const isInitialLoading = status === 'loading' && !queryResult
  const isBlockingError = status === 'error' && !queryResult
  const hasQueryCriteria = hasActiveQueryCriteria(queryCriteria)
  const emptyState = queryResult
    ? getEmptyState(queryResult, hasQueryCriteria)
    : null

  function reloadSource() {
    setStatus('loading')
    setCatalogLoaded(false)
    setLoadError(null)
    setBounds(null)
    setFilterMetadata(null)
    setAllRange(null)
    setViewport(null)
    setQueryResult(null)
    setSelectedEvent(null)
    setIsFilterDrawerOpen(false)
    selectedEventTriggerRef.current = null
    setLoadAttempt((value) => value + 1)
  }

  function retryQuery() {
    setStatus(queryResult ? 'updating' : 'loading')
    setLoadError(null)
    setQueryAttempt((value) => value + 1)
  }

  function changeViewport(nextViewport: TimeRange) {
    if (!allRange) return
    const constrainedViewport = constrainTimelineRange(nextViewport, allRange)
    if (
      viewport &&
      constrainedViewport.start === viewport.start &&
      constrainedViewport.end === viewport.end
    ) {
      return
    }
    setStatus(queryResult ? 'updating' : 'loading')
    setLoadError(null)
    setViewport(constrainedViewport)
    replaceExplorationUrl({
      viewport: constrainedViewport,
      queryCriteria,
      selectedEventId: selectedEventIdRef.current,
    })
  }

  function clearQueryCriteria() {
    const nextCriteria = normalizeQueryCriteria({})
    setStatus(queryResult ? 'updating' : 'loading')
    setLoadError(null)
    setQueryCriteria(nextCriteria)
    if (viewport) {
      replaceExplorationUrl({
        viewport,
        queryCriteria: nextCriteria,
        selectedEventId: selectedEventIdRef.current,
      })
    }
  }

  function changeSearchTerm(searchTerm: string) {
    const nextCriteria = {
      ...normalizeQueryCriteria(queryCriteria),
      searchTerm,
    }
    setStatus(queryResult ? 'updating' : 'loading')
    setLoadError(null)
    setQueryCriteria(nextCriteria)
    if (viewport) {
      replaceExplorationUrl({
        viewport,
        queryCriteria: nextCriteria,
        selectedEventId: selectedEventIdRef.current,
      })
    }
  }

  function toggleFilter(
    dimension: FilterDimension,
    value: string | PrimaryCategory,
  ) {
    const normalized = normalizeQueryCriteria(queryCriteria)
    const filters = normalized.filters as Required<HistoryEventFilters>

    switch (dimension) {
      case 'periods':
        filters.periods = toggleSelection(filters.periods, value)
        break
      case 'regions':
        filters.regions = toggleSelection(filters.regions, value)
        break
      case 'figures':
        filters.figures = toggleSelection(filters.figures, value)
        break
      case 'primaryCategories':
        filters.primaryCategories = toggleSelection(
          filters.primaryCategories,
          value as PrimaryCategory,
        )
        break
    }

    const nextCriteria = { ...normalized, filters }
    setStatus(queryResult ? 'updating' : 'loading')
    setLoadError(null)
    setQueryCriteria(nextCriteria)
    if (viewport) {
      replaceExplorationUrl({
        viewport,
        queryCriteria: nextCriteria,
        selectedEventId: selectedEventIdRef.current,
      })
    }
  }

  function openFilterDrawer(trigger: HTMLButtonElement) {
    filterDrawerTriggerRef.current = trigger
    setIsFilterDrawerOpen(true)
  }

  function closeFilterDrawer() {
    const trigger = filterDrawerTriggerRef.current
    setIsFilterDrawerOpen(false)
    filterDrawerTriggerRef.current = null
    window.setTimeout(() => {
      if (trigger?.isConnected) trigger.focus()
    }, 0)
  }

  function viewAll() {
    if (allRange) changeViewport({ ...allRange })
  }

  function goToToday() {
    if (!viewport) return
    const today = new Date().getFullYear()
    const currentSpan = viewport.end - viewport.start
    const focusSpan = Math.min(currentSpan, 120)
    changeViewport({
      start: today - focusSpan / 2,
      end: today + focusSpan / 2,
    })
  }

  function openEventDetail(
    event: HistoricalEvent,
    trigger: HTMLButtonElement,
  ) {
    selectedEventTriggerRef.current = trigger
    setSelectedEvent(event)
    const previousEventId = selectedEventIdRef.current
    selectedEventIdRef.current = event.id
    if (viewport && previousEventId !== event.id) {
      replaceExplorationUrl({
        viewport,
        queryCriteria,
        selectedEventId: event.id,
      }, false)
    }
  }

  function closeEventDetail() {
    dismissEventDetail(true)
  }

  function dismissEventDetailFromTimeline() {
    dismissEventDetail(false)
  }

  function dismissEventDetail(restoreFocus: boolean) {
    const trigger = selectedEventTriggerRef.current
    setSelectedEvent(null)
    selectedEventIdRef.current = undefined
    selectedEventTriggerRef.current = null
    if (viewport) {
      replaceExplorationUrl({ viewport, queryCriteria }, false)
    }
    if (restoreFocus) {
      window.setTimeout(() => {
        if (trigger?.isConnected) trigger.focus({ preventScroll: true })
      }, 0)
    }
  }

  return (
    <>
    <div
      className="app-shell"
      data-query-status={status}
      data-query-revision={queryRevision}
      data-source-total={queryResult?.sourceTotal}
      aria-hidden={hasMobileModal ? true : undefined}
      inert={hasMobileModal ? true : undefined}
    >
      <header className="site-header">
        <a className="brand" href="#top" aria-label="经纬史首页">
          <span className="brand__seal" aria-hidden="true">史</span>
          <span>
            <strong>经纬史</strong>
            <small>HISTORY WIKI</small>
          </span>
        </a>
        <p className="site-header__note">一条时间线，看见彼此的时代</p>
        <a className="site-header__admin" href="/admin">管理区</a>
      </header>

      <main id="top">
        <section className="hero" aria-labelledby="page-title">
          <div className="hero__eyebrow">
            <span aria-hidden="true">01</span>
            跨文明历史总览
          </div>
          <div className="hero__content">
            <div>
              <h1 id="page-title">
                时间并非孤岛
                <span>在同一尺度上阅读世界。</span>
              </h1>
              <p>
                以连续的历史纪年为经，以不同文明的历史事件为纬。
                从古代石造工程到全球公共卫生，观察遥远地区如何共享同一段时间。
              </p>
            </div>
            <dl className="hero__facts" aria-label="历史数据概览">
              <div>
                <dt>时间跨度</dt>
                <dd>{rangeSummary ?? '读取中…'}</dd>
              </div>
              <div>
                <dt>表达方式</dt>
                <dd>节点 · 区间 · 约数</dd>
              </div>
              <div>
                <dt>阅读视角</dt>
                <dd>中国史 × 同期世界史</dd>
              </div>
            </dl>
          </div>
        </section>

        <section
          className="overview"
          aria-labelledby="timeline-title"
        >
          <div className="section-heading">
            <div>
              <span className="section-heading__index">时间轴 / OVERVIEW</span>
              <h2 id="timeline-title">历史事件总览</h2>
            </div>
            <div className="legend" aria-label="时间线图例">
              <span><i className="legend__point" /> 瞬时历史事件</span>
              <span><i className="legend__interval" /> 持续性历史事件</span>
            </div>
          </div>

          {isInitialLoading && (
            <div className="status-card" role="status">
              <span className="status-card__loader" aria-hidden="true" />
              <div>
                <strong>
                  {bounds ? '正在载入历史事件' : '正在连接历史事件服务'}
                </strong>
                <p>
                  {bounds
                    ? '正在查询当前时间范围内的历史事件…'
                    : '正在读取完整时间范围…'}
                </p>
              </div>
            </div>
          )}

          {isBlockingError && loadError && (
            <ErrorCard
              title={
                loadError.stage === 'bounds'
                  ? '暂时无法读取历史事件服务'
                  : '暂时无法查询历史事件'
              }
              message={loadError.message}
              onRetry={loadError.stage === 'bounds' ? reloadSource : retryQuery}
            />
          )}

          {queryResult && viewport && allRange && filterMetadata && (
            <>
              {isMobileViewport && (
                <button
                  type="button"
                  className="mobile-filter-trigger"
                  aria-haspopup="dialog"
                  onClick={(event) => openFilterDrawer(event.currentTarget)}
                >
                  <span>搜索与筛选</span>
                  <strong>{formatActiveCriteriaCount(queryCriteria)}</strong>
                </button>
              )}

              <div className="explorer-layout">
                {!isMobileViewport && (
                  <aside className="explorer-layout__sidebar">
                    <SearchFilters
                      metadata={filterMetadata}
                      criteria={queryCriteria}
                      onSearchTermChange={changeSearchTerm}
                      onFilterToggle={toggleFilter}
                      onClear={clearQueryCriteria}
                      headingId="desktop-filter-title"
                    />
                  </aside>
                )}

                <div className="explorer-layout__results">
                  {!emptyState && (
                    <div className="timeline-toolbar">
                      <p aria-live="polite">
                        当前呈现 <strong>{queryResult.events.length}</strong> 条；当前范围匹配{' '}
                        <strong>{queryResult.totalMatching}</strong> 条历史事件
                      </p>
                      <p>
                        当前尺度显示 L1–L{queryResult.returnedProminence} 事件
                      </p>
                    </div>
                  )}
                  {status === 'updating' && (
                    <div className="timeline__update-status" role="status">
                      <span className="timeline__mini-loader" aria-hidden="true" />
                      正在更新当前范围的历史事件…
                    </div>
                  )}
                  {status === 'error' && loadError && (
                    <div className="timeline__inline-error" role="alert">
                      更新当前范围失败：{loadError.message}
                      <button type="button" onClick={retryQuery}>重试</button>
                    </div>
                  )}
                  {emptyState ? (
                    <EmptyStateCard
                      kind={emptyState}
                      onReloadSource={reloadSource}
                      onClearCriteria={clearQueryCriteria}
                      onViewAll={viewAll}
                    />
                  ) : (
                    <Timeline
                      events={queryResult.events}
                      range={viewport}
                      allRange={allRange}
                      selectedEventId={selectedEvent?.id}
                      onSelectEvent={openEventDetail}
                      onRangeChange={changeViewport}
                      onViewAll={viewAll}
                      onToday={goToToday}
                      onDismissDetail={
                        selectedEvent ? dismissEventDetailFromTimeline : undefined
                      }
                    />
                  )}
                </div>
              </div>
            </>
          )}
          {emptyState === 'source' && !viewport && (
            <EmptyStateCard
              kind="source"
              onReloadSource={reloadSource}
              onClearCriteria={clearQueryCriteria}
              onViewAll={viewAll}
            />
          )}
        </section>

        <section className="reading-panel" aria-label="历史事件阅读提示">
          <div className="reading-panel__note">
            <span className="reading-panel__kicker">阅读提示</span>
            <h2>一条轴线，多种时间语境</h2>
            <p>
              历史时期只在各自的地区与文明语境内成立。时间线使用连续坐标进行排列，
              但始终以不存在公元 0 年的历史纪年呈现给读者。
            </p>
            <div className="category-key" aria-label="主分类图例">
              {['政治', '军事', '文化', '科技', '社会', '交流'].map((category) => (
                <span key={category} className={`category-key__${category}`}>
                  {category}
                </span>
              ))}
            </div>
          </div>
        </section>
      </main>

      <footer>
        <span>经纬史 · 简体中文历史 Wiki</span>
        <span>历史内容由项目编辑者持续维护</span>
      </footer>
    </div>
    {selectedEvent && (
      <EventDetail
        event={selectedEvent}
        isMobile={isMobileViewport}
        onClose={closeEventDetail}
      />
    )}
    {isMobileViewport && isFilterDrawerOpen && filterMetadata && (
      <FilterDrawer onClose={closeFilterDrawer}>
        <SearchFilters
          metadata={filterMetadata}
          criteria={queryCriteria}
          onSearchTermChange={changeSearchTerm}
          onFilterToggle={toggleFilter}
          onClear={clearQueryCriteria}
          headingId="mobile-filter-title"
          className="search-filters--mobile"
        />
      </FilterDrawer>
    )}
    </>
  )
}

function FilterDrawer({
  children,
  onClose,
}: {
  children: ReactNode
  onClose: () => void
}) {
  const panelRef = useRef<HTMLDivElement>(null)
  const closeButtonRef = useRef<HTMLButtonElement>(null)
  const onCloseRef = useRef(onClose)

  useEffect(() => {
    onCloseRef.current = onClose
  }, [onClose])

  useEffect(() => {
    const previousOverflow = document.body.style.overflow
    document.body.style.overflow = 'hidden'
    closeButtonRef.current?.focus()

    function handleKeyDown(event: KeyboardEvent) {
      if (event.key === 'Escape') {
        event.preventDefault()
        onCloseRef.current()
        return
      }

      if (event.key !== 'Tab' || !panelRef.current) return

      const focusableElements = Array.from(
        panelRef.current.querySelectorAll<HTMLElement>(
          filterDrawerFocusableSelector,
        ),
      )
      const firstElement = focusableElements.at(0)
      const lastElement = focusableElements.at(-1)
      if (!firstElement || !lastElement) return

      if (
        event.shiftKey &&
        (document.activeElement === firstElement ||
          !panelRef.current.contains(document.activeElement))
      ) {
        event.preventDefault()
        lastElement.focus()
      } else if (!event.shiftKey && document.activeElement === lastElement) {
        event.preventDefault()
        firstElement.focus()
      }
    }

    document.addEventListener('keydown', handleKeyDown)
    return () => {
      document.removeEventListener('keydown', handleKeyDown)
      document.body.style.overflow = previousOverflow
    }
  }, [])

  return (
    <div className="filter-drawer-layer">
      <button
        type="button"
        className="filter-drawer-backdrop"
        aria-label="关闭搜索与筛选"
        tabIndex={-1}
        onClick={onClose}
      />
      <div
        ref={panelRef}
        className="filter-drawer"
        role="dialog"
        aria-modal="true"
        aria-labelledby="mobile-filter-title"
      >
        <div className="filter-drawer__handle" aria-hidden="true" />
        <button
          ref={closeButtonRef}
          type="button"
          className="filter-drawer__close"
          onClick={onClose}
        >
          完成
        </button>
        {children}
      </div>
    </div>
  )
}

function ErrorCard({
  title,
  message,
  onRetry,
}: {
  title: string
  message: string
  onRetry: () => void
}) {
  return (
    <div className="status-card status-card--error" role="alert">
      <span className="status-card__mark" aria-hidden="true">!</span>
      <div>
        <strong>{title}</strong>
        <p>{message}</p>
        <button type="button" onClick={onRetry}>重新读取</button>
      </div>
    </div>
  )
}

function createTimelineRange(bounds: TimeRange): TimeRange {
  if (bounds.start > bounds.end) {
    throw new RangeError('历史事件时间边界无效。')
  }
  const span = bounds.end - bounds.start
  if (span >= TIMELINE_MINIMUM_SPAN) return { ...bounds }

  const center = (bounds.start + bounds.end) / 2
  return {
    start: center - TIMELINE_MINIMUM_SPAN / 2,
    end: center + TIMELINE_MINIMUM_SPAN / 2,
  }
}

type EmptyState = 'source' | 'criteria' | 'range'

function EmptyStateCard({
  kind,
  onReloadSource,
  onClearCriteria,
  onViewAll,
}: {
  kind: EmptyState
  onReloadSource: () => void
  onClearCriteria: () => void
  onViewAll: () => void
}) {
  const content = {
    source: {
      title: '还没有已发布的历史事件',
      description: '数据库当前为空。可以稍后重新读取，查看是否已有历史事件。',
      action: '重新读取历史事件',
      onAction: onReloadSource,
    },
    criteria: {
      title: '没有符合当前条件的历史事件',
      description: '清空搜索与筛选条件后，可以重新浏览当前时间范围。',
      action: '清空搜索与筛选',
      onAction: onClearCriteria,
    },
    range: {
      title: '当前时间范围暂无历史事件',
      description: '数据源中仍有其他历史事件，可以返回完整时间范围继续浏览。',
      action: '查看全部历史事件',
      onAction: onViewAll,
    },
  }[kind]

  return (
    <div className="status-card status-card--empty" role="status">
      <span className="status-card__mark" aria-hidden="true">○</span>
      <div>
        <strong>{content.title}</strong>
        <p>{content.description}</p>
        <button type="button" onClick={content.onAction}>{content.action}</button>
      </div>
    </div>
  )
}

function getEmptyState(
  result: HistoryEventQueryResult,
  hasQueryCriteria: boolean,
): EmptyState | null {
  if (result.sourceTotal === 0) return 'source'
  if (result.totalMatching > 0) return null
  return hasQueryCriteria ? 'criteria' : 'range'
}

function hasActiveQueryCriteria(criteria: HistoryEventQueryCriteria): boolean {
  if (criteria.searchTerm?.trim()) return true
  if (!criteria.filters) return false

  return Object.values(criteria.filters).some((values) => values?.length)
}

function normalizeQueryCriteria(
  criteria: HistoryEventQueryCriteria,
): HistoryEventQueryCriteria {
  return {
    searchTerm: criteria.searchTerm ?? '',
    filters: {
      periods: criteria.filters?.periods ?? emptyFilters.periods,
      regions: criteria.filters?.regions ?? emptyFilters.regions,
      figures: criteria.filters?.figures ?? emptyFilters.figures,
      primaryCategories:
        criteria.filters?.primaryCategories ?? emptyFilters.primaryCategories,
    },
  }
}

function toggleSelection<T>(values: T[], value: T): T[] {
  return values.includes(value)
    ? values.filter((candidate) => candidate !== value)
    : [...values, value]
}

function formatActiveCriteriaCount(
  criteria: HistoryEventQueryCriteria,
): string {
  const selectedCount = criteria.filters
    ? Object.values(criteria.filters).reduce(
        (count, values) => count + (values?.length ?? 0),
        0,
      )
    : 0
  const total = selectedCount + (criteria.searchTerm?.trim() ? 1 : 0)
  return total > 0 ? `${total} 项已启用` : '全部历史事件'
}

function formatCoordinate(coordinate: number): string {
  return formatHistoricalYear(coordinateToHistoricalYear(coordinate))
}

function replaceExplorationUrl(
  state: ExplorationUrlState,
  isDetailEntry?: boolean,
): void {
  writeExplorationUrl('replaceState', state, isDetailEntry)
}

function writeExplorationUrl(
  mode: 'pushState' | 'replaceState',
  state: ExplorationUrlState,
  isDetailEntry?: boolean,
): void {
  const currentState = isRecord(window.history.state)
    ? window.history.state
    : {}
  const currentExplorationState = isRecord(
    currentState[explorationHistoryStateKey],
  )
    ? currentState[explorationHistoryStateKey]
    : {}
  const detailEntry =
    isDetailEntry ?? currentExplorationState.isDetailEntry === true

  window.history[mode](
    {
      ...currentState,
      [explorationHistoryStateKey]: { isDetailEntry: detailEntry },
    },
    '',
    createExplorationUrl(window.location.href, state),
  )
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null
}

function isAbortError(error: unknown): boolean {
  return error instanceof Error && error.name === 'AbortError'
}

function useMediaQuery(query: string): boolean {
  const [matches, setMatches] = useState(() => {
    if (typeof window.matchMedia === 'function') {
      return window.matchMedia(query).matches
    }
    return window.innerWidth <= 820
  })

  useEffect(() => {
    if (typeof window.matchMedia !== 'function') return

    const mediaQuery = window.matchMedia(query)
    const updateMatch = () => setMatches(mediaQuery.matches)
    updateMatch()
    mediaQuery.addEventListener('change', updateMatch)
    return () => mediaQuery.removeEventListener('change', updateMatch)
  }, [query])

  return matches
}
