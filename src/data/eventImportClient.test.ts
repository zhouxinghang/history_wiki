import { beforeEach, describe, expect, it, vi } from 'vitest'
import { preflightEventImport, submitEventImport, type EventImportRecord } from './eventImportClient'

const records: EventImportRecord[] = [{
  id: '018f6f4c-38f8-7f1f-8f47-5aa4e1c2a311', slug: 'test-event', title: '', summary: '', narrative: '',
  time: null, primaryCategory: null, prominence: null, displayOrder: 1000,
  regionIds: [], placeIds: [], periodIds: [], figureIds: [], topicTagIds: [],
}]

describe('eventImportClient', () => {
  beforeEach(() => {
    document.cookie = '__Host-history_wiki_csrf=csrf-token; path=/'
    vi.restoreAllMocks()
  })

  it('preflights without an idempotency key', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response(JSON.stringify({ valid: true, total: 1, errors: [] }), { status: 200 }))
    await expect(preflightEventImport(records)).resolves.toMatchObject({ valid: true, total: 1 })
    const [, init] = fetchMock.mock.calls[0]
    expect(init?.headers).not.toHaveProperty('Idempotency-Key')
  })

  it('submits with an idempotency key and exposes replay headers', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response(JSON.stringify({
      batchId: 'batch-id', importedCount: 1, eventIds: [records[0].id], createdAt: '2026-09-12T18:00:00Z',
    }), { status: 201, headers: { 'Idempotency-Replayed': 'true' } }))
    await expect(submitEventImport(records, 'stable-key')).resolves.toMatchObject({ batchId: 'batch-id', replayed: true })
    const [, init] = fetchMock.mock.calls[0]
    expect(init?.headers).toMatchObject({ 'Idempotency-Key': 'stable-key' })
  })
})
