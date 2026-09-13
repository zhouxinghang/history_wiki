import type { EventProminence, PrimaryCategory, TimeExpression } from '../domain/history'

export interface EventEntityReference {
  id: string
  name: string
  disambiguationLabel: string | null
}

export interface ManagedEventDraft {
  title: string
  summary: string
  narrative: string
  time: TimeExpression | null
  primaryCategory: PrimaryCategory | null
  prominence: EventProminence | null
  displayOrder: number
  regions: EventEntityReference[]
  places: EventEntityReference[]
  periods: EventEntityReference[]
  figures: EventEntityReference[]
  topicTags: EventEntityReference[]
  lockVersion: number
  basedOnRevisionNo: number | null
  createdAt: string
  updatedAt: string
}

export interface EventRevisionPublisher {
  id: string
  email: string
}

export interface ManagedEventRevisionSummary {
  id: string
  revisionNo: number
  title: string
  publishedBy: EventRevisionPublisher
  publishedAt: string
  current: boolean
}

export interface ManagedEventRevision extends ManagedEventRevisionSummary {
  eventId: string
  summary: string
  narrative: string
  time: TimeExpression
  primaryCategory: PrimaryCategory
  prominence: EventProminence
  displayOrder: number
  regions: EventEntityReference[]
  places: EventEntityReference[]
  periods: EventEntityReference[]
  figures: EventEntityReference[]
  topicTags: EventEntityReference[]
}

export interface ManagedEvent {
  id: string
  slug: string
  publicationStatus: 'unpublished' | 'published' | 'archived'
  lockVersion: number
  draft: ManagedEventDraft | null
  createdAt: string
  updatedAt: string
}

export interface EventDraftWriteInput {
  title: string
  summary: string
  narrative: string
  time: TimeExpression | null
  primaryCategory: PrimaryCategory | null
  prominence: EventProminence | null
  displayOrder: number
  regionIds: string[]
  placeIds: string[]
  periodIds: string[]
  figureIds: string[]
  topicTagIds: string[]
}

interface ProblemResponse {
  detail?: string
  code?: string
}

export class EventDraftManagementError extends Error {
  readonly code?: string
  readonly status: number

  constructor(message: string, status: number, code?: string) {
    super(message)
    this.name = 'EventDraftManagementError'
    this.status = status
    this.code = code
  }
}

export async function listManagedEvents(signal?: AbortSignal): Promise<ManagedEvent[]> {
  const response = await fetch('/api/v1/admin/events', {
    credentials: 'same-origin',
    signal,
  })
  if (!response.ok) throw await responseError(response)
  return ((await response.json()) as { events: ManagedEvent[] }).events
}

export async function createManagedEvent(
  slug: string,
  draft: EventDraftWriteInput,
  idempotencyKey: string,
): Promise<ManagedEvent> {
  return writeEvent('/api/v1/admin/events', 'POST', { slug, ...draft }, {
    'Idempotency-Key': idempotencyKey,
  })
}

export async function createManagedEventDraft(eventId: string): Promise<ManagedEvent> {
  return writeEvent(`/api/v1/admin/events/${eventId}/draft`, 'POST', undefined, {})
}

export async function publishManagedEvent(event: ManagedEvent): Promise<void> {
  if (!event.draft) throw new Error('历史事件没有可发布的活动草稿。')
  await writeEventRequest(`/api/v1/admin/events/${event.id}/publish`, 'POST', undefined, {
    'If-Match': `"draft-${event.draft.lockVersion}"`,
  })
}

export async function archiveManagedEvent(event: ManagedEvent): Promise<ManagedEvent> {
  const response = await writeEventRequest(`/api/v1/admin/events/${event.id}/archive`, 'POST', undefined, {
    'If-Match': `"event-${event.lockVersion}"`,
  })
  return await response.json() as ManagedEvent
}

export async function listManagedEventRevisions(
  eventId: string,
  signal?: AbortSignal,
): Promise<ManagedEventRevisionSummary[]> {
  const response = await fetch(`/api/v1/admin/events/${eventId}/revisions`, {
    credentials: 'same-origin',
    signal,
  })
  if (!response.ok) throw await responseError(response)
  return ((await response.json()) as { revisions: ManagedEventRevisionSummary[] }).revisions
}

export async function getManagedEventRevision(
  eventId: string,
  revisionNo: number,
  signal?: AbortSignal,
): Promise<ManagedEventRevision> {
  const response = await fetch(`/api/v1/admin/events/${eventId}/revisions/${revisionNo}`, {
    credentials: 'same-origin',
    signal,
  })
  if (!response.ok) throw await responseError(response)
  return await response.json() as ManagedEventRevision
}

export async function restoreManagedEventRevision(eventId: string, revisionNo: number): Promise<ManagedEvent> {
  return writeEvent(`/api/v1/admin/events/${eventId}/revisions/${revisionNo}/restore`, 'POST', undefined, {})
}

export async function updateManagedEventSlug(event: ManagedEvent, slug: string): Promise<ManagedEvent> {
  return writeEvent(`/api/v1/admin/events/${event.id}`, 'PATCH', { slug }, {
    'If-Match': `"event-${event.lockVersion}"`,
  })
}

export async function updateManagedEventDraft(
  event: ManagedEvent,
  draft: EventDraftWriteInput,
): Promise<ManagedEvent> {
  if (!event.draft) throw new Error('历史事件没有可编辑的活动草稿。')
  return writeEvent(`/api/v1/admin/events/${event.id}/draft`, 'PATCH', draft, {
    'If-Match': `"draft-${event.draft.lockVersion}"`,
  })
}

async function writeEvent(
  path: string,
  method: 'POST' | 'PATCH',
  body: unknown | undefined,
  extraHeaders: Record<string, string>,
): Promise<ManagedEvent> {
  const response = await writeEventRequest(path, method, body, extraHeaders)
  return await response.json() as ManagedEvent
}

async function writeEventRequest(
  path: string,
  method: 'POST' | 'PATCH',
  body: unknown | undefined,
  extraHeaders: Record<string, string>,
): Promise<Response> {
  const csrfToken = readCookie('history_wiki_csrf') ?? readCookie('__Host-history_wiki_csrf')
  const response = await fetch(path, {
    method,
    credentials: 'same-origin',
    headers: {
      ...(body === undefined ? {} : { 'Content-Type': 'application/json' }),
      ...(csrfToken ? { 'X-CSRF-Token': csrfToken } : {}),
      ...extraHeaders,
    },
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  if (!response.ok) throw await responseError(response)
  return response
}

async function responseError(response: Response): Promise<EventDraftManagementError> {
  let problem: ProblemResponse = {}
  try {
    problem = await response.json() as ProblemResponse
  } catch {
    // Fall back to the status-derived message below.
  }
  return new EventDraftManagementError(
    problem.detail ?? `请求失败（${response.status}）`,
    response.status,
    problem.code,
  )
}

function readCookie(name: string): string | null {
  const prefix = `${name}=`
  const cookie = document.cookie
    .split(';')
    .map((item) => item.trim())
    .find((item) => item.startsWith(prefix))
  return cookie ? decodeURIComponent(cookie.slice(prefix.length)) : null
}
