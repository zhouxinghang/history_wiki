import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, vi } from 'vitest'
import ContextEntityManager from './ContextEntityManager'

afterEach(() => {
  vi.unstubAllGlobals()
  document.cookie = '__Host-history_wiki_csrf=; Max-Age=0; Path=/; Secure'
})

describe('地点与历史时期管理', () => {
  it('编辑者创建关联多个地区的同名地点并可选择', async () => {
    document.cookie = '__Host-history_wiki_csrf=csrf-token; Path=/; Secure'
    const regions = [region('00000000-0000-7000-8000-000000000001', '东亚'), region('00000000-0000-7000-8000-000000000002', '中亚')]
    const existing = entity('00000000-0000-7000-8000-000000000010', '长安', '汉代都城', [regions[0]])
    const created = entity('00000000-0000-7000-8000-000000000011', '长安', '唐代都城', regions)
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(jsonResponse({ places: [existing] }))
      .mockResolvedValueOnce(jsonResponse({ regions }))
      .mockResolvedValueOnce(jsonResponse(created, 201))
      .mockResolvedValueOnce(jsonResponse({ places: [existing, created] }))
    vi.stubGlobal('fetch', fetchMock)
    vi.spyOn(globalThis.crypto, 'randomUUID').mockReturnValue('00000000-0000-7000-8000-000000000099')
    const user = userEvent.setup()

    render(<ContextEntityManager kind="place" userRole="editor" />)

    expect(await screen.findByText('消歧：汉代都城')).toBeVisible()
    const existingRow = screen.getByText('消歧：汉代都城').closest('article')!
    await user.click(within(existingRow).getByRole('button', { name: '选择' }))
    expect(screen.getByText('已选择：长安（汉代都城）')).toBeVisible()

    await user.type(screen.getByLabelText('当前名称'), '长安')
    await user.type(screen.getByLabelText('消歧名称（可选）'), '唐代都城')
    const regionGroup = screen.getByRole('group', { name: '地区语境（可选，可多选）' })
    await user.click(within(regionGroup).getByRole('checkbox', { name: '东亚' }))
    await user.click(within(regionGroup).getByRole('checkbox', { name: '中亚' }))
    await user.click(screen.getByRole('button', { name: '创建地点' }))

    expect(await screen.findByText('已创建地点“长安（唐代都城）”。')).toBeVisible()
    const createCall = fetchMock.mock.calls.find(([url, init]) => url === '/api/v1/admin/places' && init?.method === 'POST')
    expect(createCall).toBeDefined()
    expect(JSON.parse(String(createCall?.[1]?.body))).toEqual({
      name: '长安',
      disambiguationLabel: '唐代都城',
      regionIds: regions.map((item) => item.id),
    })
  })

  it('历史时期要求地区语境，管理员修改关系时提交所见版本', async () => {
    document.cookie = '__Host-history_wiki_csrf=csrf-token; Path=/; Secure'
    const regions = [region('00000000-0000-7000-8000-000000000001', '东亚'), region('00000000-0000-7000-8000-000000000002', '中亚')]
    const original = entity('00000000-0000-7000-8000-000000000020', '战国', null, [regions[0]])
    const updated = { ...original, regions, lockVersion: 2 }
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(jsonResponse({ periods: [original] }))
      .mockResolvedValueOnce(jsonResponse({ regions }))
      .mockResolvedValueOnce(jsonResponse(updated))
      .mockResolvedValueOnce(jsonResponse({ periods: [updated] }))
    vi.stubGlobal('fetch', fetchMock)
    const user = userEvent.setup()

    render(<ContextEntityManager kind="historical-period" userRole="administrator" />)
    await screen.findByText('地区语境：东亚')

    await user.type(screen.getByLabelText('当前名称'), '空语境时期')
    await user.click(screen.getByRole('button', { name: '创建历史时期' }))
    expect(screen.getByText('有效历史时期必须选择至少一个地区语境。')).toBeVisible()
    expect(fetchMock.mock.calls.some(([, init]) => init?.method === 'POST')).toBe(false)

    await user.click(screen.getByRole('button', { name: '编辑' }))
    const editForm = screen.getByRole('heading', { name: '编辑历史时期' }).closest('form')!
    await user.click(within(editForm).getByRole('checkbox', { name: '中亚' }))
    await user.click(screen.getByRole('button', { name: '保存修改' }))

    expect(await screen.findByText('已更新历史时期“战国”。')).toBeVisible()
    expect(fetchMock).toHaveBeenCalledWith(
      `/api/v1/admin/periods/${original.id}`,
      expect.objectContaining({
        method: 'PATCH',
        headers: expect.objectContaining({ 'If-Match': '"1"' }),
      }),
    )
  })

  it('地区版本变化后刷新可选地区', async () => {
    const eastAsia = region('00000000-0000-7000-8000-000000000001', '东亚')
    const centralAsia = region('00000000-0000-7000-8000-000000000002', '中亚')
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(jsonResponse({ places: [] }))
      .mockResolvedValueOnce(jsonResponse({ regions: [eastAsia] }))
      .mockResolvedValueOnce(jsonResponse({ regions: [eastAsia, centralAsia] }))
    vi.stubGlobal('fetch', fetchMock)

    const view = render(<ContextEntityManager kind="place" userRole="editor" regionsVersion={0} />)
    expect(await screen.findByRole('checkbox', { name: '东亚' })).toBeVisible()
    expect(screen.queryByRole('checkbox', { name: '中亚' })).not.toBeInTheDocument()

    view.rerender(<ContextEntityManager kind="place" userRole="editor" regionsVersion={1} />)

    expect(await screen.findByRole('checkbox', { name: '中亚' })).toBeVisible()
    expect(fetchMock).toHaveBeenCalledWith(
      '/api/v1/admin/regions?limit=100&status=active',
      expect.objectContaining({ credentials: 'same-origin' }),
    )
  })
})

function region(id: string, name: string) {
  return {
    id,
    name,
    disambiguationLabel: null,
    status: 'active' as const,
    lockVersion: 1,
    createdAt: '2026-09-12T12:00:00Z',
    updatedAt: '2026-09-12T12:00:00Z',
  }
}

function entity(id: string, name: string, disambiguationLabel: string | null, regions: ReturnType<typeof region>[]) {
  return {
    id,
    name,
    disambiguationLabel,
    status: 'active' as const,
    regions,
    lockVersion: 1,
    createdAt: '2026-09-12T12:00:00Z',
    updatedAt: '2026-09-12T12:00:00Z',
  }
}

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
}
