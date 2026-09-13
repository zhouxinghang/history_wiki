import { createHttpHistoryEventRepository, HttpRepositoryError } from './httpHistoryEventRepository'
import { HistoryEventQueryTooLargeError } from '../domain/history'

describe('HTTP History Event Repository', () => {
  it('把空数据库边界映射为 null，并保留明确的空查询结果', async () => {
    const fetch = vi.fn<typeof globalThis.fetch>()
      .mockResolvedValueOnce(jsonResponse({ hasEvents: false, start: null, end: null }))
      .mockResolvedValueOnce(jsonResponse({
        periodGroups: [],
        regions: [],
        figures: [],
        primaryCategories: [],
      }))
      .mockResolvedValueOnce(jsonResponse({
        events: [],
        sourceTotal: 0,
        totalMatching: 0,
        returnedProminence: 3,
      }))
    const repository = createHttpHistoryEventRepository({ fetch })

    await expect(repository.getBounds()).resolves.toBeNull()
    await expect(repository.getFilterMetadata()).resolves.toEqual({
      periodGroups: [],
      regions: [],
      figures: [],
      primaryCategories: [],
    })
    await expect(
      repository.query({ visibleRange: { start: -100, end: 100 } }),
    ).resolves.toEqual({
      events: [],
      sourceTotal: 0,
      totalMatching: 0,
      returnedProminence: 3,
    })
  })

  it('把范围、搜索和重复筛选条件编码到公开 API', async () => {
    const fetch = vi.fn<typeof globalThis.fetch>().mockResolvedValue(
      jsonResponse({
        events: [], sourceTotal: 0, totalMatching: 0, returnedProminence: 2,
      }),
    )
    const repository = createHttpHistoryEventRepository({
      baseUrl: 'https://history.example/',
      fetch,
    })

    await repository.query({
      visibleRange: { start: -220.1234567, end: 300 },
      searchTerm: ' 统一 ',
      filters: {
        periods: ['period-a', 'period-b'],
        regions: ['region-a'],
        figures: ['figure-a'],
        primaryCategories: ['政治', '军事'],
      },
    })

    const url = new URL(String(fetch.mock.calls[0][0]))
    expect(`${url.origin}${url.pathname}`).toBe('https://history.example/api/v1/events')
    expect(url.searchParams.get('from')).toBe('-220.123457')
    expect(url.searchParams.get('to')).toBe('300')
    expect(url.searchParams.get('q')).toBe('统一')
    expect(url.searchParams.getAll('period')).toEqual(['period-a', 'period-b'])
    expect(url.searchParams.getAll('category')).toEqual(['政治', '军事'])
  })

  it('透传 AbortSignal，并把 Problem Details 转换为可处理错误', async () => {
    const controller = new AbortController()
    const fetch = vi.fn<typeof globalThis.fetch>().mockResolvedValue(
      new Response(JSON.stringify({
        title: '查询结果过多',
        detail: '请缩小时间范围。',
        code: 'result_set_too_large',
      }), {
        status: 422,
        headers: { 'Content-Type': 'application/problem+json' },
      }),
    )
    const repository = createHttpHistoryEventRepository({ fetch })

    const promise = repository.query({
      visibleRange: { start: 0, end: 1 },
      signal: controller.signal,
    })
    await expect(promise).rejects.toBeInstanceOf(HistoryEventQueryTooLargeError)
    await expect(promise).rejects.toMatchObject({
      message: '请缩小时间范围。',
      status: 422,
      code: 'result_set_too_large',
    } satisfies Partial<HttpRepositoryError>)
    expect(fetch.mock.calls[0][1]?.signal).toBe(controller.signal)
  })
})

function jsonResponse(body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { 'Content-Type': 'application/json' },
  })
}
