import { useCallback, useEffect, useRef, useState, type FormEvent } from 'react'
import { listCanonicalEntities, type ManagedCanonicalEntity } from '../data/canonicalEntityClient'
import { listContextEntities, type ContextEntity } from '../data/contextEntityClient'
import {
  archiveManagedEvent,
  createManagedEvent,
  createManagedEventDraft,
  EventDraftManagementError,
  getManagedEventRevision,
  listManagedEventRevisions,
  listManagedEvents,
  publishManagedEvent,
  restoreManagedEventRevision,
  updateManagedEventDraft,
  updateManagedEventSlug,
  type EventDraftWriteInput,
  type EventEntityReference,
  type ManagedEvent,
  type ManagedEventRevision,
  type ManagedEventRevisionSummary,
} from '../data/eventDraftClient'
import { listRegions, type Region } from '../data/regionClient'
import type { EventProminence, PrimaryCategory, TimeExpression } from '../domain/history'

interface EventOptions {
  regions: Region[]
  places: ContextEntity[]
  periods: ContextEntity[]
  figures: ManagedCanonicalEntity[]
  topicTags: ManagedCanonicalEntity[]
}

const emptyOptions: EventOptions = {
  regions: [], places: [], periods: [], figures: [], topicTags: [],
}

const primaryCategories: PrimaryCategory[] = ['政治', '军事', '文化', '科技', '社会', '交流']

