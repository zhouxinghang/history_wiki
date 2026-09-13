import { useEffect, useState, type FormEvent } from 'react'
import {
  AuthenticationError,
  changePassword,
  createUser,
  listUsers,
  resetUserPassword,
  updateUser,
  type CurrentUser,
  type ManagedUser,
  type UserRole,
} from '../data/authClient'

interface UserManagerProps {
  currentUser: CurrentUser
  onSignedOut: (message: string) => void
}

export default function UserManager({ currentUser, onSignedOut }: UserManagerProps) {
  const [users, setUsers] = useState<ManagedUser[]>([])
  const [loading, setLoading] = useState(currentUser.role === 'administrator')
  const [busyUser, setBusyUser] = useState<string | null>(null)
  const [message, setMessage] = useState('')

  useEffect(() => {
    if (currentUser.role !== 'administrator') return
    const controller = new AbortController()
    void listUsers(controller.signal)
      .then(setUsers)
      .catch((error: unknown) => {
        if (error instanceof DOMException && error.name === 'AbortError') return
        setMessage(errorMessage(error, '暂时无法读取账号列表。'))
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false)
      })
    return () => controller.abort()
  }, [currentUser.role])

  async function submitOwnPassword(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const form = event.currentTarget
    const data = new FormData(form)
    const newPassword = String(data.get('newPassword') ?? '')
    if (newPassword !== String(data.get('confirmPassword') ?? '')) {
      setMessage('两次输入的新密码不一致。')
      return
    }
    setBusyUser(currentUser.id)
    setMessage('')
    try {
      await changePassword(String(data.get('currentPassword') ?? ''), newPassword)
      form.reset()
      onSignedOut('密码已修改，全部登录会话已撤销，请使用新密码重新登录。')
    } catch (error) {
      setMessage(errorMessage(error, '暂时无法修改密码。'))
    } finally {
      setBusyUser(null)
    }
  }

  async function submitCreate(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const form = event.currentTarget
    const data = new FormData(form)
    setBusyUser('new')
    setMessage('')
    try {
      const created = await createUser({
        email: String(data.get('email') ?? ''),
        password: String(data.get('password') ?? ''),
        role: String(data.get('role') ?? 'editor') as UserRole,
      })
      setUsers((current) => [...current, created])
      form.reset()
      setMessage(`已创建 ${created.email}。`)
    } catch (error) {
      setMessage(errorMessage(error, '暂时无法创建账号。'))
    } finally {
      setBusyUser(null)
    }
  }

  async function saveUser(user: ManagedUser, role: UserRole, disabled: boolean) {
    setBusyUser(user.id)
    setMessage('')
    try {
      const updated = await updateUser(user, { role, disabled })
      setUsers((current) => current.map((item) => item.id === updated.id ? updated : item))
      setMessage(`已更新 ${updated.email}；权限或停用变更会撤销该账号的会话。`)
      if (updated.id === currentUser.id && (disabled || role !== currentUser.role)) {
        onSignedOut('当前账号权限已变更，登录会话已撤销，请重新登录。')
      }
    } catch (error) {
      if (error instanceof AuthenticationError && error.code === 'user_version_conflict') {
        try {
          setUsers(await listUsers())
          setMessage('该账号已被其他管理员修改，列表已刷新，请重新操作。')
        } catch (reloadError) {
          setMessage(errorMessage(reloadError, '账号已被修改，且暂时无法刷新账号列表。'))
        }
      } else {
        setMessage(errorMessage(error, '暂时无法更新账号。'))
      }
    } finally {
      setBusyUser(null)
    }
  }

  async function submitReset(event: FormEvent<HTMLFormElement>, user: ManagedUser) {
    event.preventDefault()
    const form = event.currentTarget
    const password = String(new FormData(form).get('password') ?? '')
    setBusyUser(user.id)
    setMessage('')
    try {
      await resetUserPassword(user.id, password)
      form.reset()
      if (user.id === currentUser.id) {
        onSignedOut('密码已重置，全部登录会话已撤销，请重新登录。')
      } else {
        setUsers(await listUsers())
        setMessage(`已重置 ${user.email} 的密码并撤销全部会话。`)
      }
    } catch (error) {
      setMessage(errorMessage(error, '暂时无法重置密码。'))
    } finally {
      setBusyUser(null)
    }
  }

  return (
    <section className="user-manager" aria-labelledby="security-heading">
      <div className="user-manager__heading">
        <div>
          <p className="admin-kicker">账号与会话</p>
          <h2 id="security-heading">账号安全</h2>
          <p>密码操作会撤销全部登录会话，并要求重新登录。</p>
        </div>
      </div>

      {message && <p className="admin-form-message" role="status">{message}</p>}

      <form className="admin-entity-form user-manager__password" onSubmit={submitOwnPassword}>
        <h3>修改自己的密码</h3>
        <label htmlFor="current-password">当前密码</label>
        <input id="current-password" name="currentPassword" type="password" autoComplete="current-password" required />
        <label htmlFor="new-password">新密码（至少 12 个字符）</label>
        <input id="new-password" name="newPassword" type="password" minLength={12} autoComplete="new-password" required />
        <label htmlFor="confirm-password">再次输入新密码</label>
        <input id="confirm-password" name="confirmPassword" type="password" minLength={12} autoComplete="new-password" required />
        <button type="submit" disabled={busyUser === currentUser.id}>修改密码并退出</button>
      </form>

      {currentUser.role === 'administrator' && (
        <div className="user-manager__accounts">
          <div className="user-manager__subheading">
            <h3>编辑者账号管理</h3>
            <p>账号不会物理删除。必须始终保留至少一个有效管理员。</p>
          </div>

          <form className="admin-entity-form user-manager__create" onSubmit={submitCreate}>
            <h3>创建账号</h3>
            <label htmlFor="new-user-email">邮箱</label>
            <input id="new-user-email" name="email" type="email" autoComplete="off" required />
            <label htmlFor="new-user-role">角色</label>
            <select id="new-user-role" name="role" defaultValue="editor">
              <option value="editor">编辑者</option>
              <option value="administrator">管理员</option>
            </select>
            <label htmlFor="new-user-password">初始密码（至少 12 个字符）</label>
            <input id="new-user-password" name="password" type="password" minLength={12} autoComplete="new-password" required />
            <button type="submit" disabled={busyUser === 'new'}>创建账号</button>
          </form>

          {loading ? (
            <p aria-live="polite">正在读取账号列表…</p>
          ) : (
            <div className="user-list">
              {users.map((user) => (
                <ManagedUserRow
                  key={`${user.id}:${user.lockVersion}`}
                  user={user}
                  busy={busyUser === user.id}
                  onSave={saveUser}
                  onReset={submitReset}
                />
              ))}
            </div>
          )}
        </div>
      )}
    </section>
  )
}

