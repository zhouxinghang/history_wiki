import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, vi } from 'vitest'
import AdminApp from './AdminApp'

afterEach(() => {
  vi.unstubAllGlobals()
  document.cookie = '__Host-history_wiki_csrf=; Max-Age=0; Path=/; Secure'
})

describe('管理区身份边界', () => {
  it('未认证时只显示登录表单，并在登录后展示当前身份', async () => {
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(problemResponse(401, 'authentication_required'))
      .mockResolvedValueOnce(jsonResponse({
        user: { id: 'user-1', email: 'admin@example.com', role: 'administrator' },
      }))
      .mockResolvedValueOnce(jsonResponse({ events: [] }))
      .mockResolvedValueOnce(jsonResponse({ regions: [] }))
      .mockResolvedValueOnce(jsonResponse({ places: [] }))
      .mockResolvedValueOnce(jsonResponse({ regions: [] }))
      .mockResolvedValueOnce(jsonResponse({ periods: [] }))
      .mockResolvedValueOnce(jsonResponse({ regions: [] }))
      .mockResolvedValueOnce(jsonResponse({ figures: [] }))
      .mockResolvedValueOnce(jsonResponse({ topicTags: [] }))
      .mockResolvedValueOnce(jsonResponse({ users: [] }))
    vi.stubGlobal('fetch', fetchMock)
    const user = userEvent.setup()

    render(<AdminApp />)

    expect(await screen.findByRole('heading', { name: '管理员登录' })).toBeVisible()
    expect(screen.queryByText('admin@example.com')).not.toBeInTheDocument()

    await user.type(screen.getByLabelText('邮箱'), 'admin@example.com')
    await user.type(screen.getByLabelText('密码'), 'correct horse battery staple')
    await user.click(screen.getByRole('button', { name: '登录' }))

    expect((await screen.findAllByText('admin@example.com'))[0]).toBeVisible()
    expect(screen.getAllByText('管理员')[0]).toBeVisible()
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/auth/login', expect.objectContaining({
      method: 'POST',
      credentials: 'same-origin',
    }))
    expect(await screen.findByRole('heading', { name: '历史事件草稿' })).toBeVisible()
    expect(await screen.findByRole('heading', { name: '地区管理' })).toBeVisible()
    expect(screen.getByRole('heading', { name: '地点管理' })).toBeVisible()
    expect(screen.getByRole('heading', { name: '历史时期管理' })).toBeVisible()
    expect(screen.getByRole('heading', { name: '历史人物管理' })).toBeVisible()
    expect(screen.getByRole('heading', { name: '主题标签管理' })).toBeVisible()
    expect(await screen.findByRole('heading', { name: '编辑者账号管理' })).toBeVisible()
  })

  it('读取当前身份，并使用 CSRF Token 安全退出', async () => {
    document.cookie = '__Host-history_wiki_csrf=csrf-token; Path=/; Secure'
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(jsonResponse({
        user: { id: 'user-1', email: 'editor@example.com', role: 'editor' },
      }))
      .mockResolvedValueOnce(jsonResponse({ events: [] }))
      .mockResolvedValueOnce(jsonResponse({ regions: [] }))
      .mockResolvedValueOnce(jsonResponse({ places: [] }))
      .mockResolvedValueOnce(jsonResponse({ regions: [] }))
      .mockResolvedValueOnce(jsonResponse({ periods: [] }))
      .mockResolvedValueOnce(jsonResponse({ regions: [] }))
      .mockResolvedValueOnce(jsonResponse({ figures: [] }))
      .mockResolvedValueOnce(jsonResponse({ topicTags: [] }))
      .mockResolvedValueOnce(new Response(null, { status: 204 }))
    vi.stubGlobal('fetch', fetchMock)
    const user = userEvent.setup()

    render(<AdminApp />)

    expect(await screen.findByText('editor@example.com')).toBeVisible()
    expect(screen.getByText('编辑者')).toBeVisible()
    await user.click(screen.getByRole('button', { name: '退出登录' }))

    expect(await screen.findByText('已安全退出。')).toBeVisible()
    expect(fetchMock).toHaveBeenLastCalledWith('/api/v1/auth/logout', {
      method: 'POST',
      credentials: 'same-origin',
      headers: { 'X-CSRF-Token': 'csrf-token' },
    })
  })

  it('编辑者可以修改自己的密码，但不能读取管理员账号列表', async () => {
    document.cookie = '__Host-history_wiki_csrf=csrf-token; Path=/; Secure'
    const fetchMock = vi.fn<typeof fetch>(async (input, init) => {
      if (input === '/api/v1/auth/me') {
        return jsonResponse({
          user: { id: 'editor-1', email: 'editor@example.com', role: 'editor' },
        })
      }
      if (input === '/api/v1/admin/events') return jsonResponse({ events: [] })
      if (input === '/api/v1/admin/regions?limit=100') return jsonResponse({ regions: [] })
      if (input === '/api/v1/admin/regions?limit=100&status=active') return jsonResponse({ regions: [] })
      if (input === '/api/v1/admin/places?limit=100') return jsonResponse({ places: [] })
      if (input === '/api/v1/admin/periods?limit=100') return jsonResponse({ periods: [] })
      if (input === '/api/v1/admin/figures?limit=100') return jsonResponse({ figures: [] })
      if (input === '/api/v1/admin/topic-tags?limit=100') return jsonResponse({ topicTags: [] })
      if (input === '/api/v1/auth/change-password' && init?.method === 'POST') {
        return new Response(null, { status: 204 })
      }
      throw new Error(`unexpected request: ${String(input)}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    const user = userEvent.setup()

    render(<AdminApp />)

    expect(await screen.findByRole('heading', { name: '修改自己的密码' })).toBeVisible()
    expect(screen.queryByRole('heading', { name: '编辑者账号管理' })).not.toBeInTheDocument()
    expect(fetchMock).not.toHaveBeenCalledWith('/api/v1/admin/users', expect.anything())

    await user.type(screen.getByLabelText('当前密码'), 'correct horse battery staple')
    await user.type(screen.getByLabelText('新密码（至少 12 个字符）'), 'new correct horse battery staple')
    await user.type(screen.getByLabelText('再次输入新密码'), 'new correct horse battery staple')
    await user.click(screen.getByRole('button', { name: '修改密码并退出' }))

    expect(await screen.findByText(/全部登录会话已撤销/)).toBeVisible()
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/auth/change-password', expect.objectContaining({
      method: 'POST',
      headers: expect.objectContaining({ 'X-CSRF-Token': 'csrf-token' }),
    }))
  })

  it('管理员可以创建编辑者并看到账号管理操作', async () => {
    document.cookie = '__Host-history_wiki_csrf=csrf-token; Path=/; Secure'
    const administrator = {
      id: 'admin-1', email: 'admin@example.com', role: 'administrator',
      disabledAt: null, lockVersion: 1,
      createdAt: '2026-09-12T00:00:00Z', updatedAt: '2026-09-12T00:00:00Z',
    }
    const created = {
      id: 'editor-1', email: 'new-editor@example.com', role: 'editor',
      disabledAt: null, lockVersion: 1,
      createdAt: '2026-09-12T00:00:00Z', updatedAt: '2026-09-12T00:00:00Z',
    }
    const fetchMock = vi.fn<typeof fetch>(async (input, init) => {
      if (input === '/api/v1/auth/me') return jsonResponse({ user: administrator })
      if (input === '/api/v1/admin/events') return jsonResponse({ events: [] })
      if (input === '/api/v1/admin/regions?limit=100') return jsonResponse({ regions: [] })
      if (input === '/api/v1/admin/regions?limit=100&status=active') return jsonResponse({ regions: [] })
      if (input === '/api/v1/admin/places?limit=100') return jsonResponse({ places: [] })
      if (input === '/api/v1/admin/periods?limit=100') return jsonResponse({ periods: [] })
      if (input === '/api/v1/admin/figures?limit=100') return jsonResponse({ figures: [] })
      if (input === '/api/v1/admin/topic-tags?limit=100') return jsonResponse({ topicTags: [] })
      if (input === '/api/v1/admin/users' && !init?.method) return jsonResponse({ users: [administrator] })
      if (input === '/api/v1/admin/users' && init?.method === 'POST') {
        return new Response(JSON.stringify(created), {
          status: 201,
          headers: { 'Content-Type': 'application/json' },
        })
      }
      throw new Error(`unexpected request: ${String(input)}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    const user = userEvent.setup()

    render(<AdminApp />)

    expect((await screen.findAllByText('admin@example.com'))[0]).toBeVisible()
    await user.type(screen.getByLabelText('邮箱', { selector: '#new-user-email' }), 'new-editor@example.com')
    await user.type(screen.getByLabelText('初始密码（至少 12 个字符）'), 'correct horse battery staple')
    await user.click(screen.getByRole('button', { name: '创建账号' }))

    expect(await screen.findByText('new-editor@example.com')).toBeVisible()
    expect(screen.getAllByRole('button', { name: '停用账号' })).toHaveLength(2)
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/admin/users', expect.objectContaining({
      method: 'POST',
      headers: expect.objectContaining({ 'X-CSRF-Token': 'csrf-token' }),
    }))
  })
})

function jsonResponse(body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { 'Content-Type': 'application/json' },
  })
}

function problemResponse(status: number, code: string): Response {
  return new Response(JSON.stringify({ code, detail: '需要登录' }), {
    status,
    headers: { 'Content-Type': 'application/problem+json' },
  })
}
