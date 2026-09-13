import { useCallback, useEffect, useRef, useState, type FormEvent } from 'react'
import type { UserRole } from '../data/authClient'
import {
  ContextEntityManagementError,
  createContextEntity,
  listContextEntities,
  updateContextEntity,
  type ContextEntity,
  type ContextEntityKind,
} from '../data/contextEntityClient'
import { listRegions, type Region, type RegionStatus } from '../data/regionClient'
import {
  EntityGovernanceError,
  getEntityMergeImpact,
  mergeEntity,
  type EntityMergeImpact,
} from '../data/entityGovernanceClient'
import EntityMergeForm from './EntityMergeForm'

interface ContextEntityManagerProps {
  kind: ContextEntityKind
  userRole: UserRole
  regionsVersion?: number
}

const copy = {
  place: {
    title: '地点管理',
    noun: '地点',
    description: '地点可以归属于零个、一个或多个地区，同名地点通过消歧名称区分。',
    emptyRegions: '尚未关联地区',
    conflictCode: 'place_version_conflict',
  },
  'historical-period': {
    title: '历史时期管理',
    noun: '历史时期',
    description: '历史时期必须置于至少一个地区或文明语境中，也可以跨越多个地区。',
    emptyRegions: '缺少地区语境',
    conflictCode: 'historical_period_version_conflict',
  },
} satisfies Record<ContextEntityKind, {
  title: string
  noun: string
  description: string
  emptyRegions: string
  conflictCode: string
}>

