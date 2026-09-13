export type UserRole = 'editor' | 'administrator'

export interface CurrentUser {
  id: string
  email: string
  role: UserRole
}

export interface ManagedUser extends CurrentUser {
  disabledAt: string | null
  lockVersion: number
  createdAt: string
  updatedAt: string
}

interface CurrentUserResponse {
  user: CurrentUser
}

interface ProblemResponse {
  detail?: string
  code?: string
}

interface UserListResponse {
  users: ManagedUser[]
}

export class AuthenticationError extends Error {
  readonly code?: string
  readonly status: number

  constructor(message: string, status: number, code?: string) {
    super(message)
    this.name = 'AuthenticationError'
    this.status = status
    this.code = code
  }
}

export async function getCurrentUser(
  signal?: AbortSignal,
): Promise<CurrentUser | null> {
  const response = await fetch('/api/v1/auth/me', {
    credentials: 'same-origin',
    signal,
  })
  if (response.status === 401) return null
  return readCurrentUser(response)
}

export async function login(
  email: string,
  password: string,
): Promise<CurrentUser> {
  const response = await fetch('/api/v1/auth/login', {
    method: 'POST',
    credentials: 'same-origin',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ email, password }),
  })
  return readCurrentUser(response)
}

export async function logout(): Promise<void> {
  const csrfToken = readCookie('__Host-history_wiki_csrf')
  const response = await fetch('/api/v1/auth/logout', {
    method: 'POST',
    credentials: 'same-origin',
    headers: csrfToken ? { 'X-CSRF-Token': csrfToken } : undefined,
  })
  if (!response.ok) throw await responseError(response)
}

export async function changePassword(
  currentPassword: string,
  newPassword: string,
): Promise<void> {
  await writeRequest('/api/v1/auth/change-password', 'POST', {
    currentPassword,
    newPassword,
  })
}

export async function listUsers(signal?: AbortSignal): Promise<ManagedUser[]> {
  const response = await fetch('/api/v1/admin/users', {
    credentials: 'same-origin',
    signal,
  })
  if (!response.ok) throw await responseError(response)
  return ((await response.json()) as UserListResponse).users
}

export async function createUser(input: {
  email: string
  password: string
  role: UserRole
}): Promise<ManagedUser> {
  return (await writeRequest('/api/v1/admin/users', 'POST', input)) as ManagedUser
}

export async function updateUser(
  user: ManagedUser,
  input: { role: UserRole; disabled: boolean },
): Promise<ManagedUser> {
  return (await writeRequest(
    `/api/v1/admin/users/${encodeURIComponent(user.id)}`,
    'PATCH',
    input,
    { 'If-Match': `"${user.lockVersion}"` },
  )) as ManagedUser
}

export async function resetUserPassword(
  userId: string,
  password: string,
): Promise<void> {
  await writeRequest(
    `/api/v1/admin/users/${encodeURIComponent(userId)}/reset-password`,
    'POST',
    { password },
  )
}

async function readCurrentUser(response: Response): Promise<CurrentUser> {
  if (!response.ok) throw await responseError(response)
  const body = (await response.json()) as CurrentUserResponse
  return body.user
}

async function responseError(response: Response): Promise<AuthenticationError> {
  let problem: ProblemResponse = {}
  try {
    problem = (await response.json()) as ProblemResponse
  } catch {
    // Fall back to the status-derived message below.
  }
  return new AuthenticationError(
    problem.detail ?? `请求失败（${response.status}）`,
    response.status,
    problem.code,
  )
}

async function writeRequest(
  path: string,
  method: 'POST' | 'PATCH',
  body: unknown,
  extraHeaders: Record<string, string> = {},
): Promise<unknown> {
  const csrfToken = readCookie('__Host-history_wiki_csrf')
  const response = await fetch(path, {
    method,
    credentials: 'same-origin',
    headers: {
      'Content-Type': 'application/json',
      ...(csrfToken ? { 'X-CSRF-Token': csrfToken } : {}),
      ...extraHeaders,
    },
    body: JSON.stringify(body),
  })
  if (!response.ok) throw await responseError(response)
  if (response.status === 204) return undefined
  return response.json()
}

function readCookie(name: string): string | null {
  const prefix = `${name}=`
  const cookie = document.cookie
    .split(';')
    .map((item) => item.trim())
    .find((item) => item.startsWith(prefix))
  return cookie ? decodeURIComponent(cookie.slice(prefix.length)) : null
}
