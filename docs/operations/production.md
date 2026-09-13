# 单实例生产运行手册

## 构建与配置

```bash
docker build -f server/Dockerfile -t registry.example/history-wiki:0.1.0 .
```

镜像以非 root 用户运行，由同一 Go 进程提供 `/api` 与生产 SPA。镜像包含三个互不隐式调用的命令：`/app/migrate`、`/app/adminctl` 和 `/app/api`。API 进程不会执行迁移。

生产必须从部署系统注入以下配置，真实值不得写入镜像、Compose 文件或版本库：

- `DATABASE_URL` 或只读 Secret 文件路径 `DATABASE_URL_FILE`（二选一）；
- `PUBLIC_BASE_URL`；
- `SESSION_COOKIE_NAME`、`CSRF_COOKIE_NAME`；
- `TRUSTED_PROXY_COUNT`（只填写实际位于应用前方且受信任的代理层数）；
- `LOG_LEVEL`；
- `DATABASE_MAX_CONNS`、`DATABASE_MIN_CONNS`、连接生命周期；
- `SESSION_IDLE_TIMEOUT`、`SESSION_MAX_LIFETIME`；
- `ARGON2_MEMORY_KIB`、`ARGON2_ITERATIONS`、`ARGON2_PARALLELISM`。

`compose.production.yaml` 是 Secret 注入示例，不包含默认数据库口令。创建 `postgres_password` 和完整 PostgreSQL 连接串 `database_url` 两个外部 Secret 后再部署。根目录 `compose.yaml` 仅用于本机开发，其固定口令不得用于生产。

## 发布顺序

1. 准备一个空 PostgreSQL 17 数据库及持久卷。迁移不插入演示或性能数据。
2. 使用待发布镜像执行一次 `/app/migrate`；迁移失败时不要启动新 API。
3. 首次部署在交互终端运行 `/app/adminctl create-admin`，账号或密码没有默认值。
4. 启动一个 `/app/api` 实例；确认 `GET /health/live` 为 200，`GET /health/ready` 为 200 且迁移版本正确。
5. 通过只允许监控系统访问的内部网络采集 `GET /metrics`。指标包含 HTTP 数量/耗时/并发、PostgreSQL 连接池与查询耗时、发布及导入结果；标签不包含账号、历史事件或查询字符串。
6. 由管理员创建规范实体、录入首条历史事件、发布，并验证公开查询与详情。

回滚应用镜像前必须确认旧镜像支持当前数据库版本。本项目不自动回滚数据库迁移，也不在 API 启动时改变结构。

## 安全边界

- TLS 在受信任反向代理终止；Session/CSRF Cookie 使用 `Secure`、`SameSite=Lax` 和根路径。
- 仅当代理覆盖并重写 `X-Forwarded-For` 时配置 `TRUSTED_PROXY_COUNT`；否则保持 0。
- 登录失败每 IP 10 次/15 分钟；公开查询每 IP 120 次/分钟；普通管理写入每用户 60 次/分钟；正式导入每管理员 5 次/小时。
- 普通 JSON 请求体最多 2 MiB，导入预检查和正式导入最多 20 MiB/10,000 条。
- `/metrics` 没有应用层认证，必须由网络策略限制为监控系统可访问。

## 已接受的高风险

首版**没有数据库备份、恢复流程或时间点恢复（PITR）**，也**没有高可用或自动故障转移**。应用或数据库故障会导致停机；数据库实例或持久卷损坏可能永久丢失全部历史事件、活动草稿、事件版本、规范实体、账号、Session 与审计记录。不可变事件版本只防止内容被修改，不能代替备份。录入不可替代内容前必须重新评审 ADR-0009 并建设备份与恢复演练。
