import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, vi } from 'vitest'
import RegionManager from './RegionManager'

afterEach(() => {
  vi.unstubAllGlobals()
  document.cookie = '__Host-history_wiki_csrf=; Max-Age=0; Path=/; Secure'
})

describe('地区管理', () => {
  it('显示同名地区的消歧名称，并允许搜索、选择和创建', async () => {
    document.cookie = '__Host-history_wiki_csrf=csrf-token; Path=/; Secure'
    const existingRegions = [
      region('region-1', '刚果', '刚果共和国'),
      region('region-2', '刚果', '刚果民主共和国'),
    ]
    const created = region('region-3', '东亚', '文化地理语境')
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(jsonResponse({ regions: existingRegions }))
      .mockResolvedValueOnce(jsonResponse({ regions: [existingRegions[1]] }))
      .mockResolvedValueOnce(jsonResponse(created, 201))
      .mockResolvedValueOnce(jsonResponse({ regions: [created] }))
    vi.stubGlobal('fetch', fetchMock)
    vi.spyOn(globalThis.crypto, 'randomUUID').mockReturnValue('00000000-0000-7000-8000-000000000001')
    const user = userEvent.setup()
    const onChange = vi.fn()

    render(<RegionManager userRole="editor" onChange={onChange} />)

    expect(await screen.findByText('消歧：刚果共和国')).toBeVisible()
    expect(screen.getByText('消歧：刚果民主共和国')).toBeVisible()
    const firstRegion = screen.getByText('消歧：刚果共和国').closest('article')!
    await user.click(within(firstRegion).getByRole('button', { name: '选择' }))
    expect(screen.getByText('已选择：刚果（刚果共和国）')).toBeVisible()

    await user.type(screen.getByLabelText('搜索名称或消歧名称'), '民主')
    await user.click(screen.getByRole('button', { name: '搜索' }))
    expect(fetchMock).toHaveBeenCalledWith(
      '/api/v1/admin/regions?limit=100&q=%E6%B0%91%E4%B8%BB',
      expect.objectContaining({ credentials: 'same-origin' }),
    )

    await user.type(screen.getByLabelText('当前名称'), '东亚')
    await user.type(screen.getByLabelText('消歧名称（可选）'), '文化地理语境')
    await user.click(screen.getByRole('button', { name: '创建地区' }))

    expect(await screen.findByText('已创建地区“东亚（文化地理语境）”。')).toBeVisible()
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/admin/regions', expect.objectContaining({
      method: 'POST',
      credentials: 'same-origin',
      headers: expect.objectContaining({
        'Idempotency-Key': '00000000-0000-7000-8000-000000000001',
        'X-CSRF-Token': 'csrf-token',
      }),
    }))
    expect(onChange).toHaveBeenCalledTimes(1)
  })

  it('管理员使用所见版本修改地区', async () => {
    document.cookie = '__Host-history_wiki_csrf=csrf-token; Path=/; Secure'
    const original = region('00000000-0000-7000-8000-000000000010', '东亚', null)
    const updated = { ...original, name: '东亚地区', status: 'inactive' as const, lockVersion: 2 }
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(jsonResponse({ regions: [original] }))
      .mockResolvedValueOnce(jsonResponse(updated))
      .mockResolvedValueOnce(jsonResponse({ regions: [updated] }))
    vi.stubGlobal('fetch', fetchMock)
    const user = userEvent.setup()
    const onChange = vi.fn()

    render(<RegionManager userRole="administrator" onChange={onChange} />)
    await user.click(await screen.findByRole('button', { name: '编辑' }))
    const name = screen.getByLabelText('当前名称', { selector: '#canonical-regions-edit-name' })
    await user.clear(name)
    await user.type(name, '东亚地区')
    await user.selectOptions(screen.getByLabelText('状态'), 'inactive')
    await user.click(screen.getByRole('button', { name: '保存修改' }))

    expect(await screen.findByText('已更新地区“东亚地区”。')).toBeVisible()
    expect(fetchMock).toHaveBeenCalledWith(
      `/api/v1/admin/regions/${original.id}`,
      expect.objectContaining({
        method: 'PATCH',
        headers: expect.objectContaining({ 'If-Match': '"1"' }),
      }),
    )
    expect(onChange).toHaveBeenCalledTimes(1)
  })
})

function region(id: string, name: string, disambiguationLabel: string | null) {
  return {
    id,
    name,
    disambiguationLabel,
    status: 'active' as const,
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