export default function EventDraftManager({ userRole = 'editor' }: { userRole?: 'editor' | 'administrator' }) {
  const [events, setEvents] = useState<ManagedEvent[]>([])
  const [editing, setEditing] = useState<ManagedEvent | null>(null)
  const [creating, setCreating] = useState(false)
  const [revisionEventID, setRevisionEventID] = useState<string | null>(null)
  const [revisions, setRevisions] = useState<ManagedEventRevisionSummary[]>([])
  const [revisionDetail, setRevisionDetail] = useState<ManagedEventRevision | null>(null)
  const [revisionsLoading, setRevisionsLoading] = useState(false)
  const [timeKind, setTimeKind] = useState<TimeExpression['kind'] | ''>('')
  const [options, setOptions] = useState<EventOptions>(emptyOptions)
  const [loading, setLoading] = useState(true)
  const [optionsLoading, setOptionsLoading] = useState(false)
  const [submitting, setSubmitting] = useState(false)
  const [message, setMessage] = useState('')
  const createKey = useRef<string | null>(null)

  const loadEvents = useCallback(async (signal?: AbortSignal) => {
    setLoading(true)
    try {
      setEvents(await listManagedEvents(signal))
      setMessage('')
    } catch (error) {
      if (error instanceof DOMException && error.name === 'AbortError') return
      setMessage(errorMessage(error, '暂时无法读取历史事件草稿。'))
    } finally {
      if (!signal?.aborted) setLoading(false)
    }
  }, [])

  useEffect(() => {
    const controller = new AbortController()
    void listManagedEvents(controller.signal)
      .then((loadedEvents) => {
        setEvents(loadedEvents)
        setMessage('')
      })
      .catch((error: unknown) => {
        if (error instanceof DOMException && error.name === 'AbortError') return
        setMessage(errorMessage(error, '暂时无法读取历史事件草稿。'))
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false)
      })
    return () => controller.abort()
  }, [])

  async function beginCreate() {
    setCreating(true)
    setEditing(null)
    setRevisionEventID(null)
    setTimeKind('')
    await loadOptions()
  }

  async function beginEdit(event: ManagedEvent) {
    if (!event.draft) return
    setEditing(event)
    setCreating(false)
    setRevisionEventID(null)
    setTimeKind(event.draft.time?.kind ?? '')
    await loadOptions()
  }

  async function beginDraftFromCurrent(event: ManagedEvent) {
    setSubmitting(true)
    setMessage('')
    try {
      const created = await createManagedEventDraft(event.id)
      await loadEvents()
      await beginEdit(created)
      setMessage(`已从当前发布版本建立“${created.draft?.title || created.slug}”的活动草稿。`)
    } catch (error) {
      if (error instanceof EventDraftManagementError && error.code === 'event_draft_already_exists') {
        await loadEvents()
        setMessage('该历史事件已有活动草稿，列表已刷新。')
      } else {
        setMessage(errorMessage(error, '暂时无法从当前版本建立活动草稿。'))
      }
    } finally {
      setSubmitting(false)
    }
  }

  async function publishDraft(event: ManagedEvent) {
    setSubmitting(true)
    setMessage('')
    try {
      await publishManagedEvent(event)
      await loadEvents()
      setEditing(null)
      setMessage(event.publicationStatus === 'archived' ? '活动草稿已重新发布为新的事件版本。' : '活动草稿已发布为新的事件版本。')
    } catch (error) {
      if (error instanceof EventDraftManagementError && error.code === 'event_draft_version_conflict') {
        await loadEvents()
        setEditing(null)
        setMessage('活动草稿已被其他编辑者修改，列表已刷新，请检查后重新发布。')
      } else {
        setMessage(errorMessage(error, '暂时无法发布活动草稿。'))
      }
    } finally {
      setSubmitting(false)
    }
  }

  async function archiveEvent(event: ManagedEvent) {
    setSubmitting(true)
    setMessage('')
    try {
      await archiveManagedEvent(event)
      await loadEvents()
      setEditing(null)
      setRevisionEventID(null)
      setMessage(`历史事件“${event.draft?.title || event.slug}”已下线，所有发布版本仍保留在管理区。`)
    } catch (error) {
      if (error instanceof EventDraftManagementError && (
        error.code === 'event_version_conflict' || error.code === 'event_not_published'
      )) {
        await loadEvents()
        setEditing(null)
        setMessage('历史事件状态已发生变化，列表已刷新。')
      } else {
        setMessage(errorMessage(error, '暂时无法下线历史事件。'))
      }
    } finally {
      setSubmitting(false)
    }
  }

  async function showRevisions(event: ManagedEvent) {
    setRevisionEventID(event.id)
    setRevisionDetail(null)
    setEditing(null)
    setCreating(false)
    setRevisionsLoading(true)
    setMessage('')
    try {
      setRevisions(await listManagedEventRevisions(event.id))
    } catch (error) {
      setMessage(errorMessage(error, '暂时无法读取事件版本。'))
    } finally {
      setRevisionsLoading(false)
    }
  }

  async function showRevision(eventID: string, revisionNo: number) {
    setRevisionsLoading(true)
    setMessage('')
    try {
      setRevisionDetail(await getManagedEventRevision(eventID, revisionNo))
    } catch (error) {
      setMessage(errorMessage(error, '暂时无法读取指定事件版本。'))
    } finally {
      setRevisionsLoading(false)
    }
  }

  async function restoreRevision(eventID: string, revisionNo: number) {
    setSubmitting(true)
    setMessage('')
    try {
      const restored = await restoreManagedEventRevision(eventID, revisionNo)
      await loadEvents()
      await beginEdit(restored)
      setMessage(`已将版本 ${revisionNo} 复制为活动草稿，当前发布版本保持不变。`)
    } catch (error) {
      if (error instanceof EventDraftManagementError && error.code === 'event_draft_already_exists') {
        await loadEvents()
        setMessage('已有活动草稿，不能用旧版本覆盖；请先继续编辑或发布现有草稿。')
      } else {
        setMessage(errorMessage(error, '暂时无法恢复事件版本。'))
      }
    } finally {
      setSubmitting(false)
    }
  }

  async function loadOptions() {
    setOptionsLoading(true)
    setMessage('')
    try {
      const [regions, places, periods, figures, topicTags] = await Promise.all([
        listRegions('', undefined, 'active'),
        listContextEntities('place'),
        listContextEntities('historical-period'),
        listCanonicalEntities('figures', 'figures'),
        listCanonicalEntities('topic-tags', 'topicTags'),
      ])
      setOptions({
        regions,
        places: places.filter((entity) => entity.status === 'active'),
        periods: periods.filter((entity) => entity.status === 'active'),
        figures: figures.filter((entity) => entity.status === 'active'),
        topicTags: topicTags.filter((entity) => entity.status === 'active'),
      })
    } catch (error) {
      setMessage(errorMessage(error, '暂时无法读取草稿可选的规范实体。'))
    } finally {
      setOptionsLoading(false)
    }
  }

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const form = event.currentTarget
    const data = new FormData(form)
    const slug = String(data.get('slug') ?? '')
    let draft: EventDraftWriteInput
    try {
      draft = draftFromForm(data, timeKind)
    } catch (error) {
      setMessage(errorMessage(error, '时间表述无效。'))
      return
    }
    setSubmitting(true)
    setMessage('')
    try {
      if (editing) {
        let current = editing
        if (slug.trim().toLowerCase() !== editing.slug) {
          current = await updateManagedEventSlug(editing, slug)
        }
        const updated = await updateManagedEventDraft(current, draft)
        setEditing(updated)
        await loadEvents()
        setMessage(`已保存“${updated.draft?.title || updated.slug}”的活动草稿。`)
      } else {
        createKey.current ??= globalThis.crypto.randomUUID()
        const created = await createManagedEvent(slug, draft, createKey.current)
        createKey.current = null
        setCreating(false)
        setEditing(created)
        setTimeKind(created.draft?.time?.kind ?? '')
        await loadEvents()
        setMessage(`已创建历史事件“${created.draft?.title || created.slug}”。`)
      }
    } catch (error) {
      if (error instanceof EventDraftManagementError && error.status < 500 && !editing) {
        createKey.current = null
      }
      if (error instanceof EventDraftManagementError && (
        error.code === 'event_draft_version_conflict' || error.code === 'event_version_conflict'
      )) {
        setEditing(null)
        setCreating(false)
        await loadEvents()
        setMessage('该历史事件已被其他编辑者修改，列表已刷新，请重新编辑。')
      } else {
        setMessage(errorMessage(error, '暂时无法保存历史事件草稿。'))
      }
    } finally {
      setSubmitting(false)
    }
  }

  const selected = editing?.draft
  const revisionEvent = revisionEventID ? events.find((event) => event.id === revisionEventID) ?? null : null

  return (
    <section className="event-draft-manager" aria-labelledby="event-drafts-title">
      <div className="region-manager__heading">
        <div>
          <p className="admin-kicker">内容录入</p>
          <h2 id="event-drafts-title">历史事件草稿</h2>
          <p>先创建可不完整的活动草稿，再逐步补齐时间表述和规范实体关联。</p>
        </div>
        <button type="button" onClick={() => void beginCreate()} disabled={optionsLoading}>创建历史事件</button>
      </div>

      <div className="event-draft-manager__layout">
        <div className="region-list" aria-live="polite" aria-busy={loading}>
          {loading && <p>正在读取历史事件草稿…</p>}
          {!loading && events.length === 0 && <p>尚未创建历史事件草稿。</p>}
          {!loading && events.map((event) => (
            <article className="region-row" key={event.id}>
              <div>
                <h3>{event.draft?.title || '未命名历史事件'}</h3>
                <p>/{event.slug} · {publicationLabel(event.publicationStatus)}</p>
                <span>{event.draft ? `草稿版本 ${event.draft.lockVersion}` : '没有活动草稿'}</span>
              </div>
              <div className="region-row__actions">
                {event.draft ? (
                  <button type="button" onClick={() => void beginEdit(event)}>编辑草稿</button>
                ) : event.publicationStatus !== 'unpublished' ? (
                  <button type="button" disabled={submitting} onClick={() => void beginDraftFromCurrent(event)}>从当前版本建立草稿</button>
                ) : null}
                {event.publicationStatus !== 'unpublished' && (
                  <button className="admin-button--secondary" type="button" onClick={() => void showRevisions(event)}>管理版本</button>
                )}
                {userRole === 'administrator' && event.draft && (
                  <button type="button" disabled={submitting} onClick={() => void publishDraft(event)}>
                    {event.publicationStatus === 'archived' ? '重新发布草稿' : '发布草稿'}
                  </button>
                )}
                {userRole === 'administrator' && event.publicationStatus === 'published' && (
                  <button className="admin-button--secondary" type="button" disabled={submitting} onClick={() => void archiveEvent(event)}>下线事件</button>
                )}
              </div>
            </article>
          ))}
        </div>

        {(creating || editing) && (
          <form
            key={editing ? `${editing.id}-${editing.draft?.lockVersion}` : 'new-event'}
            className="event-draft-form"
            onSubmit={submit}
            onChange={() => { if (!editing) createKey.current = null }}
          >
            <div className="event-draft-form__heading">
              <h3>{editing ? '编辑活动草稿' : '创建历史事件'}</h3>
              <button className="admin-button--secondary" type="button" onClick={() => { setCreating(false); setEditing(null) }}>关闭</button>
            </div>
            <label htmlFor="event-slug">slug</label>
            <input id="event-slug" name="slug" required maxLength={120} pattern="[a-z0-9]+(?:-[a-z0-9]+)*" defaultValue={editing?.slug ?? ''} />
            <label htmlFor="event-title">标题（草稿可暂缺）</label>
            <input id="event-title" name="title" maxLength={200} defaultValue={selected?.title ?? ''} />
            <label htmlFor="event-summary">摘要</label>
            <textarea id="event-summary" name="summary" maxLength={1000} defaultValue={selected?.summary ?? ''} />
            <label htmlFor="event-narrative">正文</label>
            <textarea id="event-narrative" name="narrative" maxLength={100000} defaultValue={selected?.narrative ?? ''} />

            <div className="event-draft-form__columns">
              <label htmlFor="event-category">主分类
                <select id="event-category" name="primaryCategory" defaultValue={selected?.primaryCategory ?? ''}>
                  <option value="">暂不设置</option>
                  {primaryCategories.map((category) => <option key={category}>{category}</option>)}
                </select>
              </label>
              <label htmlFor="event-prominence">事件显著度
                <select id="event-prominence" name="prominence" defaultValue={selected?.prominence ?? ''}>
                  <option value="">暂不设置</option>
                  <option value="1">L1 地标事件</option>
                  <option value="2">L2 重要事件</option>
                  <option value="3">L3 细节事件</option>
                </select>
              </label>
              <label htmlFor="event-display-order">展示顺序
                <input id="event-display-order" name="displayOrder" type="number" min={-1000000} max={1000000} defaultValue={selected?.displayOrder ?? 1000} />
              </label>
            </div>

            <label htmlFor="event-time-kind">时间表述
              <select id="event-time-kind" name="timeKind" value={timeKind} onChange={(event) => setTimeKind(event.target.value as TimeExpression['kind'] | '')}>
                <option value="">暂不设置</option>
                <option value="exact-date">精确日期</option>
                <option value="year">年份</option>
                <option value="circa">约数年份</option>
                <option value="interval">闭区间</option>
              </select>
            </label>
            <TimeFields kind={timeKind} value={selected?.time ?? null} />

            {optionsLoading ? <p>正在读取可选规范实体…</p> : (
              <div className="event-draft-form__relations">
                <EntityChoices legend="地区（发布前至少一个）" name="regionIds" entities={options.regions} selected={selected?.regions} />
                <EntityChoices legend="地点（可多选）" name="placeIds" entities={options.places} selected={selected?.places} />
                <EntityChoices legend="历史时期（可多选）" name="periodIds" entities={options.periods} selected={selected?.periods} />
                <EntityChoices legend="历史人物（可多选）" name="figureIds" entities={options.figures} selected={selected?.figures} />
                <EntityChoices legend="主题标签（可多选）" name="topicTagIds" entities={options.topicTags} selected={selected?.topicTags} />
              </div>
            )}
            <button type="submit" disabled={submitting || optionsLoading}>{submitting ? '正在保存…' : '保存活动草稿'}</button>
          </form>
        )}

        {revisionEvent && !creating && !editing && (
          <section className="event-revision-panel" aria-labelledby="event-revisions-heading">
            <div className="event-draft-form__heading">
              <div>
                <p className="admin-kicker">不可变发布记录</p>
                <h3 id="event-revisions-heading">/{revisionEvent.slug} 的事件版本</h3>
              </div>
              <button className="admin-button--secondary" type="button" onClick={() => { setRevisionEventID(null); setRevisionDetail(null) }}>关闭</button>
            </div>
            {revisionsLoading && <p>正在读取事件版本…</p>}
            {!revisionsLoading && revisions.length === 0 && <p>该历史事件尚无发布版本。</p>}
            <div className="event-revision-list">
              {revisions.map((revision) => (
                <article className="event-revision-row" key={revision.id}>
                  <div>
                    <h4>版本 {revision.revisionNo}{revision.current ? '（当前）' : ''}</h4>
                    <p>{revision.title}</p>
                    <span>{revision.publishedBy.email} · {formatPublishedAt(revision.publishedAt)}</span>
                  </div>
                  <div className="region-row__actions">
                    <button type="button" onClick={() => void showRevision(revisionEvent.id, revision.revisionNo)}>查看版本</button>
                    <button
                      className="admin-button--secondary"
                      type="button"
                      disabled={submitting || Boolean(revisionEvent.draft)}
                      onClick={() => void restoreRevision(revisionEvent.id, revision.revisionNo)}
                    >恢复为活动草稿</button>
                  </div>
                </article>
              ))}
            </div>
            {revisionEvent.draft && <p className="admin-form-message">已有活动草稿，版本恢复不会覆盖它。</p>}
            {revisionDetail && (
              <article className="event-revision-detail" aria-labelledby="event-revision-detail-heading">
                <div className="event-draft-form__heading">
                  <h4 id="event-revision-detail-heading">版本 {revisionDetail.revisionNo}：{revisionDetail.title}</h4>
                  <span>{revisionDetail.current ? '当前发布版本' : '历史发布版本'}</span>
                </div>
                <p>{revisionDetail.summary}</p>
                <p>{revisionDetail.narrative}</p>
                <dl>
                  <div><dt>发布人</dt><dd>{revisionDetail.publishedBy.email}</dd></div>
                  <div><dt>发布时间</dt><dd>{formatPublishedAt(revisionDetail.publishedAt)}</dd></div>
                  <div><dt>主分类</dt><dd>{revisionDetail.primaryCategory}</dd></div>
                  <div><dt>事件显著度</dt><dd>L{revisionDetail.prominence}</dd></div>
                </dl>
                <RevisionEntities label="地区" values={revisionDetail.regions} />
                <RevisionEntities label="地点" values={revisionDetail.places} />
                <RevisionEntities label="历史时期" values={revisionDetail.periods} />
                <RevisionEntities label="历史人物" values={revisionDetail.figures} />
                <RevisionEntities label="主题标签" values={revisionDetail.topicTags} />
              </article>
            )}
          </section>
        )}
      </div>
      {message && <p className="admin-form-message" role="status">{message}</p>}
    </section>
  )
}

