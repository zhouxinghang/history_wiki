import type {
  HistoryEventFilterMetadata,
  HistoryEventQueryCriteria,
  PrimaryCategory,
  TimeRange,
} from './history'

export interface ParsedExplorationUrlState {
  hasStateParameters: boolean
  viewport?: TimeRange
  queryCriteria: HistoryEventQueryCriteria
  selectedEventId?: string
}

export interface ExplorationUrlState {
  viewport: TimeRange
  queryCriteria: HistoryEventQueryCriteria
  selectedEventId?: string
}

const parameterNames = {
  start: 'from',
  end: 'to',
  searchTerm: 'q',
  periods: 'period',
  regions: 'region',
  figures: 'figure',
  primaryCategories: 'category',
  selectedEventId: 'event',
} as const

const stateParameterNames = new Set<string>(Object.values(parameterNames))
const primaryCategories = new Set<PrimaryCategory>([
  '政治',
  '军事',
  '文化',
  '科技',
  '社会',
  '交流',
])

export function parseExplorationUrlState(
  search: string,
): ParsedExplorationUrlState {
  const parameters = new URLSearchParams(search)
  const start = parseCoordinate(parameters.get(parameterNames.start))
  const end = parseCoordinate(parameters.get(parameterNames.end))
  const viewport =
    start !== undefined && end !== undefined && start < end
      ? { start, end }
      : undefined
  const selectedEventId = normalizeSingleValue(
    parameters.get(parameterNames.selectedEventId),
  )

  return {
    hasStateParameters: [...parameters.keys()].some((name) =>
      stateParameterNames.has(name),
    ),
    viewport,
    queryCriteria: {
      searchTerm: parameters.get(parameterNames.searchTerm) ?? '',
      filters: {
        periods: readRepeatedValues(parameters, parameterNames.periods),
        regions: readRepeatedValues(parameters, parameterNames.regions),
        figures: readRepeatedValues(parameters, parameterNames.figures),
        primaryCategories: readRepeatedValues(
          parameters,
          parameterNames.primaryCategories,
        ).filter(isPrimaryCategory),
      },
    },
    selectedEventId,
  }
}

export function validateUrlQueryCriteria(
  criteria: HistoryEventQueryCriteria,
  metadata: HistoryEventFilterMetadata,
): HistoryEventQueryCriteria {
  const allowedPeriods = new Set(
    metadata.periodGroups.flatMap((group) =>
      group.periods.map((period) => period.id),
    ),
  )

  return {
    searchTerm: criteria.searchTerm ?? '',
    filters: {
      periods: retainAllowed(criteria.filters?.periods, allowedPeriods),
      regions: retainAllowed(
        criteria.filters?.regions,
        new Set(metadata.regions.map((region) => region.id)),
      ),
      figures: retainAllowed(
        criteria.filters?.figures,
        new Set(metadata.figures.map((figure) => figure.id)),
      ),
      primaryCategories: retainAllowed(
        criteria.filters?.primaryCategories,
        new Set(metadata.primaryCategories),
      ),
    },
  }
}

export function normalizeUrlViewport(
  viewport: TimeRange | undefined,
  limits: TimeRange,
  minimumSpan: number,
): TimeRange {
  if (!viewport) return { ...limits }

  const maximumSpan = limits.end - limits.start
  if (maximumSpan <= minimumSpan) return { ...limits }

  const requestedSpan = viewport.end - viewport.start
  const span = Math.min(maximumSpan, Math.max(minimumSpan, requestedSpan))
  const center = viewport.start + requestedSpan / 2
  let start = center - span / 2
  let end = center + span / 2

  if (start < limits.start) {
    start = limits.start
    end = start + span
  }
  if (end > limits.end) {
    end = limits.end
    start = end - span
  }

  return { start, end }
}

export function createExplorationUrl(
  href: string,
  state: ExplorationUrlState,
): string {
  const url = new URL(href)
  const parameters = url.searchParams

  for (const name of stateParameterNames) parameters.delete(name)

  parameters.set(parameterNames.start, formatCoordinate(state.viewport.start))
  parameters.set(parameterNames.end, formatCoordinate(state.viewport.end))

  if (state.queryCriteria.searchTerm?.trim()) {
    parameters.set(parameterNames.searchTerm, state.queryCriteria.searchTerm)
  }

  appendValues(
    parameters,
    parameterNames.periods,
    state.queryCriteria.filters?.periods,
  )
  appendValues(
    parameters,
    parameterNames.regions,
    state.queryCriteria.filters?.regions,
  )
  appendValues(
    parameters,
    parameterNames.figures,
    state.queryCriteria.filters?.figures,
  )
  appendValues(
    parameters,
    parameterNames.primaryCategories,
    state.queryCriteria.filters?.primaryCategories,
  )

  if (state.selectedEventId) {
    parameters.set(parameterNames.selectedEventId, state.selectedEventId)
  }

  return `${url.pathname}${url.search}${url.hash}`
}

function parseCoordinate(value: string | null): number | undefined {
  if (value === null || value.trim() === '') return undefined
  const coordinate = Number(value)
  return Number.isFinite(coordinate) ? coordinate : undefined
}

function normalizeSingleValue(value: string | null): string | undefined {
  const normalized = value?.trim()
  return normalized ? normalized : undefined
}

function readRepeatedValues(
  parameters: URLSearchParams,
  name: string,
): string[] {
  return unique(
    parameters
      .getAll(name)
      .map((value) => value.trim())
      .filter(Boolean),
  )
}

function retainAllowed<T>(
  values: T[] | undefined,
  allowed: ReadonlySet<T>,
): T[] {
  return unique(values ?? []).filter((value) => allowed.has(value))
}

function unique<T>(values: T[]): T[] {
  return [...new Set(values)]
}

function isPrimaryCategory(value: string): value is PrimaryCategory {
  return primaryCategories.has(value as PrimaryCategory)
}

function appendValues(
  parameters: URLSearchParams,
  name: string,
  values: readonly string[] | undefined,
): void {
  for (const value of unique([...(values ?? [])])) {
    if (value) parameters.append(name, value)
  }
}

function formatCoordinate(coordinate: number): string {
  return Number(coordinate.toFixed(6)).toString()
}
