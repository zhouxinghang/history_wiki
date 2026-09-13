# 历史 Wiki

## 本地开发

启动 PostgreSQL、执行空库迁移并启动 Go API：

```bash
docker compose up --build
```

API 默认监听 `http://localhost:8080`。存活检查为 `/health/live`，数据库就绪检查为 `/health/ready`。

数据库迁移完成后，通过一次性交互式命令创建首位管理员。命令不会创建默认账号或默认密码，且系统已有任意账号时会拒绝再次初始化：

```bash
docker compose run --rm adminctl
```

另开终端启动读者端；Vite 会将 `/api` 和 `/health` 代理到 Go 服务：

```bash
npm install
npm run dev
```

不使用 Docker 运行 Go 服务时，可复制 `.env.example` 的配置，并在 `server/` 目录执行：

```bash
go run ./cmd/migrate
go run ./cmd/adminctl create-admin
go run ./cmd/api
```

`PUBLIC_BASE_URL` 必须与管理区页面的来源一致；开发环境默认使用 `http://localhost:5173`。管理区入口为 `/admin`，认证 API 使用服务端 Session、Secure Cookie 和 CSRF Token。

公开及认证 API 契约位于 `server/openapi/openapi.yaml`。真实 PostgreSQL 集成测试需要提供专用数据库连接：

```bash
cd server
TEST_DATABASE_URL='postgres://user:password@localhost:5432/postgres?sslmode=disable' go test ./...
```

## 契约与生产交付

`npm run openapi:generate` 从 OpenAPI 3.1 契约生成 Go 边界类型和 TypeScript 类型；`npm run openapi:check` 用于 CI 检查生成文件无差异。生产镜像、独立迁移、Secret、监控与首次发布步骤见 [`docs/operations/production.md`](docs/operations/production.md)，首版数据持久性和单实例风险见 [`docs/releases/v0.1.0.md`](docs/releases/v0.1.0.md)。
