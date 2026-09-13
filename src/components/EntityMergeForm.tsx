import type { EntityMergeImpact } from '../data/entityGovernanceClient'
import type { FormEvent } from 'react'

export interface GovernedEntityView {
  id: string
  name: string
  disambiguationLabel: string | null
  status: 'active' | 'inactive' | 'merged'
}

interface EntityMergeFormProps<T extends GovernedEntityView> {
  noun: string
  idPrefix: string
  source: T
  entities: T[]
  targetID: string
  impact: EntityMergeImpact | null
  confirmed: boolean
  submitting: boolean
  onTargetChange: (targetID: string) => void
  onReview: () => void
  onConfirmedChange: (confirmed: boolean) => void
  onCancel: () => void
  onSubmit: (event: FormEvent<HTMLFormElement>) => void
}

export default function EntityMergeForm<T extends GovernedEntityView>({
  noun,
  idPrefix,
  source,
  entities,
  targetID,
  impact,
  confirmed,
  submitting,
  onTargetChange,
  onReview,
  onConfirmedChange,
  onCancel,
  onSubmit,
}: EntityMergeFormProps<T>) {
  return (
    <form className="admin-entity-form" onSubmit={onSubmit}>
      <h3>合并{noun}</h3>
      <p>来源：{displayEntity(source)}</p>
      <label htmlFor={`${idPrefix}-merge-target`}>有效目标实体</label>
      <select
        id={`${idPrefix}-merge-target`}
        value={targetID}
        required
        onChange={(event) => onTargetChange(event.target.value)}
      >
        <option value="">请选择目标</option>
        {entities.filter((entity) => entity.status === 'active' && entity.id !== source.id).map((entity) => (
          <option key={entity.id} value={entity.id}>{displayEntity(entity)}</option>
        ))}
      </select>
      <button type="button" disabled={submitting || !targetID} onClick={onReview}>查看影响</button>
      {impact && (
        <div className="admin-merge-impact" role="status">
          <p>将影响 {impact.draftCount} 份活动草稿、{impact.revisionCount} 个不可变版本，其中 {impact.publishedEventCount} 个当前公开事件会立即显示目标名称。</p>
          <label>
            <input type="checkbox" checked={confirmed} onChange={(event) => onConfirmedChange(event.target.checked)} />
            我已确认影响范围和合并目标
          </label>
        </div>
      )}
      <div className="admin-entity-form__actions">
        <button type="submit" disabled={submitting || !impact || !confirmed}>确认合并</button>
        <button className="admin-button--secondary" type="button" onClick={onCancel}>取消</button>
      </div>
    </form>
  )
}

function displayEntity(entity: GovernedEntityView): string {
  return entity.disambiguationLabel ? `${entity.name}（${entity.disambiguationLabel}）` : entity.name
}
