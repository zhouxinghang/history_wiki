import { useEffect, useState, type FormEvent } from 'react'
import {
  AuthenticationError,
  getCurrentUser,
  login,
  logout,
  type CurrentUser,
} from './data/authClient'
import CanonicalEntityManager, { type CanonicalEntityDefinition } from './components/CanonicalEntityManager'
import RegionManager from './components/RegionManager'
import UserManager from './components/UserManager'
import ContextEntityManager from './components/ContextEntityManager'
import EventDraftManager from './components/EventDraftManager'
import EventImportManager from './components/EventImportManager'

type AdminStatus = 'checking' | 'anonymous' | 'authenticated' | 'unavailable'

const historicalFigureDefinition: CanonicalEntityDefinition = {
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
  description: '主题标签使用稳定标识，可供多个历史事件共同选择和引用。',
  versionConflictCode: 'topic_tag_version_conflict',
}

export default function AdminApp() {
  const [status, setStatus] = useState<AdminStatus>('checking')
  const [user, setUser] = useState<CurrentUser | null>(null)
  const [message, setMessage] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const [regionsVersion, setRegionsVersion] = useState(0)

  useEffect(() => {
    const controller = new AbortController()
    void getCurrentUser(controller.signal)
      .then((currentUser) => {
        if (!currentUser) {
          setStatus('anonymous')
          return
        }
        setUser(currentUser)
        setStatus('authenticated')
      })
      .catch((error: unknown) => {
        if (error instanceof DOMException && error.name === 'AbortError') return
        setMessage(errorMessage(error, '暂时无法确认登录状态。'))
        setStatus('unavailable')
      })
    return () => controller.abort()
  }, [])

  async function submitLogin(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const form = new FormData(event.currentTarget)
    setSubmitting(true)
    setMessage('')
    try {
      const currentUser = await login(
        String(form.get('email') ?? ''),
        String(form.get('password') ?? ''),
      )
      setUser(currentUser)
      setStatus('authenticated')
    } catch (error) {
      setMessage(errorMessage(error, '登录失败，请稍后重试。'))
      setStatus('anonymous')
    } finally {
      setSubmitting(false)
    }
  }

  async function submitLogout() {
    setSubmitting(true)
    setMessage('')
    try {
      await logout()
      setUser(null)
      setStatus('anonymous')
      setMessage('已安全退出。')
    } catch (error) {
      if (error instanceof AuthenticationError && error.status === 401) {
        setUser(null)
        setStatus('anonymous')
        setMessage('登录已失效，请重新登录。')
      } else {
        setMessage(errorMessage(error, '暂时无法退出，请稍后重试。'))
      }
    } finally {
      setSubmitting(false)
    }
  }

  function handleSignedOut(nextMessage: string) {
    setUser(null)
    setStatus('anonymous')
    setMessage(nextMessage)
  }

  return (
    <div className="admin-page">
      <header className="admin-header">
        <a className="brand" href="/" aria-label="返回经纬史读者端">
          <span className="brand__seal" aria-hidden="true">史</span>
          <span>
            <strong>经纬史</strong>
            <small>ADMINISTRATION</small>
          </span>
        </a>
        <a className="admin-header__reader-link" href="/">返回读者端</a>
      </header>

      <main className="admin-main">
        {status === 'checking' && (
          <section className="admin-card" aria-live="polite">
            <p className="admin-kicker">身份校验</p>
            <h1>正在确认管理区登录状态</h1>
          </section>
        )}

        {status === 'unavailable' && (
          <section className="admin-card" role="alert">
            <p className="admin-kicker">服务暂不可用</p>
            <h1>无法进入管理区</h1>
            <p>{message}</p>
            <button type="button" onClick={() => window.location.reload()}>
              重新尝试
            </button>
          </section>
        )}

        {status === 'anonymous' && (
          <section className="admin-card admin-card--login" aria-labelledby="login-title">
            <div>
              <p className="admin-kicker">受保护的管理区</p>
              <h1 id="login-title">管理员登录</h1>
              <p>
                使用项目维护者创建的本地账号登录。系统不提供默认账号、公开注册或邮件找回。
              </p>
            </div>
            <form className="admin-login-form" onSubmit={submitLogin}>
              <label htmlFor="admin-email">邮箱</label>
              <input
                id="admin-email"
                name="email"
                type="email"
                autoComplete="username"
                required
              />
              <label htmlFor="admin-password">密码</label>
              <input
                id="admin-password"
                name="password"
                type="password"
                autoComplete="current-password"
                required
              />
              {message && <p className="admin-form-message" role="status">{message}</p>}
              <button type="submit" disabled={submitting}>
                {submitting ? '正在登录…' : '登录'}
              </button>
            </form>
          </section>
        )}

        {status === 'authenticated' && user && (
          <div className="admin-workspace">
            <section className="admin-card admin-card--identity" aria-labelledby="admin-title">
              <div>
                <p className="admin-kicker">当前身份</p>
                <h1 id="admin-title">管理区</h1>
              </div>
              <dl className="admin-identity">
                <div>
                  <dt>邮箱</dt>
                  <dd>{user.email}</dd>
                </div>
                <div>
                  <dt>角色</dt>
                  <dd>{user.role === 'administrator' ? '管理员' : '编辑者'}</dd>
                </div>
              </dl>
              {message && <p className="admin-form-message" role="status">{message}</p>}
              <button type="button" disabled={submitting} onClick={submitLogout}>
                {submitting ? '正在退出…' : '退出登录'}
              </button>
            </section>
            <EventDraftManager userRole={user.role} />
            <EventImportManager userRole={user.role} />
            <RegionManager userRole={user.role} onChange={() => setRegionsVersion((version) => version + 1)} />
            <ContextEntityManager kind="place" userRole={user.role} regionsVersion={regionsVersion} />
            <ContextEntityManager kind="historical-period" userRole={user.role} regionsVersion={regionsVersion} />
            <CanonicalEntityManager definition={historicalFigureDefinition} userRole={user.role} />
            <CanonicalEntityManager definition={topicTagDefinition} userRole={user.role} />
            <UserManager currentUser={user} onSignedOut={handleSignedOut} />
          </div>
        )}
      </main>
    </div>
  )
}

function errorMessage(error: unknown, fallback: string): string {
  return error instanceof Error ? error.message : fallback
}
