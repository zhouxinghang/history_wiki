import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, vi } from 'vitest'
import CanonicalEntityManager, { type CanonicalEntityDefinition } from './CanonicalEntityManager'

afterEach(() => {
  vi.unstubAllGlobals()
  document.cookie = '__Host-history_wiki_csrf=; Max-Age=0; Path=/; Secure'
})

const figureDefinition: CanonicalEntityDefinition = {
  resource: 'figures',
  listField: 'figures',
  title: '历史人物管理',
  singular: '历史人物',
  description: '同名历史人物可以分别创建，并通过消歧名称选择正确人物。',
  versionConflictCode: 'historical_figure_version_conflict',
}

const topicTagDefinition: CanonicalEntityDefinition = {
  resource: 'topic-tags',
  listField: 'topicTags',
  title: '主题标签管理',
  singular: '主题标签',
  description: '主题标签使用稳定标识。',
  versionConflictCode: 'topic_tag_version_conflict',
}

describe('复用的规范实体管理交互', () => {
  it('区分、搜索、选择并创建同名历史人物', async () => {
    document.cookie = '__Host-history_wiki_csrf=csrf-token; Path=/; Secure'
    const figures = [
      entity('figure-1', '刘彻', '汉武帝'),
      entity('figure-2', '刘彻', '近现代同名人物'),
    ]
    const created = entity('figure-3', '司马迁', '西汉史学家')
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(jsonResponse({ figures }))
      .mockResolvedValueOnce(jsonResponse({ figures: [figures[1]] }))
      .mockResolvedValueOnce(jsonResponse(created, 201))
      .mockResolvedValueOnce(jsonResponse({ figures: [created] }))
    vi.stubGlobal('fetch', fetchMock)
    vi.spyOn(globalThis.crypto, 'randomUUID').mockReturnValue('00000000-0000-7000-8000-000000000017')
    const user = userEvent.setup()

    render(<CanonicalEntityManager definition={figureDefinition} userRole="editor" />)

    const emperor = await screen.findByText('消歧：汉武帝')
    expect(screen.getByText('消歧：近现代同名人物')).toBeVisible()
    await user.click(within(emperor.closest('article')!).getByRole('button', { name: '选择' }))
    expect(screen.getByText('已选择：刘彻（汉武帝）')).toBeVisible()

    await user.type(screen.getByLabelText('搜索名称或消歧名称'), '近现代')
    await user.click(screen.getByRole('button', { name: '搜索' }))
    expect(fetchMock).toHaveBeenCalledWith(
      '/api/v1/admin/figures?limit=100&q=%E8%BF%91%E7%8E%B0%E4%BB%A3',
      expect.objectContaining({ credentials: 'same-origin' }),
    )

    await user.type(screen.getByLabelText('当前名称'), '司马迁')
    await user.type(screen.getByLabelText('消歧名称（可选）'), '西汉史学家')
    await user.click(screen.getByRole('button', { name: '创建历史人物' }))

    expect(await screen.findByText('已创建历史人物“司马迁（西汉史学家）”。')).toBeVisible()
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/admin/figures', expect.objectContaining({
      method: 'POST',
      headers: expect.objectContaining({
        'Idempotency-Key': '00000000-0000-7000-8000-000000000017',
        'X-CSRF-Token': 'csrf-token',
      }),
    }))
  })

  it('主题标签使用同一套查询、选择和创建交互', async () => {
    const silkRoad = entity('tag-1', '丝绸之路', null)
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(jsonResponse({ topicTags: [silkRoad] }))
    vi.stubGlobal('fetch', fetchMock)
    const user = userEvent.setup()

    render(<CanonicalEntityManager definition={topicTagDefinition} userRole="editor" />)

    expect(await screen.findByRole('heading', { name: '主题标签管理' })).toBeVisible()
    await user.click(screen.getByRole('button', { name: '选择' }))
    expect(screen.getByText('已选择：丝绸之路')).toBeVisible()
    expect(screen.queryByRole('button', { name: '编辑' })).not.toBeInTheDocument()
  })

  it('切换编辑目标时重新填充对应实体，避免把前一实体内容写入新目标', async () => {
    const figures = [
      entity('figure-1', '刘彻', '汉武帝'),
      entity('figure-2', '司马迁', '西汉史学家'),
    ]
    vi.stubGlobal('fetch', vi.fn<typeof fetch>().mockResolvedValueOnce(jsonResponse({ figures })))
    const user = userEvent.setup()

    render(<CanonicalEntityManager definition={figureDefinition} userRole="administrator" />)

    const first = await screen.findByText('消歧：汉武帝')
    await user.click(within(first.closest('article')!).getByRole('button', { name: '编辑' }))
    expect(screen.getByLabelText('当前名称', { selector: '#canonical-figures-edit-name' })).toHaveValue('刘彻')

    const second = screen.getByText('消歧：西汉史学家')
    await user.click(within(second.closest('article')!).getByRole('button', { name: '编辑' }))
    expect(screen.getByLabelText('当前名称', { selector: '#canonical-figures-edit-name' })).toHaveValue('司马迁')
    expect(screen.getByLabelText('消歧名称（可选）', { selector: '#canonical-figures-edit-disambiguation' })).toHaveValue('西汉史学家')
  })

  it('管理员先查看并确认影响范围，再提交稳定 ID 合并', async () => {
    document.cookie = '__Host-history_wiki_csrf=csrf-token; Path=/; Secure'
    const source = entity('00000000-0000-7000-8000-000000000001', '刘彻', '旧条目')
    const target = entity('00000000-0000-7000-8000-000000000002', '刘彻', '汉武帝')
    const impact = { draftCount: 2, revisionCount: 4, publishedEventCount: 1 }
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(jsonResponse({ figures: [source, target] }))
      .mockResolvedValueOnce(jsonResponse(impact))
      .mockResolvedValueOnce(jsonResponse({
        entity: { ...source, status: 'merged', mergedIntoId: target.id, lockVersion: 2 },
        impact,
      }))
      .mockResolvedValueOnce(jsonResponse({
        figures: [{ ...source, status: 'merged', mergedIntoId: target.id, lockVersion: 2 }, target],
      }))
    vi.stubGlobal('fetch', fetchMock)
    const user = userEvent.setup()

    render(<CanonicalEntityManager definition={figureDefinition} userRole="administrator" />)

    const sourceRow = (await screen.findByText('消歧：旧条目')).closest('article')!
    await user.click(within(sourceRow).getByRole('button', { name: '合并' }))
    await user.selectOptions(screen.getByLabelText('有效目标实体'), target.id)
    await user.click(screen.getByRole('button', { name: '查看影响' }))
    expect(await screen.findByText(/2 份活动草稿、4 个不可变版本/)).toBeVisible()
    expect(screen.getByRole('button', { name: '确认合并' })).toBeDisabled()

    await user.click(screen.getByRole('checkbox', { name: '我已确认影响范围和合并目标' }))
    await user.click(screen.getByRole('button', { name: '确认合并' }))

    expect(await screen.findByText(/旧标识仍会解析到目标实体/)).toBeVisible()
    expect(fetchMock).toHaveBeenCalledWith(
      `/api/v1/admin/figures/${source.id}/merge`,
      expect.objectContaining({
        method: 'POST',
        headers: expect.objectContaining({ 'If-Match': '"1"', 'X-CSRF-Token': 'csrf-token' }),
        body: JSON.stringify({ targetId: target.id, confirmedImpact: impact }),
      }),
    )
  })
})

function entity(id: string, name: string, disambiguationLabel: string | null) {
  return {
    id,
    name,
    disambiguationLabel,
    status: 'active' as const,
    mergedIntoId: null,
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
