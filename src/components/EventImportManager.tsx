import { useRef, useState, type ChangeEvent } from 'react'
import type { UserRole } from '../data/authClient'
import { randomUUID } from '../data/randomUUID'
import {
  EventImportError,
  MAX_EVENT_IMPORT_BYTES,
  MAX_EVENT_IMPORT_RECORDS,
  preflightEventImport,
  submitEventImport,
  type EventImportBatch,
  type EventImportRecord,
  type EventImportValidation,
} from '../data/eventImportClient'

type ImportPhase = 'idle' | 'reading' | 'preflighting' | 'ready' | 'importing' | 'complete'

export default function EventImportManager({ userRole }: { userRole: UserRole }) {
  const [fileName, setFileName] = useState('')
  const [events, setEvents] = useState<EventImportRecord[]>([])
  const [phase, setPhase] = useState<ImportPhase>('idle')
  const [validation, setValidation] = useState<EventImportValidation | null>(null)
  const [batch, setBatch] = useState<EventImportBatch | null>(null)
  const [message, setMessage] = useState('')
  const idempotencyKey = useRef<string | null>(null)

  async function selectFile(event: ChangeEvent<HTMLInputElement>) {
    const file = event.target.files?.[0]
    setValidation(null)
    setBatch(null)
    setEvents([])
    idempotencyKey.current = null
    if (!file) {
      setFileName('')
      setPhase('idle')
      setMessage('')
      return
    }
    setFileName(file.name)
    if (file.size > MAX_EVENT_IMPORT_BYTES) {
      setPhase('idle')
      setMessage('文件超过 20 MiB，无法预检查。')
      return
    }
    setPhase('reading')
    setMessage('正在读取导入文件…')
    try {
      const parsed: unknown = JSON.parse(await file.text())
      const records = Array.isArray(parsed)
        ? parsed
        : isRecord(parsed) && Array.isArray(parsed.events) ? parsed.events : null
      if (!records) throw new Error('JSON 顶层必须是事件数组或包含 events 数组的对象。')
      if (records.length === 0) throw new Error('导入文件至少需要一条历史事件草稿。')
      if (records.length > MAX_EVENT_IMPORT_RECORDS) throw new Error('每批最多导入 10,000 条历史事件草稿。')
      setEvents(records as EventImportRecord[])
      setPhase('idle')
      setMessage(`已读取 ${records.length.toLocaleString()} 条记录，请先执行预检查。`)
      idempotencyKey.current = randomUUID()
    } catch (error) {
      setPhase('idle')
      setMessage(errorMessage(error, '无法读取导入文件。'))
    }
  }

  async function preflight() {
    setPhase('preflighting')
    setValidation(null)
    setBatch(null)
    setMessage('正在预检查字段、UUID、slug 和规范实体关联…')
    try {
      const result = await preflightEventImport(events)
      setValidation(result)
      setPhase(result.valid ? 'ready' : 'idle')
      setMessage(result.valid ? `预检查通过：${result.total.toLocaleString()} 条记录可以导入。` : `预检查发现 ${result.errors.length.toLocaleString()} 个错误。`)
    } catch (error) {
      setPhase('idle')
      setMessage(errorMessage(error, '暂时无法预检查导入文件。'))
    }
  }

  async function submit() {
    if (userRole !== 'administrator' || !validation?.valid) return
    idempotencyKey.current ??= randomUUID()
    setPhase('importing')
    setMessage(`正在以单个事务导入 ${events.length.toLocaleString()} 条活动草稿，请勿关闭页面…`)
    try {
      const result = await submitEventImport(events, idempotencyKey.current)
      setBatch(result)
      setPhase('complete')
      setMessage(result.replayed
        ? `已返回批次 ${result.batchId} 的原导入结果，没有创建重复草稿。`
        : `导入完成：${result.importedCount.toLocaleString()} 条活动草稿均已创建为未发布状态。`)
    } catch (error) {
      if (error instanceof EventImportError && error.validation) {
        setValidation(error.validation)
      }
      setPhase('idle')
      setMessage(errorMessage(error, '整批导入失败，数据库未保留部分结果。'))
    }
  }

  const busy = phase === 'reading' || phase === 'preflighting' || phase === 'importing'

  return (
    <section className="event-import-manager" aria-labelledby="event-import-title" aria-busy={busy}>
      <div className="region-manager__heading">
        <div>
          <p className="admin-kicker">批量录入</p>
          <h2 id="event-import-title">事务性导入历史事件草稿</h2>
          <p>上传 JSON 后先预检查；正式导入绝不发布或覆盖既有历史事件。</p>
        </div>
      </div>

      <div className="event-import-manager__controls">
        <label htmlFor="event-import-file">导入文件（JSON，最多 10,000 条 / 20 MiB）</label>
        <input id="event-import-file" type="file" accept="application/json,.json" disabled={busy} onChange={(event) => void selectFile(event)} />
        {fileName && <p>当前文件：<strong>{fileName}</strong> · {events.length.toLocaleString()} 条记录</p>}
        <div className="region-row__actions">
          <button type="button" disabled={busy || events.length === 0} onClick={() => void preflight()}>
            {phase === 'preflighting' ? '正在预检查…' : '预检查'}
          </button>
          <button type="button" disabled={busy || !validation?.valid || userRole !== 'administrator'} onClick={() => void submit()}>
            {phase === 'importing' ? '正在导入…' : '正式导入'}
          </button>
        </div>
        {userRole !== 'administrator' && <p className="admin-card__note">编辑者可以准备并预检查文件，只有管理员能够执行正式导入。</p>}
      </div>

      {message && <p className="admin-form-message" role="status">{message}</p>}

      {validation && !validation.valid && (
        <div className="event-import-manager__errors" role="alert">
          <h3>预检查错误</h3>
          <table>
            <thead><tr><th>记录</th><th>字段</th><th>问题</th></tr></thead>
            <tbody>
              {validation.errors.map((issue, index) => (
                <tr key={`${issue.index}-${issue.field}-${issue.code}-${index}`}>
                  <td>{issue.index + 1}</td><td><code>{issue.field}</code></td><td>{issue.detail}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {batch && (
        <div className="event-import-manager__result">
          <h3>批次结果</h3>
          <dl>
            <div><dt>批次 UUID</dt><dd><code>{batch.batchId}</code></dd></div>
            <div><dt>创建草稿</dt><dd>{batch.importedCount.toLocaleString()} 条</dd></div>
            <div><dt>完成时间</dt><dd>{new Date(batch.createdAt).toLocaleString()}</dd></div>
          </dl>
        </div>
      )}
    </section>
  )
}

function isRecord(value: unknown): value is { events?: unknown } {
  return typeof value === 'object' && value !== null
}

function errorMessage(error: unknown, fallback: string): string {
  return error instanceof Error ? error.message : fallback
}
