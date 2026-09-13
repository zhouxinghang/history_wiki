# Issue #20：公开查询高密度基线

## 可重复场景

- PostgreSQL：在隔离 schema 中生成 5,001 条同年 L3 已发布历史事件。
- Mock Repository：生成相同规模的确定性内存数据。
- 自动化入口：
  - `src/data/historyEventRepository.contract.test.ts`
  - `src/data/mockHistoryEventRepository.test.ts`
  - `server/internal/httpapi/public_events_contract_postgres_integration_test.go`

## 验收基线

| 场景 | 预期 |
| --- | --- |
| 显著度过滤后 5,000 条 | 返回完整 5,000 条，不分页、不截断 |
| 显著度过滤后 5,001 条 | `422 result_set_too_large` |
| 5,001 条均为 L3，当前尺度只返回 L1–L2 | 不报过量错误；`totalMatching=5001`、`events=[]` |
| Mock 5,000 + 5,001 两次查询 | Vitest 中合计小于 1,000 ms |

2026-09-13 本地 PostgreSQL 基线：5,000 条完整结果的 Store 查询约
31 ms。该查询在同一个只读、可重复读事务中计数，批量读取事件及关联，
并在 Go 中按固定中文规则排序。

## 搜索基础

`00012_index_published_event_search.sql` 为 `published_event_search.searchable_text`
建立 `pg_trgm` GIN 索引。查询仍使用搜索投影，并转义 `%`、`_`
和 `\`，保证关键词是字面子串而不是 SQL `LIKE` 模式。
