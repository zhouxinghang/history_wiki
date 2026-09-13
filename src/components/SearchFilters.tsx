import { useId, type ReactNode } from 'react'
import type {
  CanonicalEntityReference,
  HistoryEventFilterMetadata,
  HistoryEventQueryCriteria,
  PrimaryCategory,
} from '../domain/history'

export type FilterDimension =
  | 'periods'
  | 'regions'
  | 'figures'
  | 'primaryCategories'

interface SearchFiltersProps {
  metadata: HistoryEventFilterMetadata
  criteria: HistoryEventQueryCriteria
  onSearchTermChange: (value: string) => void
  onFilterToggle: (
    dimension: FilterDimension,
    value: string | PrimaryCategory,
  ) => void
  onClear: () => void
  headingId?: string
  className?: string
}

export function SearchFilters({
  metadata,
  criteria,
  onSearchTermChange,
  onFilterToggle,
  onClear,
  headingId,
  className = '',
}: SearchFiltersProps) {
  const generatedHeadingId = useId()
  const titleId = headingId ?? generatedHeadingId
  const selectedCount = countSelectedFilters(criteria)
  const hasActiveCriteria =
    Boolean(criteria.searchTerm?.trim()) || selectedCount > 0

  return (
    <section
      className={`search-filters ${className}`.trim()}
      aria-labelledby={titleId}
    >
      <div className="search-filters__header">
        <div>
          <span>探索范围</span>
          <h3 id={titleId}>搜索与筛选</h3>
        </div>
        <button
          type="button"
          className="search-filters__clear"
          onClick={onClear}
          disabled={!hasActiveCriteria}
        >
          清空全部
        </button>
      </div>

      <div className="search-filters__search" role="search">
        <label htmlFor={`${titleId}-search`}>关键词</label>
        <input
          id={`${titleId}-search`}
          type="search"
          value={criteria.searchTerm ?? ''}
          placeholder="搜索标题、叙述、地点、人物或主题"
          onChange={(event) => onSearchTermChange(event.currentTarget.value)}
        />
        <p>支持中文子串；拉丁字符不区分大小写。</p>
      </div>

      <FilterFieldset legend="主分类">
        <OptionList
          dimension="primaryCategories"
          options={metadata.primaryCategories}
          selected={criteria.filters?.primaryCategories ?? []}
          onToggle={onFilterToggle}
        />
      </FilterFieldset>

      <FilterFieldset legend="历史时期" className="search-filters__periods">
        {metadata.periodGroups.length > 0 ? (
          metadata.periodGroups.map((group) => (
            <div className="search-filters__period-group" key={group.context.id}>
              <strong>{formatEntityName(group.context)}</strong>
              <OptionList
                dimension="periods"
                options={group.periods.map(toEntityOption)}
                selected={criteria.filters?.periods ?? []}
                onToggle={onFilterToggle}
              />
            </div>
          ))
        ) : (
          <p className="search-filters__empty-options">暂无历史时期元数据</p>
        )}
      </FilterFieldset>

      <FilterFieldset legend="地区">
        <OptionList
          dimension="regions"
          options={metadata.regions.map(toEntityOption)}
          selected={criteria.filters?.regions ?? []}
          onToggle={onFilterToggle}
        />
      </FilterFieldset>

      <FilterFieldset legend="历史人物">
        <OptionList
          dimension="figures"
          options={metadata.figures.map(toEntityOption)}
          selected={criteria.filters?.figures ?? []}
          onToggle={onFilterToggle}
        />
      </FilterFieldset>

      <p className="search-filters__logic">
        同一维度内按“或”匹配，不同维度及关键词之间按“且”匹配。
      </p>
    </section>
  )
}

function FilterFieldset({
  legend,
  className = '',
  children,
}: {
  legend: string
  className?: string
  children: ReactNode
}) {
  return (
    <fieldset className={className}>
      <legend>{legend}</legend>
      {children}
    </fieldset>
  )
}

function OptionList({
  dimension,
  options,
  selected,
  onToggle,
}: {
  dimension: FilterDimension
  options: readonly FilterOption[] | readonly PrimaryCategory[]
  selected: readonly (string | PrimaryCategory)[]
  onToggle: SearchFiltersProps['onFilterToggle']
}) {
  if (options.length === 0) {
    return <p className="search-filters__empty-options">暂无选项</p>
  }

  return (
    <div className="search-filters__options">
      {options.map((option) => {
        const normalized = typeof option === 'string'
          ? { value: option, label: option }
          : option
        return <label key={normalized.value}>
          <input
            type="checkbox"
            checked={selected.includes(normalized.value)}
            onChange={() => onToggle(dimension, normalized.value)}
          />
          <span>{normalized.label}</span>
        </label>
      })}
    </div>
  )
}

interface FilterOption {
  value: string
  label: string
}

function toEntityOption(reference: CanonicalEntityReference): FilterOption {
  return { value: reference.id, label: formatEntityName(reference) }
}

function formatEntityName(reference: CanonicalEntityReference): string {
  return reference.disambiguationLabel
    ? `${reference.name}（${reference.disambiguationLabel}）`
    : reference.name
}

function countSelectedFilters(criteria: HistoryEventQueryCriteria): number {
  if (!criteria.filters) return 0

  return Object.values(criteria.filters).reduce(
    (count, values) => count + (values?.length ?? 0),
    0,
  )
}
