export type CanonicalEntityStatus = 'active' | 'inactive' | 'merged'
export type CanonicalEntityResource = 'regions' | 'figures' | 'topic-tags'
export type CanonicalEntityListField = 'regions' | 'figures' | 'topicTags'

export interface ManagedCanonicalEntity {
  id: string
  name: string
  disambiguationLabel: string | null
  status: CanonicalEntityStatus
  mergedIntoId: string | null
  lockVersion: number
  createdAt: string
  updatedAt: string
}

export interface CanonicalEntityWriteInput {
  name: string
  disambiguationLabel: string | null
}

interface ProblemResponse {
  detail?: string
  code?: string
}

export class CanonicalEntityManagementError extends Error {
  readonly code?: string
  readonly status: number

  constructor(message: string, status: number, code?: string) {
    super(message)
    this.name = 'CanonicalEntityManagementError'
    this.status = status
    this.code = code
  }
}

export async function listCanonicalEntities(
  resource: CanonicalEntityResource,
  listField: CanonicalEntityListField,
  query = '',
  signal?: AbortSignal,
): Promise<ManagedCanonicalEntity[]> {
  const parameters = new URLSearchParams({ limit: '100' })
  if (query.trim()) parameters.set('q', query.trim())
  const response = await fetch(`/api/v1/admin/${resource}?${parameters}`, {
    credentials: 'same-origin',
    signal,
  })
  if (!response.ok) throw await responseError(response)
  const body = (await response.json()) as Record<CanonicalEntityListField, ManagedCanonicalEntity[]>
  return body[listField]
}

export async function createCanonicalEntity(
  resource: CanonicalEntityResource,
  input: CanonicalEntityWriteInput,
  idempotencyKey: string,
): Promise<ManagedCanonicalEntity> {
  const response = await fetch(`/api/v1/admin/${resource}`, {
    method: 'POST',
    credentials: 'same-origin',
    headers: managementHeaders({ 'Idempotency-Key': idempotencyKey }),
    body: JSON.stringify(input),
  })
  if (!response.ok) throw await responseError(response)
  return (await response.json()) as ManagedCanonicalEntity
}

export async function updateCanonicalEntity(
  resource: CanonicalEntityResource,
  entity: ManagedCanonicalEntity,
  input: CanonicalEntityWriteInput & { status: Exclude<CanonicalEntityStatus, 'merged'> },
): Promise<ManagedCanonicalEntity> {
  const response = await fetch(`/api/v1/admin/${resource}/${entity.id}`, {
    method: 'PATCH',
    credentials: 'same-origin',
    headers: managementHeaders({ 'If-Match': `"${entity.lockVersion}"` }),
    body: JSON.stringify(input),
  })
  if (!response.ok) throw await responseError(response)
  return (await response.json()) as ManagedCanonicalEntity
}

export function newIdempotencyKey(): string {
  return globalThis.crypto.randomUUID()
}

function managementHeaders(extra: Record<string, string>): Record<string, string> {
  const csrfToken = readCookie('__Host-history_wiki_csrf')
  return {
    'Content-Type': 'application/json',
    ...(csrfToken ? { 'X-CSRF-Token': csrfToken } : {}),
    ...extra,
  }
}

async function responseError(response: Response): Promise<CanonicalEntityManagementError> {
  let problem: ProblemResponse = {}
  try {
    problem = (await response.json()) as ProblemResponse
  } catch {
    // Fall back to the status-derived message below.
  }
  return new CanonicalEntityManagementError(
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
