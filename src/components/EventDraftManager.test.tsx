import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, vi } from 'vitest'
import EventDraftManager from './EventDraftManager'

afterEach(() => {
  vi.unstubAllGlobals()
  document.cookie = '__Host-history_wiki_csrf=; Max-Age=0; Path=/; Secure'
})

describe('历史事件活动草稿管理', () => {
  it('创建不完整草稿并用稳定 ID 提交全部规范实体关联', async () => {
    document.cookie = '__Host-history_wiki_csrf=csrf-token; Path=/; Secure'
    const ids = {
      region: '00000000-0000-7000-8000-000000000001',
      place: '00000000-0000-7000-8000-000000000002',
      period: '00000000-0000-7000-8000-000000000003',
      figure: '00000000-0000-7000-8000-000000000004',
      tag: '00000000-0000-7000-8000-000000000005',
      event: '00000000-0000-7000-8000-000000000006',
    }
    const fetchMock = vi.fn<typeof fetch>(async (input, init) => {
      if (input === '/api/v1/admin/events' && !init?.method) return jsonResponse({ events: [] })
      if (input === '/api/v1/admin/regions?limit=100&status=active') return jsonResponse({ regions: [entity(ids.region, '东亚')] })
      if (input === '/api/v1/admin/places?limit=100') return jsonResponse({ places: [entity(ids.place, '长安')] })
      if (input === '/api/v1/admin/periods?limit=100') return jsonResponse({ periods: [entity(ids.period, '秦代')] })
      if (input === '/api/v1/admin/figures?limit=100') return jsonResponse({ figures: [entity(ids.figure, '秦始皇')] })
      if (input === '/api/v1/admin/topic-tags?limit=100') return jsonResponse({ topicTags: [entity(ids.tag, '统一')] })
      if (input === '/api/v1/admin/events' && init?.method === 'POST') {
        return new Response(JSON.stringify(managedEvent(ids.event, 'qin-unification', 1)), {
          status: 201,
          headers: { 'Content-Type': 'application/json' },
        })
      }
      throw new Error(`unexpected request: ${String(input)}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    vi.stubGlobal('crypto', { randomUUID: () => 'request-key' })
    const user = userEvent.setup()

    render(<EventDraftManager />)
    expect(await screen.findByText('尚未创建历史事件草稿。')).toBeVisible()
    await user.click(screen.getByRole('button', { name: '创建历史事件' }))
    await screen.findByText('东亚')

    await user.type(screen.getByLabelText('slug'), 'qin-unification')
    await user.type(screen.getByLabelText('标题（草稿可暂缺）'), '秦统一六国')
    await user.selectOptions(screen.getByLabelText('时间表述'), 'exact-date')
    await user.selectOptions(screen.getByLabelText('时间纪元'), 'BCE')
    await user.clear(screen.getByLabelText('时间年份'))
    await user.type(screen.getByLabelText('时间年份'), '221')
    await user.clear(screen.getByLabelText('月'))
    await user.type(screen.getByLabelText('月'), '10')
    await user.clear(screen.getByLabelText('日'))
    await user.type(screen.getByLabelText('日'), '1')
    for (const name of ['东亚', '长安', '秦代', '秦始皇', '统一']) {
      await user.click(screen.getByRole('checkbox', { name }))
    }
    await user.click(screen.getByRole('button', { name: '保存活动草稿' }))

    await waitFor(() => expect(fetchMock).toHaveBeenCalledWith('/api/v1/admin/events', expect.objectContaining({
      method: 'POST',
      headers: expect.objectContaining({
        'Idempotency-Key': 'request-key',
        'X-CSRF-Token': 'csrf-token',
      }),
    })))
    const createCall = fetchMock.mock.calls.find(([, init]) => init?.method === 'POST')
    const body = JSON.parse(String(createCall?.[1]?.body))
    expect(body).toMatchObject({
      slug: 'qin-unification',
      title: '秦统一六国',
      time: { kind: 'exact-date', date: { era: 'BCE', year: 221, month: 10, day: 1 } },
      regionIds: [ids.region], placeIds: [ids.place], periodIds: [ids.period],
      figureIds: [ids.figure], topicTagIds: [ids.tag],
    })
  })

  it('并发冲突后放弃陈旧编辑并刷新列表', async () => {
    const existing = managedEvent('00000000-0000-7000-8000-000000000006', 'old-slug', 3)
    let listCount = 0
    const fetchMock = vi.fn<typeof fetch>(async (input, init) => {
      if (input === '/api/v1/admin/events' && !init?.method) {
        listCount += 1
        return jsonResponse({ events: [existing] })
      }
      if (String(input).includes('/api/v1/admin/regions')) return jsonResponse({ regions: [] })
      if (String(input).includes('/api/v1/admin/places')) return jsonResponse({ places: [] })
      if (String(input).includes('/api/v1/admin/periods')) return jsonResponse({ periods: [] })
      if (String(input).includes('/api/v1/admin/figures')) return jsonResponse({ figures: [] })
      if (String(input).includes('/api/v1/admin/topic-tags')) return jsonResponse({ topicTags: [] })
      if (String(input).endsWith('/draft') && init?.method === 'PATCH') {
        return problemResponse(409, 'event_draft_version_conflict', '活动草稿已被修改')
      }
      throw new Error(`unexpected request: ${String(input)}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    const user = userEvent.setup()

    render(<EventDraftManager />)
    await user.click(await screen.findByRole('button', { name: '编辑草稿' }))
    await screen.findByRole('heading', { name: '编辑活动草稿' })
    await user.click(screen.getByRole('button', { name: '保存活动草稿' }))

    expect(await screen.findByText(/已被其他编辑者修改/)).toBeVisible()
    expect(screen.queryByRole('heading', { name: '编辑活动草稿' })).not.toBeInTheDocument()
    expect(listCount).toBe(2)
    expect(fetchMock).toHaveBeenCalledWith(`/api/v1/admin/events/${existing.id}/draft`, expect.objectContaining({
      method: 'PATCH',
      headers: expect.objectContaining({ 'If-Match': '"draft-3"' }),
    }))
  })

  it('在管理区查看发布人和时间，并将旧版本复制为活动草稿', async () => {
    document.cookie = '__Host-history_wiki_csrf=csrf-token; Path=/; Secure'
    const eventID = '00000000-0000-7000-8000-000000000006'
    const regionID = '00000000-0000-7000-8000-000000000001'
    const published = { ...managedEvent(eventID, 'qin-history', 1), publicationStatus: 'published' as const, draft: null }
    const restored = {
      ...published,
      draft: {
        ...managedEvent(eventID, 'qin-history', 1).draft,
        basedOnRevisionNo: 1,
        regions: [{ id: regionID, name: '东亚（当前名称）', disambiguationLabel: null }],
      },
    }
    const fetchMock = vi.fn<typeof fetch>(async (input, init) => {
      if (input === '/api/v1/admin/events' && !init?.method) return jsonResponse({ events: [published] })
      if (input === `/api/v1/admin/events/${eventID}/revisions`) return jsonResponse({
        revisions: [{
          id: '00000000-0000-7000-8000-000000000010', revisionNo: 1, title: '秦统一六国',
          publishedBy: { id: '00000000-0000-7000-8000-000000000020', email: 'publisher@example.com' },
          publishedAt: '2026-09-13T01:00:00Z', current: true,
        }],
      })
      if (input === `/api/v1/admin/events/${eventID}/revisions/1` && !init?.method) return jsonResponse({
        id: '00000000-0000-7000-8000-000000000010', eventId: eventID, revisionNo: 1,
        title: '秦统一六国', summary: '摘要', narrative: '正文',
        time: { kind: 'year', year: { era: 'BCE', year: 221 } }, primaryCategory: '政治',
        prominence: 1, displayOrder: 20,
        regions: [{ id: regionID, name: '东亚（当前名称）', disambiguationLabel: null }],
        places: [], periods: [], figures: [], topicTags: [],
        publishedBy: { id: '00000000-0000-7000-8000-000000000020', email: 'publisher@example.com' },
        publishedAt: '2026-09-13T01:00:00Z', current: true,
      })
      if (input === `/api/v1/admin/events/${eventID}/revisions/1/restore` && init?.method === 'POST') {
        return new Response(JSON.stringify(restored), { status: 201, headers: { 'Content-Type': 'application/json' } })
      }
      if (String(input).includes('/api/v1/admin/regions')) return jsonResponse({ regions: [entity(regionID, '东亚（当前名称）')] })
      if (String(input).includes('/api/v1/admin/places')) return jsonResponse({ places: [] })
      if (String(input).includes('/api/v1/admin/periods')) return jsonResponse({ periods: [] })
      if (String(input).includes('/api/v1/admin/figures')) return jsonResponse({ figures: [] })
      if (String(input).includes('/api/v1/admin/topic-tags')) return jsonResponse({ topicTags: [] })
      throw new Error(`unexpected request: ${String(input)}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    const user = userEvent.setup()

    render(<EventDraftManager />)
    await user.click(await screen.findByRole('button', { name: '管理版本' }))
    expect(await screen.findByText(/publisher@example.com/)).toBeVisible()
    expect(screen.getByText('版本 1（当前）')).toBeVisible()

    await user.click(screen.getByRole('button', { name: '查看版本' }))
    expect(await screen.findByText('地区：')).toBeVisible()
    expect(screen.getByText(/东亚（当前名称）/)).toBeVisible()

    await user.click(screen.getByRole('button', { name: '恢复为活动草稿' }))
    expect(await screen.findByText(/复制为活动草稿/)).toBeVisible()
    expect(screen.getByRole('heading', { name: '编辑活动草稿' })).toBeVisible()
    expect(fetchMock).toHaveBeenCalledWith(`/api/v1/admin/events/${eventID}/revisions/1/restore`, expect.objectContaining({
      method: 'POST',
      headers: expect.objectContaining({ 'X-CSRF-Token': 'csrf-token' }),
    }))
  })

  it('仅向管理员提供下线和重新发布操作，并提交对应并发版本', async () => {
    document.cookie = '__Host-history_wiki_csrf=csrf-token; Path=/; Secure'
    const eventID = '00000000-0000-7000-8000-000000000006'
    const published = {
      ...managedEvent(eventID, 'archive-republish', 2),
      publicationStatus: 'published' as const,
      lockVersion: 2,
    }
    const archived = {
      ...published,
      publicationStatus: 'archived' as const,
      lockVersion: 3,
    }
    const republished = {
      ...archived,
      publicationStatus: 'published' as const,
      lockVersion: 4,
      draft: null,
    }
    let listCount = 0
    const fetchMock = vi.fn<typeof fetch>(async (input, init) => {
      if (input === '/api/v1/admin/events' && !init?.method) {
        listCount += 1
        return jsonResponse({ events: [listCount === 1 ? published : listCount === 2 ? archived : republished] })
      }
      if (input === `/api/v1/admin/events/${eventID}/archive` && init?.method === 'POST') {
        return jsonResponse(archived)
      }
      if (input === `/api/v1/admin/events/${eventID}/publish` && init?.method === 'POST') {
        return jsonResponse({ id: eventID, title: '重新发布版本' })
      }
      throw new Error(`unexpected request: ${String(input)}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    const user = userEvent.setup()

    render(<EventDraftManager userRole="administrator" />)
    await user.click(await screen.findByRole('button', { name: '下线事件' }))
    expect(await screen.findByRole('status')).toHaveTextContent('已下线')
    expect(fetchMock).toHaveBeenCalledWith(`/api/v1/admin/events/${eventID}/archive`, expect.objectContaining({
      method: 'POST',
      headers: expect.objectContaining({
        'If-Match': '"event-2"',
        'X-CSRF-Token': 'csrf-token',
      }),
    }))

    await user.click(await screen.findByRole('button', { name: '重新发布草稿' }))
    expect(await screen.findByText(/重新发布为新的事件版本/)).toBeVisible()
    expect(fetchMock).toHaveBeenCalledWith(`/api/v1/admin/events/${eventID}/publish`, expect.objectContaining({
      method: 'POST',
      headers: expect.objectContaining({
        'If-Match': '"draft-2"',
        'X-CSRF-Token': 'csrf-token',
      }),
    }))
  })

  it('编辑者看不到发布和下线操作', async () => {
    const eventID = '00000000-0000-7000-8000-000000000006'
    const published = {
      ...managedEvent(eventID, 'editor-view', 1),
      publicationStatus: 'published' as const,
      lockVersion: 2,
    }
    vi.stubGlobal('fetch', vi.fn<typeof fetch>().mockResolvedValue(jsonResponse({ events: [published] })))

    render(<EventDraftManager userRole="editor" />)

    expect(await screen.findByRole('button', { name: '编辑草稿' })).toBeVisible()
    expect(screen.queryByRole('button', { name: '发布草稿' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: '下线事件' })).not.toBeInTheDocument()
  })
})

function entity(id: string, name: string) {
  return {
    id, name, disambiguationLabel: null, status: 'active', lockVersion: 1,
    regions: [], createdAt: '2026-09-12T00:00:00Z', updatedAt: '2026-09-12T00:00:00Z',
  }
}

function managedEvent(id: string, slug: string, draftVersion: number) {
  return {
    id,
    slug,
    publicationStatus: 'unpublished',
    lockVersion: 1,
    draft: {
      title: '秦统一六国', summary: '', narrative: '', time: null, primaryCategory: null,
      prominence: null, displayOrder: 1000, regions: [], places: [], periods: [], figures: [], topicTags: [],
      lockVersion: draftVersion, basedOnRevisionNo: null,
      createdAt: '2026-09-12T00:00:00Z', updatedAt: '2026-09-12T00:00:00Z',
    },
    createdAt: '2026-09-12T00:00:00Z',
    updatedAt: '2026-09-12T00:00:00Z',
  }
}

function jsonResponse(body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { 'Content-Type': 'application/json' },
  })
}

function problemResponse(status: number, code: string, detail: string): Response {
  return new Response(JSON.stringify({ code, detail }), {
    status,
    headers: { 'Content-Type': 'application/problem+json' },
  })
}
