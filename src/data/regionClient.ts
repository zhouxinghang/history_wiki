import { randomUUID } from './randomUUID'

export type RegionStatus = 'active' | 'inactive' | 'merged'

export interface Region {
  id: string
  name: string
  disambiguationLabel: string | null
  status: RegionStatus
  mergedIntoId: string | null
  lockVersion: number
  createdAt: string
  updatedAt: string
}

export interface RegionWriteInput {
  name: string
  disambiguationLabel: string | null
}

interface RegionListResponse {
  regions: Region[]
}

interface ProblemResponse {
  detail?: string
  code?: string
}

export class RegionManagementError extends Error {
  readonly code?: string
  readonly status: number

  constructor(message: string, status: number, code?: string) {
    super(message)
    this.name = 'RegionManagementError'
    this.status = status
    this.code = code
  }
}

export async function listRegions(
  query = '',
  signal?: AbortSignal,
  status?: RegionStatus,
): Promise<Region[]> {
  const parameters = new URLSearchParams({ limit: '100' })
  if (query.trim()) parameters.set('q', query.trim())
  if (status) parameters.set('status', status)
  const response = await fetch(`/api/v1/admin/regions?${parameters}`, {
    credentials: 'same-origin',
    signal,
  })
  if (!response.ok) throw await responseError(response)
  return ((await response.json()) as RegionListResponse).regions
}

export async function createRegion(
  input: RegionWriteInput,
  idempotencyKey: string,
): Promise<Region> {
  const response = await fetch('/api/v1/admin/regions', {
    method: 'POST',
    credentials: 'same-origin',
    headers: managementHeaders({ 'Idempotency-Key': idempotencyKey }),
    body: JSON.stringify(input),
  })
  if (!response.ok) throw await responseError(response)
  return (await response.json()) as Region
}

export async function updateRegion(
  region: Region,
  input: RegionWriteInput & { status: Exclude<RegionStatus, 'merged'> },
): Promise<Region> {
  const response = await fetch(`/api/v1/admin/regions/${region.id}`, {
    method: 'PATCH',
    credentials: 'same-origin',
    headers: managementHeaders({ 'If-Match': `"${region.lockVersion}"` }),
    body: JSON.stringify(input),
  })
  if (!response.ok) throw await responseError(response)
  return (await response.json()) as Region
}

export function newIdempotencyKey(): string {
  return randomUUID()
}

function managementHeaders(extra: Record<string, string>): Record<string, string> {
  const csrfToken = readCookie('history_wiki_csrf') ?? readCookie('__Host-history_wiki_csrf')
  return {
    'Content-Type': 'application/json',
    ...(csrfToken ? { 'X-CSRF-Token': csrfToken } : {}),
    ...extra,
  }
}

async function responseError(response: Response): Promise<RegionManagementError> {
  let problem: ProblemResponse = {}
  try {
    problem = (await response.json()) as ProblemResponse
  } catch {
    // Fall back to the status-derived message below.
  }
  return new RegionManagementError(
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