export default function ContextEntityManager({ kind, userRole, regionsVersion = 0 }: ContextEntityManagerProps) {
  const labels = copy[kind]
  const prefix = kind === 'place' ? 'place' : 'historical-period'
  const [entities, setEntities] = useState<ContextEntity[]>([])
  const [regions, setRegions] = useState<Region[]>([])
  const [query, setQuery] = useState('')
  const [loading, setLoading] = useState(true)
  const [regionsLoading, setRegionsLoading] = useState(true)
  const [message, setMessage] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const [selectedID, setSelectedID] = useState<string | null>(null)
  const [editing, setEditing] = useState<ContextEntity | null>(null)
  const [mergeSource, setMergeSource] = useState<ContextEntity | null>(null)
  const [mergeTargetID, setMergeTargetID] = useState('')
  const [mergeImpact, setMergeImpact] = useState<EntityMergeImpact | null>(null)
  const [mergeConfirmed, setMergeConfirmed] = useState(false)
  const createKey = useRef<string | null>(null)

  const load = useCallback(async (searchQuery: string, signal?: AbortSignal) => {
    setLoading(true)
    try {
      setEntities(await listContextEntities(kind, searchQuery, signal))
      setMessage('')
    } catch (error) {
      if (error instanceof DOMException && error.name === 'AbortError') return
      setMessage(errorMessage(error, `暂时无法读取${labels.noun}。`))
    } finally {
      if (!signal?.aborted) setLoading(false)
    }
  }, [kind, labels.noun])

  useEffect(() => {
    const controller = new AbortController()
    void listContextEntities(kind, '', controller.signal)
      .then((loadedEntities) => {
        setEntities(loadedEntities)
        setMessage('')
      })
      .catch((error: unknown) => {
        if (error instanceof DOMException && error.name === 'AbortError') return
        setMessage(errorMessage(error, `暂时无法读取${labels.noun}。`))
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false)
      })
    return () => controller.abort()
  }, [kind, labels.noun])

  useEffect(() => {
    const controller = new AbortController()
    void listRegions('', controller.signal, 'active')
      .then(setRegions)
      .catch((error: unknown) => {
        if (error instanceof DOMException && error.name === 'AbortError') return
        setMessage(errorMessage(error, '暂时无法读取可选地区。'))
      })
      .finally(() => {
        if (!controller.signal.aborted) setRegionsLoading(false)
      })
    return () => controller.abort()
  }, [regionsVersion])

  async function submitSearch(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    await load(query)
  }

  async function submitCreate(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const form = event.currentTarget
    const data = new FormData(form)
    const regionIds = data.getAll('regionIds').map(String)
    if (kind === 'historical-period' && regionIds.length === 0) {
      setMessage('有效历史时期必须选择至少一个地区语境。')
      return
    }
    setSubmitting(true)
    setMessage('')
    createKey.current ??= globalThis.crypto.randomUUID()
    try {
      const created = await createContextEntity(kind, {
        name: String(data.get('name') ?? ''),
        disambiguationLabel: nullableText(data.get('disambiguationLabel')),
        regionIds,
      }, createKey.current)
      createKey.current = null
      form.reset()
      setSelectedID(created.id)
      await load(query)
      setMessage(`已创建${labels.noun}“${displayEntity(created)}”。`)
    } catch (error) {
      if (error instanceof ContextEntityManagementError && error.status < 500) {
        createKey.current = null
      }
      setMessage(errorMessage(error, `暂时无法创建${labels.noun}。`))
    } finally {
      setSubmitting(false)
    }
  }

  async function submitEdit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!editing || editing.status === 'merged') return
    const data = new FormData(event.currentTarget)
    const regionIds = data.getAll('regionIds').map(String)
    const status = String(data.get('status')) as Exclude<RegionStatus, 'merged'>
    if (kind === 'historical-period' && status === 'active' && regionIds.length === 0) {
      setMessage('有效历史时期必须保留至少一个地区语境。')
      return
    }
    setSubmitting(true)
    setMessage('')
    try {
      const updated = await updateContextEntity(kind, editing, {
        name: String(data.get('name') ?? ''),
        disambiguationLabel: nullableText(data.get('disambiguationLabel')),
        status,
        regionIds,
      })
      setEditing(null)
      await load(query)
      setMessage(`已更新${labels.noun}“${displayEntity(updated)}”。`)
    } catch (error) {
      if (error instanceof ContextEntityManagementError && error.code === labels.conflictCode) {
        setEditing(null)
        await load(query)
        setMessage(`该${labels.noun}已被其他管理员修改，列表已刷新，请重新编辑。`)
      } else {
        setMessage(errorMessage(error, `暂时无法修改${labels.noun}。`))
      }
    } finally {
      setSubmitting(false)
    }
  }

  async function reviewMerge() {
    if (!mergeSource || !mergeTargetID) return
    setSubmitting(true)
    setMessage('')
    try {
      setMergeImpact(await getEntityMergeImpact(kind, mergeSource.id))
      setMergeConfirmed(false)
    } catch (error) {
      setMessage(errorMessage(error, `暂时无法计算${labels.noun}合并影响。`))
    } finally {
      setSubmitting(false)
    }
  }

  async function submitMerge(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!mergeSource || !mergeTargetID || !mergeImpact || !mergeConfirmed) return
    setSubmitting(true)
    setMessage('')
    try {
      await mergeEntity(kind, mergeSource, mergeTargetID, mergeImpact)
      const sourceName = displayEntity(mergeSource)
      setMergeSource(null)
      setMergeTargetID('')
      setMergeImpact(null)
      setMergeConfirmed(false)
      await load(query)
      setMessage(`已合并${labels.noun}“${sourceName}”。旧标识仍会解析到目标实体。`)
    } catch (error) {
      if (error instanceof EntityGovernanceError && ['merge_impact_changed', labels.conflictCode].includes(error.code ?? '')) {
        setMergeImpact(null)
        setMergeConfirmed(false)
      }
      setMessage(errorMessage(error, `暂时无法合并${labels.noun}。`))
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <section className="region-manager" aria-labelledby={`${prefix}-manager-title`}>
      <div className="region-manager__heading">
        <div>
          <p className="admin-kicker">地区语境</p>
          <h2 id={`${prefix}-manager-title`}>{labels.title}</h2>
          <p>{labels.description}</p>
        </div>
        {selectedID && (
          <p className="region-manager__selection" role="status">
            已选择：{displayEntity(entities.find((entity) => entity.id === selectedID))}
          </p>
        )}
      </div>

      <div className="region-manager__grid">
        <div>
          <form className="region-search" onSubmit={submitSearch}>
            <label htmlFor={`${prefix}-search`}>搜索{labels.noun}名称或消歧名称</label>
            <div>
              <input
                id={`${prefix}-search`}
                value={query}
                maxLength={100}
                onChange={(event) => setQuery(event.target.value)}
              />
              <button type="submit" disabled={loading}>搜索</button>
            </div>
          </form>

          <div className="region-list" aria-live="polite" aria-busy={loading}>
            {loading && <p>正在读取{labels.noun}…</p>}
            {!loading && entities.length === 0 && <p>没有符合条件的{labels.noun}。</p>}
            {!loading && entities.map((entity) => (
              <article className="region-row" key={entity.id}>
                <div>
                  <h3>{entity.name}</h3>
                  <p>{entity.disambiguationLabel ? `消歧：${entity.disambiguationLabel}` : '无消歧名称'}</p>
                  <p>地区语境：{entity.regions.length > 0 ? entity.regions.map(displayRegion).join('、') : labels.emptyRegions}</p>
                  <span>{statusLabel(entity.status)} · 版本 {entity.lockVersion}</span>
                  {entity.mergedIntoId && <small>目标 ID：{entity.mergedIntoId}</small>}
                </div>
                <div className="region-row__actions">
                  <button
                    type="button"
                    className="admin-button--secondary"
                    disabled={entity.status !== 'active'}
                    aria-pressed={selectedID === entity.id}
                    onClick={() => setSelectedID(entity.id)}
                  >
                    {selectedID === entity.id ? '已选择' : '选择'}
                  </button>
                  {userRole === 'administrator' && entity.status !== 'merged' && (
                    <button type="button" onClick={() => setEditing(entity)}>编辑</button>
                  )}
                  {userRole === 'administrator' && entity.status !== 'merged' && (
                    <button
                      type="button"
                      className="admin-button--secondary"
                      onClick={() => {
                        setMergeSource(entity)
                        setMergeTargetID('')
                        setMergeImpact(null)
                        setMergeConfirmed(false)
                      }}
                    >合并</button>
                  )}
                </div>
              </article>
            ))}
          </div>
        </div>

        <aside className="region-manager__forms">
          <form className="admin-entity-form" onSubmit={submitCreate} onChange={() => { createKey.current = null }}>
            <h3>创建{labels.noun}</h3>
            <label htmlFor={`${prefix}-name`}>当前名称</label>
            <input id={`${prefix}-name`} name="name" maxLength={120} required />
            <label htmlFor={`${prefix}-disambiguation`}>消歧名称（可选）</label>
            <input id={`${prefix}-disambiguation`} name="disambiguationLabel" maxLength={120} />
            <RegionChoices
              id={`${prefix}-create-regions`}
              regions={regions}
              required={kind === 'historical-period'}
              noun={labels.noun}
            />
            <button type="submit" disabled={submitting || loading || regionsLoading}>创建{labels.noun}</button>
          </form>

          {editing && userRole === 'administrator' && (
            <form className="admin-entity-form" key={`${editing.id}-${editing.lockVersion}`} onSubmit={submitEdit}>
              <h3>编辑{labels.noun}</h3>
              <label htmlFor={`${prefix}-edit-name`}>当前名称</label>
              <input id={`${prefix}-edit-name`} name="name" maxLength={120} required defaultValue={editing.name} />
              <label htmlFor={`${prefix}-edit-disambiguation`}>消歧名称（可选）</label>
              <input
                id={`${prefix}-edit-disambiguation`}
                name="disambiguationLabel"
                maxLength={120}
                defaultValue={editing.disambiguationLabel ?? ''}
              />
              <label htmlFor={`${prefix}-edit-status`}>状态</label>
              <select id={`${prefix}-edit-status`} name="status" defaultValue={editing.status}>
                <option value="active">有效</option>
                <option value="inactive">停用</option>
              </select>
              <RegionChoices
                id={`${prefix}-edit-regions`}
                regions={regions}
                required={kind === 'historical-period'}
                noun={labels.noun}
                selected={new Set(editing.regions.map((region) => region.id))}
              />
              <div className="admin-entity-form__actions">
                <button type="submit" disabled={submitting}>保存修改</button>
                <button className="admin-button--secondary" type="button" onClick={() => setEditing(null)}>取消</button>
              </div>
            </form>
          )}

          {mergeSource && userRole === 'administrator' && (
            <EntityMergeForm
              noun={labels.noun}
              idPrefix={prefix}
              source={mergeSource}
              entities={entities}
              targetID={mergeTargetID}
              impact={mergeImpact}
              confirmed={mergeConfirmed}
              submitting={submitting}
              onTargetChange={(targetID) => {
                setMergeTargetID(targetID)
                setMergeImpact(null)
                setMergeConfirmed(false)
              }}
              onReview={reviewMerge}
              onConfirmedChange={setMergeConfirmed}
              onCancel={() => setMergeSource(null)}
              onSubmit={submitMerge}
            />
          )}
        </aside>
      </div>
      {message && <p className="admin-form-message" role="status">{message}</p>}
    </section>
  )
}

