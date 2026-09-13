import type { EventDraftWriteInput } from './eventDraftClient'

export const MAX_EVENT_IMPORT_RECORDS = 10_000
export const MAX_EVENT_IMPORT_BYTES = 20 * 1024 * 1024

export interface EventImportRecord extends EventDraftWriteInput {
  id: string
  slug: string
}

export interface EventImportIssue {
  index: number
  field: string
  code: string
  detail: string
}

export interface EventImportValidation {
  valid: boolean
  total: number
  errors: EventImportIssue[]
}

export interface EventImportBatch {
  batchId: string
  importedCount: number
  eventIds: string[]
  createdAt: string
  replayed: boolean
}

interface ProblemResponse {
  detail?: string
  code?: string
  valid?: boolean
  total?: number
  errors?: EventImportIssue[]
}

export class EventImportError extends Error {
  readonly status: number
  readonly code?: string
  readonly validation?: EventImportValidation

  constructor(message: string, status: number, code?: string, validation?: EventImportValidation) {
    super(message)
    this.name = 'EventImportError'
    this.status = status
    this.code = code
    this.validation = validation
  }
}

export async function preflightEventImport(events: EventImportRecord[]): Promise<EventImportValidation> {
  const response = await writeImport('/api/v1/admin/event-imports/preflight', events)
  return await response.json() as EventImportValidation
}

export async function submitEventImport(events: EventImportRecord[], idempotencyKey: string): Promise<EventImportBatch> {
  const response = await writeImport('/api/v1/admin/event-imports', events, idempotencyKey)
  const batch = await response.json() as Omit<EventImportBatch, 'replayed'>
  return { ...batch, replayed: response.headers.get('Idempotency-Replayed') === 'true' }
}

async function writeImport(path: string, events: EventImportRecord[], idempotencyKey?: string): Promise<Response> {
  const csrfToken = readCookie('__Host-history_wiki_csrf')
  const response = await fetch(path, {
    method: 'POST',
    credentials: 'same-origin',
    headers: {
      'Content-Type': 'application/json',
      ...(csrfToken ? { 'X-CSRF-Token': csrfToken } : {}),
      ...(idempotencyKey ? { 'Idempotency-Key': idempotencyKey } : {}),
    },
    body: JSON.stringify({ events }),
  })
  if (!response.ok) throw await responseError(response)
  return response
}

async function responseError(response: Response): Promise<EventImportError> {
  let body: ProblemResponse = {}
  try {
    body = await response.json() as ProblemResponse
  } catch {
    // Fall back to the status-derived message below.
  }
  const validation = Array.isArray(body.errors)
    ? { valid: false, total: body.total ?? 0, errors: body.errors }
    : undefined
  return new EventImportError(body.detail ?? `请求失败（${response.status}）`, response.status, body.code, validation)
}

function readCookie(name: string): string | null {
  const prefix = `${name}=`
  const cookie = document.cookie
    .split(';')
    .map((item) => item.trim())
    .find((item) => item.startsWith(prefix))
  return cookie ? decodeURIComponent(cookie.slice(prefix.length)) : null
}
