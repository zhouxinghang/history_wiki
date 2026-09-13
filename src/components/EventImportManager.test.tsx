import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import EventImportManager from './EventImportManager'
import { preflightEventImport, submitEventImport } from '../data/eventImportClient'

vi.mock('../data/eventImportClient', async (importOriginal) => {
  const original = await importOriginal<typeof import('../data/eventImportClient')>()
  return {
    ...original,
    preflightEventImport: vi.fn(),
    submitEventImport: vi.fn(),
  }
})

const importJSON = JSON.stringify({
  events: [{
    id: '018f6f4c-38f8-7f1f-8f47-5aa4e1c2a311',
    slug: 'test-event',
    title: '测试事件',
  }],
})

describe('EventImportManager', () => {
  beforeEach(() => {
    vi.mocked(preflightEventImport).mockReset()
    vi.mocked(submitEventImport).mockReset()
  })

  it('allows editors to prepare and preflight but not formally import', async () => {
    vi.mocked(preflightEventImport).mockResolvedValue({ valid: true, total: 1, errors: [] })
    const user = userEvent.setup()
    render(<EventImportManager userRole="editor" />)

    await user.upload(screen.getByLabelText(/导入文件/), jsonFile(importJSON))
    expect(await screen.findByText(/已读取 1 条记录/)).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '预检查' }))
    expect(await screen.findByText(/预检查通过/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '正式导入' })).toBeDisabled()
    expect(screen.getByText(/只有管理员能够执行正式导入/)).toBeInTheDocument()
  })

  it('shows preflight errors and an administrator batch result', async () => {
    vi.mocked(preflightEventImport)
      .mockResolvedValueOnce({
        valid: false,
        total: 1,
        errors: [{ index: 0, field: 'slug', code: 'slug_conflict', detail: 'slug 已属于既有历史事件。' }],
      })
      .mockResolvedValueOnce({ valid: true, total: 1, errors: [] })
    vi.mocked(submitEventImport).mockResolvedValue({
      batchId: '018f6f4c-38f8-7f1f-8f47-5aa4e1c2a399',
      importedCount: 1,
      eventIds: ['018f6f4c-38f8-7f1f-8f47-5aa4e1c2a311'],
      createdAt: '2026-09-12T18:00:00Z',
      replayed: false,
    })
    const user = userEvent.setup()
    render(<EventImportManager userRole="administrator" />)

    await user.upload(screen.getByLabelText(/导入文件/), jsonFile(importJSON))
    await user.click(await screen.findByRole('button', { name: '预检查' }))
    expect(await screen.findByText('slug 已属于既有历史事件。')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '正式导入' })).toBeDisabled()

    await user.click(screen.getByRole('button', { name: '预检查' }))
    await waitFor(() => expect(screen.getByRole('button', { name: '正式导入' })).toBeEnabled())
    await user.click(screen.getByRole('button', { name: '正式导入' }))
    expect(await screen.findByText(/导入完成：1 条活动草稿/)).toBeInTheDocument()
    expect(screen.getByText('018f6f4c-38f8-7f1f-8f47-5aa4e1c2a399')).toBeInTheDocument()
    expect(submitEventImport).toHaveBeenCalledTimes(1)
  })
})

function jsonFile(contents: string): File {
  const file = new File([contents], 'events.json', { type: 'application/json' })
  Object.defineProperty(file, 'text', { value: async () => contents })
  return file
}
