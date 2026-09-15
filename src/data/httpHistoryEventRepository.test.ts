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

  it('默认在两侧预取更宽的事件窗口并返回覆盖范围', async () => {
    const fetch = vi.fn<typeof globalThis.fetch>().mockResolvedValue(
      jsonResponse({
        events: [], sourceTotal: 0, totalMatching: 0, returnedProminence: 3,
        coveredFrom: -200, coveredTo: 200,
      }),
    )
    const repository = createHttpHistoryEventRepository({
      fetch,
      paddingRatio: 0.5,
    })

    const result = await repository.query({
      visibleRange: { start: -100, end: 100 },
    })

    const url = new URL(String(fetch.mock.calls[0][0]), 'http://localhost')
    expect(url.searchParams.get('pad')).toBe('100')
    expect(result.coveredRange).toEqual({ start: -200, end: 200 })
  })

  it('显式 padding 覆盖默认比例，为 0 时不再请求预取', async () => {
    const fetch = vi.fn<typeof globalThis.fetch>().mockResolvedValue(
      jsonResponse({
        events: [], sourceTotal: 0, totalMatching: 0, returnedProminence: 3,
      }),
    )
    const repository = createHttpHistoryEventRepository({
      fetch,
      paddingRatio: 0.5,
    })

    await repository.query({
      visibleRange: { start: 0, end: 100 },
      padding: 0,
    })

    const url = new URL(String(fetch.mock.calls[0][0]), 'http://localhost')
    expect(url.searchParams.has('pad')).toBe(false)
  })

  it('预取窗口超限时回退到仅可视范围', async () => {
    const fetch = vi.fn<typeof globalThis.fetch>()
      .mockResolvedValueOnce(
        new Response(JSON.stringify({
          title: '查询结果过多', detail: '请缩小时间范围。', code: 'result_set_too_large',
        }), {
          status: 422,
          headers: { 'Content-Type': 'application/problem+json' },
        }),
      )
      .mockResolvedValueOnce(jsonResponse({
        events: [], sourceTotal: 0, totalMatching: 0, returnedProminence: 3,
      }))
    const repository = createHttpHistoryEventRepository({
      fetch,
      paddingRatio: 0.5,
    })

    const result = await repository.query({
      visibleRange: { start: 0, end: 10 },
    })

    expect(fetch).toHaveBeenCalledTimes(2)
    expect(
      new URL(String(fetch.mock.calls[0][0]), 'http://localhost').searchParams.get('pad'),
    ).toBe('5')
    expect(
      new URL(String(fetch.mock.calls[1][0]), 'http://localhost').searchParams.has('pad'),
    ).toBe(false)
    expect(result.totalMatching).toBe(0)
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
    const repository = createHttpHistoryEventRepository({
      fetch,
      paddingRatio: 0,
    })

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
