import type { Region, RegionStatus } from './regionClient'

export type ContextEntityKind = 'place' | 'historical-period'

export interface ContextEntity {
  id: string
  name: string
  disambiguationLabel: string | null
  status: RegionStatus
  mergedIntoId: string | null
  regions: Pick<Region, 'id' | 'name' | 'disambiguationLabel'>[]
  lockVersion: number
  createdAt: string
  updatedAt: string
}

export interface ContextEntityWriteInput {
  name: string
  disambiguationLabel: string | null
  regionIds: string[]
}

interface ProblemResponse {
  detail?: string
  code?: string
}

export class ContextEntityManagementError extends Error {
  readonly code?: string
  readonly status: number

  constructor(message: string, status: number, code?: string) {
    super(message)
    this.name = 'ContextEntityManagementError'
    this.status = status
    this.code = code
  }
}

export async function listContextEntities(
  kind: ContextEntityKind,
  query = '',
  signal?: AbortSignal,
): Promise<ContextEntity[]> {
  const parameters = new URLSearchParams({ limit: '100' })
  if (query.trim()) parameters.set('q', query.trim())
  const response = await fetch(`${collectionPath(kind)}?${parameters}`, {
    credentials: 'same-origin',
    signal,
  })
  if (!response.ok) throw await responseError(response)
  const body = await response.json() as { places?: ContextEntity[]; periods?: ContextEntity[] }
  return kind === 'place' ? body.places ?? [] : body.periods ?? []
}

export async function createContextEntity(
  kind: ContextEntityKind,
  input: ContextEntityWriteInput,
  idempotencyKey: string,
): Promise<ContextEntity> {
  const response = await fetch(collectionPath(kind), {
    method: 'POST',
    credentials: 'same-origin',
    headers: managementHeaders({ 'Idempotency-Key': idempotencyKey }),
    body: JSON.stringify(input),
  })
  if (!response.ok) throw await responseError(response)
  return await response.json() as ContextEntity
}

export async function updateContextEntity(
  kind: ContextEntityKind,
  entity: ContextEntity,
  input: ContextEntityWriteInput & { status: Exclude<RegionStatus, 'merged'> },
): Promise<ContextEntity> {
  const response = await fetch(`${collectionPath(kind)}/${entity.id}`, {
    method: 'PATCH',
    credentials: 'same-origin',
    headers: managementHeaders({ 'If-Match': `"${entity.lockVersion}"` }),
    body: JSON.stringify(input),
  })
  if (!response.ok) throw await responseError(response)
  return await response.json() as ContextEntity
}

function collectionPath(kind: ContextEntityKind): string {
  return kind === 'place'
    ? '/api/v1/admin/places'
    : '/api/v1/admin/periods'
}

function managementHeaders(extra: Record<string, string>): Record<string, string> {
  const csrfToken = readCookie('history_wiki_csrf') ?? readCookie('__Host-history_wiki_csrf')
  return {
    'Content-Type': 'application/json',
    ...(csrfToken ? { 'X-CSRF-Token': csrfToken } : {}),
    ...extra,
  }
}

async function responseError(response: Response): Promise<ContextEntityManagementError> {
  let problem: ProblemResponse = {}
  try {
    problem = await response.json() as ProblemResponse
  } catch {
    // Fall back to the status-derived message below.
  }
  return new ContextEntityManagementError(
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