function TimeFields({ kind, value }: { kind: TimeExpression['kind'] | '', value: TimeExpression | null }) {
  if (!kind) return null
  if (kind === 'interval') {
    const interval = value?.kind === 'interval' ? value : null
    return (
      <div className="event-draft-form__time-grid">
        <YearFields prefix="start" label="开始" value={interval?.start} />
        <label><input name="startCirca" type="checkbox" defaultChecked={interval?.start.circa ?? false} /> 开始年份为约数</label>
        <YearFields prefix="end" label="结束" value={interval?.end} />
        <label><input name="endCirca" type="checkbox" defaultChecked={interval?.end.circa ?? false} /> 结束年份为约数</label>
      </div>
    )
  }
  const point = value?.kind === 'exact-date' ? value.date : value?.kind === kind ? value.year : undefined
  return (
    <div className="event-draft-form__time-grid">
      <YearFields prefix="point" label="时间" value={point} />
      {kind === 'exact-date' && (
        <>
          <label htmlFor="event-month">月<input id="event-month" name="month" type="number" min="1" max="12" required defaultValue={value?.kind === 'exact-date' ? value.date.month : 1} /></label>
          <label htmlFor="event-day">日<input id="event-day" name="day" type="number" min="1" max="31" required defaultValue={value?.kind === 'exact-date' ? value.date.day : 1} /></label>
        </>
      )}
    </div>
  )
}