function ManagedUserRow({
  user,
  busy,
  onSave,
  onReset,
}: {
  user: ManagedUser
  busy: boolean
  onSave: (user: ManagedUser, role: UserRole, disabled: boolean) => Promise<void>
  onReset: (event: FormEvent<HTMLFormElement>, user: ManagedUser) => Promise<void>
}) {
  const [role, setRole] = useState<UserRole>(user.role)
  const disabled = user.disabledAt !== null

  return (
    <article className="user-row">
      <div className="user-row__identity">
        <h4>{user.email}</h4>
        <span>{disabled ? '已停用' : '有效'}</span>
      </div>
      <div className="user-row__controls">
        <label>
          角色
          <select
            aria-label={`${user.email} 的角色`}
            value={role}
            onChange={(event) => setRole(event.target.value as UserRole)}
          >
            <option value="editor">编辑者</option>
            <option value="administrator">管理员</option>
          </select>
        </label>
        <button type="button" disabled={busy} onClick={() => void onSave(user, role, disabled)}>
          保存角色
        </button>
        <button type="button" disabled={busy} onClick={() => void onSave(user, role, !disabled)}>
          {disabled ? '重新启用' : '停用账号'}
        </button>
      </div>
      <form className="user-row__reset" onSubmit={(event) => void onReset(event, user)}>
        <label htmlFor={`reset-${user.id}`}>设置临时密码</label>
        <input id={`reset-${user.id}`} name="password" type="password" minLength={12} autoComplete="new-password" required />
        <button type="submit" disabled={busy}>重置密码</button>
      </form>
    </article>
  )
}

function errorMessage(error: unknown, fallback: string): string {
  if (error instanceof AuthenticationError) return error.message
  return error instanceof Error ? error.message : fallback
}