function RegionChoices({
  id,
  regions,
  noun,
  required,
  selected = new Set<string>(),
}: {
  id: string
  regions: Region[]
  noun: string
  required: boolean
  selected?: Set<string>
}) {
  return (
    <fieldset className="admin-region-choices" id={id}>
      <legend>地区语境{required ? '（至少选择一个）' : '（可选，可多选）'}</legend>
      {regions.length === 0 && <p>暂无可选的有效地区，请先创建地区。</p>}
      {regions.map((region) => (
        <label key={region.id}>
          <input name="regionIds" type="checkbox" value={region.id} defaultChecked={selected.has(region.id)} />
          <span>{displayRegion(region)}</span>
        </label>
      ))}
      <small>{noun}只能新增关联当前有效的地区。</small>
    </fieldset>
  )
}

function nullableText(value: FormDataEntryValue | null): string | null {
  const text = String(value ?? '').trim()
  return text || null
}

function displayEntity(entity?: ContextEntity): string {
  if (!entity) return '当前列表以外的规范实体'
  return entity.disambiguationLabel ? `${entity.name}（${entity.disambiguationLabel}）` : entity.name
}

function displayRegion(region: Pick<Region, 'name' | 'disambiguationLabel'>): string {
  return region.disambiguationLabel ? `${region.name}（${region.disambiguationLabel}）` : region.name
}

function statusLabel(status: RegionStatus): string {
  if (status === 'active') return '有效'
  if (status === 'inactive') return '已停用'
  return '已合并'
}

function errorMessage(error: unknown, fallback: string): string {
  return error instanceof Error ? error.message : fallback
}