function YearFields({ prefix, label, value }: { prefix: string, label: string, value?: { era: 'BCE' | 'CE', year: number } }) {
  return (
    <div className="event-draft-form__year">
      <label htmlFor={`event-${prefix}-era`}>{label}纪元
        <select id={`event-${prefix}-era`} name={`${prefix}Era`} defaultValue={value?.era ?? 'CE'}>
          <option value="BCE">公元前</option>
          <option value="CE">公元</option>
        </select>
      </label>
      <label htmlFor={`event-${prefix}-year`}>{label}年份
        <input id={`event-${prefix}-year`} name={`${prefix}Year`} type="number" min="1" max="999999" required defaultValue={value?.year ?? 1} />
      </label>
    </div>
  )
}

function EntityChoices({ legend, name, entities, selected = [] }: {
  legend: string
  name: string
  entities: EventEntityReference[]
  selected?: EventEntityReference[]
}) {
  const selectedIDs = new Set(selected.map((entity) => entity.id))
  const entityIDs = new Set(entities.map((entity) => entity.id))
  const choices = [...selected.filter((entity) => !entityIDs.has(entity.id)), ...entities]
  return (
    <fieldset className="admin-region-choices">
      <legend>{legend}</legend>
      {choices.length === 0 && <p>暂无可选的有效规范实体。</p>}
      {choices.map((entity) => (
        <label key={entity.id}>
          <input type="checkbox" name={name} value={entity.id} defaultChecked={selectedIDs.has(entity.id)} />
          <span>{displayEntity(entity)}</span>
        </label>
      ))}
    </fieldset>
  )
}

