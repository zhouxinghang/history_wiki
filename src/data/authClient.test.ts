import { afterEach, describe, expect, it, vi } from 'vitest'
import { updateUser, type ManagedUser } from './authClient'

afterEach(() => {
  vi.unstubAllGlobals()
  document.cookie = '__Host-history_wiki_csrf=; Max-Age=0; Path=/; Secure'
})

describe('account update concurrency', () => {
  it('sends the managed user lock version as a strong If-Match ETag', async () => {
    document.cookie = '__Host-history_wiki_csrf=csrf-token; Path=/; Secure'
    const user: ManagedUser = {
      id: '018f47da-34be-7f11-8000-123456789abc',
      email: 'editor@example.com',
      role: 'editor',
      disabledAt: null,
      lockVersion: 7,
      createdAt: '2026-09-12T00:00:00Z',
      updatedAt: '2026-09-12T00:00:00Z',
    }
    const updated = { ...user, disabledAt: '2026-09-12T01:00:00Z', lockVersion: 8 }
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(new Response(JSON.stringify(updated), {
      status: 200,
      headers: { 'Content-Type': 'application/json', ETag: '"8"' },
    }))
    vi.stubGlobal('fetch', fetchMock)

    await expect(updateUser(user, { role: 'editor', disabled: true })).resolves.toEqual(updated)
    expect(fetchMock).toHaveBeenCalledWith(
      '/api/v1/admin/users/018f47da-34be-7f11-8000-123456789abc',
      expect.objectContaining({
        method: 'PATCH',
        headers: {
          'Content-Type': 'application/json',
          'If-Match': '"7"',
          'X-CSRF-Token': 'csrf-token',
        },
      }),
    )
  })
})
