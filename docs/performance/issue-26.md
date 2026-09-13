# Issue #26：10 万历史事件 PostgreSQL 验证

## 可复现命令

此命令会清空目标数据库，且拒绝数据库名不含 `test` 或 `perf` 的连接串：

```bash
cd server
go run ./cmd/perfverify \
  -database-url "$PERFORMANCE_DATABASE_URL" \
  -reset-dedicated-database \
  -samples 20 \
  -output ../docs/performance/issue-26-results.json
```

夹具通过 PostgreSQL `generate_series` 创建恰好 100,000 条已发布历史事件，不由迁移加载，也不进入生产镜像或前端生产 bundle。验证包含：

- 宽范围查询（自动使用 L1，返回不超过 5,000 条）；
- 窄范围高密度查询；
- 中文子串搜索；
- 历史时期、地区、历史人物和主分类的多维筛选；
- 详情和元数据；
- 超过 5,000 条时返回 `422 result_set_too_large`；
- 在 1 秒内发起 100 个详情请求的单实例峰值检查。

公开查询 p95 阈值为 200 ms，详情及元数据 p95 阈值为 100 ms。JSON 原始结果记录 PostgreSQL 版本、连接池上限、样本数、p95、状态码和总体结论。

## 2026-09-13 结果

在本机 PostgreSQL 17.11、连接池上限 20、每场景 3 次预热加 20 个样本下，先验证迁移后的生产表为空，再生成夹具；总体 `passed: true`：宽范围 66.56 ms、窄范围高密度 56.47 ms、中文子串 91.24 ms、多维筛选 133.38 ms、详情 1.04 ms、元数据 69.72 ms、真实认证管理写 1.25 ms（均为 p95）。1 秒内发起 100 个详情请求时错误数为 0、p95 2.79 ms；10,040 条候选明确返回 `422 result_set_too_large`。完整机器可读数据见 [`issue-26-results.json`](issue-26-results.json)。
