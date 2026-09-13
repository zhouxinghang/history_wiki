# 在单仓库内建设 Go 与 PostgreSQL 服务

服务端放在 `server/` 下作为独立 Go Module，与前端同仓库、同域部署，使用 `net/http`、Chi、pgx、sqlc 和 PostgreSQL。相比沿用前端 TypeScript 或拆分仓库，这一选择接受跨语言构建成本，以换取清晰的服务边界、关系数据约束和对复杂时间范围查询 SQL 的直接控制。
