# level1 数据录入产物

本目录记录第一批地标事件（`history_level1.json`，67 条）录入线上环境的地区清单、映射与操作脚本，可重复执行。

## 数据文件

| 文件 | 说明 |
|---|---|
| `level1-regions.json` | 地区词汇表（30 个），以及事件 slug → 地区名称的映射；`group` 仅为人工浏览分组 |
| `level1-region-ids.json` | 地区名称/key → 线上地区 UUID，供事件 `regionIds` 使用 |
| `history_level1.import.json` | 参考文件：已填好 `regionIds` 的事件 JSON（因事件已存在，不能用于重新导入） |
| `event-regions-applied.json` | 补齐 `regionIds` 的单次执行明细 |
| `events-publish-result.json` | 发布事件的单次执行明细 |

## 脚本

三个脚本都通过管理接口操作，需要同一份凭据，且可安全重复运行。

```bash
export HW_BASE=http://homelab.com:9003
export HW_SESSION_COOKIE=<管理区 session cookie 值>
export HW_CSRF_TOKEN=<管理区 csrf cookie 值>

node data/import-regions.mjs       # 1. 创建地区，幂等键 level1-regions/<key>
node data/apply-event-regions.mjs  # 2. 读取现有草稿，仅替换 regionIds
node data/publish-events.mjs       # 3. 发布已有草稿的事件（已发布自动跳过）
```

- 也可用 `HW_EMAIL` / `HW_PASSWORD` 登录代替会话 Cookie。
- `apply-event-regions.mjs` 会保留正文、时间及已有的 places/periods/figures/topicTags，不覆盖人工修改。
- 三个脚本都会在响应 `429` 时按 `Retry-After` 等待后重试（管理写入限流 60 次/分钟）。

## 地区粒度约定

宏观地理区与文明/政权/次区域并列为平级地区（例如「古埃及」与「北非」并存、「两河流域」与「西亚」并存），单事件关联 1–3 个。地区在数据模型中没有层级，`group` 仅供查阅。
