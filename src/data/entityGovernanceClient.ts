import type { CanonicalEntityResource, ManagedCanonicalEntity } from './canonicalEntityClient'
import type { ContextEntityKind } from './contextEntityClient'

export interface EntityMergeImpact {
  draftCount: number
  revisionCount: number
  publishedEventCount: number
}

export type GovernedEntityResource = CanonicalEntityResource | ContextEntityKind

interface MergeResponse {
  entity: ManagedCanonicalEntity
  impact: EntityMergeImpact
}

interface ProblemResponse {
  detail?: string
  code?: string
}

export class EntityGovernanceError extends Error {
  readonly code?: string
  readonly status: number

  constructor(message: string, status: number, code?: string) {
    super(message)
    this.name = 'EntityGovernanceError'
    this.status = status
    this.code = code
  }
}

export async function getEntityMergeImpact(
  resource: GovernedEntityResource,
  entityID: string,
): Promise<EntityMergeImpact> {
  const response = await fetch(`${resourcePath(resource)}/${entityID}/merge-impact`, {
    credentials: 'same-origin',
  })
  if (!response.ok) throw await responseError(response)
  return await response.json() as EntityMergeImpact
}

export async function mergeEntity(
  resource: GovernedEntityResource,
  entity: Pick<ManagedCanonicalEntity, 'id' | 'lockVersion'>,
  targetID: string,
  confirmedImpact: EntityMergeImpact,
): Promise<MergeResponse> {
  const response = await fetch(`${resourcePath(resource)}/${entity.id}/merge`, {
    method: 'POST',
    credentials: 'same-origin',
    headers: managementHeaders({ 'If-Match': `"${entity.lockVersion}"` }),
    body: JSON.stringify({ targetId: targetID, confirmedImpact }),
  })
  if (!response.ok) throw await responseError(response)
  return await response.json() as MergeResponse
}

function resourcePath(resource: GovernedEntityResource): string {
  if (resource === 'place') return '/api/v1/admin/places'
  if (resource === 'historical-period') return '/api/v1/admin/periods'
  return `/api/v1/admin/${resource}`
}

function managementHeaders(extra: Record<string, string>): Record<string, string> {
  const csrfToken = readCookie('history_wiki_csrf') ?? readCookie('__Host-history_wiki_csrf')
  return {
    'Content-Type': 'application/json',
    ...(csrfToken ? { 'X-CSRF-Token': csrfToken } : {}),
    ...extra,
  }
}

async function responseError(response: Response): Promise<EntityGovernanceError> {
  let problem: ProblemResponse = {}
  try {
    problem = await response.json() as ProblemResponse
  } catch {
    // Fall back to the status-derived message below.
  }
  return new EntityGovernanceError(
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