function RevisionEntities({ label, values }: { label: string, values: EventEntityReference[] }) {
  if (values.length === 0) return null
  return <p><strong>{label}：</strong>{values.map(displayEntity).join('、')}</p>
}

function draftFromForm(data: FormData, kind: TimeExpression['kind'] | ''): EventDraftWriteInput {
  const category = String(data.get('primaryCategory') ?? '')
  const prominence = String(data.get('prominence') ?? '')
  return {
    title: String(data.get('title') ?? ''),
    summary: String(data.get('summary') ?? ''),
    narrative: String(data.get('narrative') ?? ''),
    time: timeFromForm(data, kind),
    primaryCategory: category ? category as PrimaryCategory : null,
    prominence: prominence ? Number(prominence) as EventProminence : null,
    displayOrder: Number(data.get('displayOrder') ?? 1000),
    regionIds: data.getAll('regionIds').map(String),
    placeIds: data.getAll('placeIds').map(String),
    periodIds: data.getAll('periodIds').map(String),
    figureIds: data.getAll('figureIds').map(String),
    topicTagIds: data.getAll('topicTagIds').map(String),
  }
}

function timeFromForm(data: FormData, kind: TimeExpression['kind'] | ''): TimeExpression | null {
  if (!kind) return null
  if (kind === 'interval') {
    return {
      kind,
      start: {
        era: String(data.get('startEra')) as 'BCE' | 'CE',
        year: Number(data.get('startYear')),
        circa: data.get('startCirca') === 'on',
      },
      end: {
        era: String(data.get('endEra')) as 'BCE' | 'CE',
        year: Number(data.get('endYear')),
        circa: data.get('endCirca') === 'on',
      },
    }
  }
  const year = {
    era: String(data.get('pointEra')) as 'BCE' | 'CE',
    year: Number(data.get('pointYear')),
  }
  if (kind === 'exact-date') {
    return { kind, date: { ...year, month: Number(data.get('month')), day: Number(data.get('day')) } }
  }
  return { kind, year }
}

function displayEntity(entity: EventEntityReference): string {
  return entity.disambiguationLabel ? `${entity.name}（${entity.disambiguationLabel}）` : entity.name
}

function publicationLabel(status: ManagedEvent['publicationStatus']): string {
  if (status === 'published') return '已发布'
  if (status === 'archived') return '已下线'
  return '未发布'
}

function formatPublishedAt(value: string): string {
  return new Intl.DateTimeFormat('zh-CN', { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(value))
}

function errorMessage(error: unknown, fallback: string): string {
  return error instanceof Error ? error.message : fallback
}
