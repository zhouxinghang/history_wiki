import { useCallback, useEffect, useRef, useState, type FormEvent } from 'react'
import type { UserRole } from '../data/authClient'
import {
  CanonicalEntityManagementError,
  createCanonicalEntity,
  listCanonicalEntities,
  newIdempotencyKey,
  updateCanonicalEntity,
  type CanonicalEntityListField,
  type CanonicalEntityResource,
  type CanonicalEntityStatus,
  type ManagedCanonicalEntity,
} from '../data/canonicalEntityClient'
import {
  EntityGovernanceError,
  getEntityMergeImpact,
  mergeEntity,
  type EntityMergeImpact,
} from '../data/entityGovernanceClient'
import EntityMergeForm from './EntityMergeForm'

export interface CanonicalEntityDefinition {
  resource: CanonicalEntityResource
  listField: CanonicalEntityListField
  title: string
  singular: string
  description: string
  versionConflictCode: string
}

interface CanonicalEntityManagerProps {
  definition: CanonicalEntityDefinition
  userRole: UserRole
  onChange?: () => void
}

export default function CanonicalEntityManager({ definition, userRole, onChange }: CanonicalEntityManagerProps) {
  const [entities, setEntities] = useState<ManagedCanonicalEntity[]>([])
  const [query, setQuery] = useState('')
  const [loading, setLoading] = useState(true)
  const [message, setMessage] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const [selectedEntityID, setSelectedEntityID] = useState<string | null>(null)
  const [editingEntity, setEditingEntity] = useState<ManagedCanonicalEntity | null>(null)
  const [mergeSource, setMergeSource] = useState<ManagedCanonicalEntity | null>(null)
  const [mergeTargetID, setMergeTargetID] = useState('')
  const [mergeImpact, setMergeImpact] = useState<EntityMergeImpact | null>(null)
  const [mergeConfirmed, setMergeConfirmed] = useState(false)
  const createKey = useRef<string | null>(null)
  const idPrefix = `canonical-${definition.resource}`

  const load = useCallback(async (searchQuery: string, signal?: AbortSignal) => {
    setLoading(true)
    try {
      setEntities(await listCanonicalEntities(definition.resource, definition.listField, searchQuery, signal))
      setMessage('')
    } catch (error) {
      if (error instanceof DOMException && error.name === 'AbortError') return
      setMessage(errorMessage(error, `暂时无法读取${definition.singular}。`))
    } finally {
      setLoading(false)
    }
  }, [definition.listField, definition.resource, definition.singular])

  useEffect(() => {
    const controller = new AbortController()
    void listCanonicalEntities(definition.resource, definition.listField, '', controller.signal)
      .then((loadedEntities) => {
        setEntities(loadedEntities)
        setMessage('')
      })
      .catch((error: unknown) => {
        if (error instanceof DOMException && error.name === 'AbortError') return
        setMessage(errorMessage(error, `暂时无法读取${definition.singular}。`))
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false)
      })
    return () => controller.abort()
  }, [definition.listField, definition.resource, definition.singular])

  async function submitSearch(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    await load(query)
  }

  async function submitCreate(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const form = event.currentTarget
    const data = new FormData(form)
    setSubmitting(true)
    setMessage('')
    createKey.current ??= newIdempotencyKey()
    try {
      const created = await createCanonicalEntity(definition.resource, {
        name: String(data.get('entity-name') ?? ''),
        disambiguationLabel: nullableText(data.get('entity-disambiguation')),
      }, createKey.current)
      createKey.current = null
      form.reset()
      setSelectedEntityID(created.id)
      onChange?.()
      await load(query)
      setMessage(`已创建${definition.singular}“${displayEntity(created)}”。`)
    } catch (error) {
      if (error instanceof CanonicalEntityManagementError && error.status < 500) {
        createKey.current = null
      }
      setMessage(errorMessage(error, `暂时无法创建${definition.singular}。`))
    } finally {
      setSubmitting(false)
    }
  }

  async function submitEdit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!editingEntity || editingEntity.status === 'merged') return
    const data = new FormData(event.currentTarget)
    setSubmitting(true)
    setMessage('')
    try {
      const updated = await updateCanonicalEntity(definition.resource, editingEntity, {
        name: String(data.get('edit-entity-name') ?? ''),
        disambiguationLabel: nullableText(data.get('edit-entity-disambiguation')),
        status: String(data.get('edit-entity-status')) as Exclude<CanonicalEntityStatus, 'merged'>,
      })
      setEditingEntity(null)
      onChange?.()
      await load(query)
      setMessage(`已更新${definition.singular}“${displayEntity(updated)}”。`)
    } catch (error) {
      if (error instanceof CanonicalEntityManagementError && error.code === definition.versionConflictCode) {
        setEditingEntity(null)
        await load(query)
        setMessage(`该${definition.singular}已被其他管理员修改，列表已刷新，请重新编辑。`)
      } else {
        setMessage(errorMessage(error, `暂时无法修改${definition.singular}。`))
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
      setMergeImpact(await getEntityMergeImpact(definition.resource, mergeSource.id))
      setMergeConfirmed(false)
    } catch (error) {
      setMessage(errorMessage(error, `暂时无法计算${definition.singular}合并影响。`))
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
      await mergeEntity(definition.resource, mergeSource, mergeTargetID, mergeImpact)
      const sourceName = displayEntity(mergeSource)
      setMergeSource(null)
      setMergeTargetID('')
      setMergeImpact(null)
      setMergeConfirmed(false)
      onChange?.()
      await load(query)
      setMessage(`已合并${definition.singular}“${sourceName}”。旧标识仍会解析到目标实体。`)
    } catch (error) {
      if (error instanceof EntityGovernanceError && ['merge_impact_changed', definition.versionConflictCode].includes(error.code ?? '')) {
        setMergeImpact(null)
        setMergeConfirmed(false)
      }
      setMessage(errorMessage(error, `暂时无法合并${definition.singular}。`))
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <section className="region-manager" aria-labelledby={`${idPrefix}-title`}>
      <div className="region-manager__heading">
        <div>
          <p className="admin-kicker">规范实体</p>
          <h2 id={`${idPrefix}-title`}>{definition.title}</h2>
          <p>{definition.description}</p>
        </div>
        {selectedEntityID && (
          <p className="region-manager__selection" role="status">
            已选择：{displayEntity(entities.find((entity) => entity.id === selectedEntityID))}
          </p>
        )}
      </div>

      <div className="region-manager__grid">
        <div>
          <form className="region-search" onSubmit={submitSearch}>
            <label htmlFor={`${idPrefix}-search`}>搜索名称或消歧名称</label>
            <div>
              <input id={`${idPrefix}-search`} value={query} maxLength={100} onChange={(event) => setQuery(event.target.value)} />
              <button type="submit" disabled={loading}>搜索</button>
            </div>
          </form>

          <div className="region-list" aria-live="polite" aria-busy={loading}>
            {loading && <p>正在读取{definition.singular}…</p>}
            {!loading && entities.length === 0 && <p>没有符合条件的{definition.singular}。</p>}
            {!loading && entities.map((entity) => (
              <article className="region-row" key={entity.id}>
                <div>
                  <h3>{entity.name}</h3>
                  <p>{entity.disambiguationLabel ? `消歧：${entity.disambiguationLabel}` : '无消歧名称'}</p>
                  <span>{statusLabel(entity.status)} · 版本 {entity.lockVersion}</span>
                  {entity.mergedIntoId && <small>目标 ID：{entity.mergedIntoId}</small>}
                </div>
                <div className="region-row__actions">
                  <button
                    type="button"
                    className="admin-button--secondary"
                    disabled={entity.status !== 'active'}
                    aria-pressed={selectedEntityID === entity.id}
                    onClick={() => setSelectedEntityID(entity.id)}
                  >
                    {selectedEntityID === entity.id ? '已选择' : '选择'}
                  </button>
                  {userRole === 'administrator' && entity.status !== 'merged' && (
                    <button type="button" onClick={() => setEditingEntity(entity)}>编辑</button>
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
          <form className="admin-entity-form" onSubmit={submitCreate}>
            <h3>创建{definition.singular}</h3>
            <label htmlFor={`${idPrefix}-name`}>当前名称</label>
            <input id={`${idPrefix}-name`} name="entity-name" maxLength={120} required onChange={() => { createKey.current = null }} />
            <label htmlFor={`${idPrefix}-disambiguation`}>消歧名称（可选）</label>
            <input id={`${idPrefix}-disambiguation`} name="entity-disambiguation" maxLength={120} onChange={() => { createKey.current = null }} />
            <button type="submit" disabled={submitting}>创建{definition.singular}</button>
          </form>

          {editingEntity && userRole === 'administrator' && (
            <form key={editingEntity.id} className="admin-entity-form" onSubmit={submitEdit}>
              <h3>编辑{definition.singular}</h3>
              <label htmlFor={`${idPrefix}-edit-name`}>当前名称</label>
              <input id={`${idPrefix}-edit-name`} name="edit-entity-name" maxLength={120} required defaultValue={editingEntity.name} />
              <label htmlFor={`${idPrefix}-edit-disambiguation`}>消歧名称（可选）</label>
              <input id={`${idPrefix}-edit-disambiguation`} name="edit-entity-disambiguation" maxLength={120} defaultValue={editingEntity.disambiguationLabel ?? ''} />
              <label htmlFor={`${idPrefix}-edit-status`}>状态</label>
              <select id={`${idPrefix}-edit-status`} name="edit-entity-status" defaultValue={editingEntity.status}>
                <option value="active">有效</option>
                <option value="inactive">停用</option>
              </select>
              <div className="admin-entity-form__actions">
                <button type="submit" disabled={submitting}>保存修改</button>
                <button className="admin-button--secondary" type="button" onClick={() => setEditingEntity(null)}>取消</button>
              </div>
            </form>
          )}

          {mergeSource && userRole === 'administrator' && (
            <EntityMergeForm
              noun={definition.singular}
              idPrefix={idPrefix}
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

function nullableText(value: FormDataEntryValue | null): string | null {
  const text = String(value ?? '').trim()
  return text || null
}

function displayEntity(entity?: ManagedCanonicalEntity): string {
  if (!entity) return '当前列表以外的实体'
  return entity.disambiguationLabel ? `${entity.name}（${entity.disambiguationLabel}）` : entity.name
}

function statusLabel(status: CanonicalEntityStatus): string {
  if (status === 'active') return '有效'
  if (status === 'inactive') return '已停用'
  return '已合并'
}

function errorMessage(error: unknown, fallback: string): string {
  return error instanceof Error ? error.message : fallback
}
