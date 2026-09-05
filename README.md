# Centipede

Centipede（千足虫）是一个可持续增加功能的模块化单体应用。它作为一个进程部署，但每个业务功能保持清晰边界；AllmachtTool 独立承担登录、SSO 和身份管理。

## 当前骨架

- Go 1.25+
- Gin HTTP API
- PostgreSQL + pgx
- SQL 版本迁移
- Allmacht 风格的 Zap 结构化日志、控制台输出、JSON 文件和滚动切分
- 可配置的启动 Banner
- 优雅关闭、存活检查和数据库就绪检查
- 本地注册/登录、短期 JWT Access Token 和 HttpOnly Refresh Cookie
- 可替换的认证服务边界，预留 Allmacht Authorization Code + PKCE 适配器
- 面向外文阅读的多语言词汇、来源和多条上下文 API
- Vue 3 + Vite + TypeScript 阅读型工作台

## 架构

```text
cmd/                         进程入口
internal/app/                依赖组合根
internal/modules/            业务模块
internal/platform/           配置、数据库、HTTP、迁移等基础设施
migrations/                  Centipede 独占的 PostgreSQL 迁移
api/                         OpenAPI 契约
frontend/                    Vue 3 + Vite + TypeScript 前端应用
```

每个复杂业务模块按需采用：

```text
internal/modules/<module>/
  domain/
  application/
  adapter/in/http/
  adapter/out/postgres/
```

简单查询模块不强制建立空的分层目录。

## 本地运行

配置采用与 AllmachtTool 一致的 bootstrap + profile 文件结构：`.env` 只选择环境、配置源和 Nacos 连接信息；业务配置位于 `config/application-<env>.yaml`。复制 bootstrap 配置并启动数据库：

```powershell
Copy-Item .env.example .env
docker compose up -d db
go run ./cmd/migrate
go run ./cmd/api
```

API 启动时会打印 Banner，控制台输出使用结构化 Zap 日志；文件日志写入 `logs/`，支持按天和按大小滚动。迁移命令复用同一套 logger，但默认只输出到控制台。

默认从 `config/application-dev.yaml` 读取。要使用 Nacos，把 `.env` 改为：

```dotenv
APP_ENV=dev
CONFIG_SOURCE=nacos
NACOS_SERVER_ADDRS=http://127.0.0.1:8848
NACOS_DATA_ID=centipede-dev.yaml
NACOS_GROUP=DEFAULT_GROUP
```

Nacos DataID 内容使用与本地 YAML 文件相同的结构。API 启动时加载配置，并在 Nacos 模式下监听后续变更；服务器地址、数据库连接和认证密钥等需要重启才能生效的设置会拒绝热更新。可通过 `DOTENV_PATH` 指定其他 bootstrap 文件，通过 `CONFIG_DIR` 指定本地配置目录。

检查服务：

```powershell
Invoke-RestMethod http://localhost:7788/health/live
Invoke-RestMethod http://localhost:7788/health/ready
```

## Docmost 渐进式迁移

Centipede 支持作为 Docmost 的迁移入口运行。将 `migration.legacy_base_url` 配置为现有 Node 服务地址后，Centipede 自己已经实现的路由优先处理，其余未迁移的 HTTP、Socket.IO 和 WebSocket 请求会透明转发到旧服务：

```yaml
migration:
  legacy_base_url: "http://localhost:3000"
```

迁移期间，Docmost 前端将 `API_BASE_URL` 指向 Centipede 的 `/api` 地址即可。每迁移一个领域，再把对应路由注册到 Centipede，旧服务会自动退居为该路由的 fallback；不需要一次性切换全部接口。

当前网关不访问 Allmacht 数据库，也不改变 Docmost 数据表。协同编辑和文档转换等尚未迁移的能力继续由旧 Node 服务提供。

### 独立启动 Docmost 前端

Docmost 的 React 前端位于 `C:\\Users\\1\\Desktop\\projects\\docmost\\docmost\\apps\\client`，可以不依赖 Node 静态服务单独启动。先启动当前 Node 服务（供尚未迁移的接口和协同能力使用），再启动 Centipede 网关：

```powershell
# 终端 1：Docmost Node 旧服务
Set-Location C:\\Users\\1\\Desktop\\projects\\docmost\\docmost
corepack pnpm server:dev

# 终端 2：Centipede Go 网关
Set-Location C:\\Users\\1\\Desktop\\projects\\Centipede
# 将 config/application-dev.yaml 的 migration.legacy_base_url 设为 http://localhost:3000
go run ./cmd/api

# 终端 3：独立 React 前端
Set-Location C:\\Users\\1\\Desktop\\projects\\docmost\\docmost
$env:API_BASE_URL="http://localhost:7788/api"
$env:REALTIME_URL="http://localhost:7788"
$env:COLLAB_URL="http://localhost:7788"
corepack pnpm client:dev
```

浏览器访问 `http://localhost:5173`。生产环境可使用根目录的 `Dockerfile.client` 构建 Nginx 静态前端；`API_BASE_URL`、`REALTIME_URL` 和 `COLLAB_URL` 可在容器启动时注入。

## 身份边界

Centipede 不保存 Allmacht 密码，也不访问 Allmacht 数据库。当前本地账号使用 `identity_issuer=local`；未来 SSO 完成后，以 `(identity_issuer, identity_subject)` 映射本地 `app_users`，业务表只关联本地用户 ID。认证用例依赖本地用户、会话和 Token provider 接口，业务模块不依赖具体认证方式。

## 第一阶段 API

- `POST /api/v1/auth/register`、`/login`：返回短期 JWT；Refresh Token 仅通过 HttpOnly Cookie 下发
- `POST /api/v1/auth/refresh`：校验数据库中的 Refresh Token 哈希并轮换会话
- `POST /api/v1/auth/logout`、`GET /api/v1/auth/me`
- `POST /api/v1/vocabulary/entries`：创建词/短语，可带语言、词元、释义、备注、书籍/章节/位置和多个上下文
- `GET /api/v1/vocabulary/entries`：按语言、状态、关键词分页查询
- `GET /api/v1/vocabulary/entries/{id}`、`PATCH /api/v1/vocabulary/entries/{id}/status`

完整契约见 [`api/openapi.yaml`](api/openapi.yaml)。

## 数据库迁移

迁移只操作 Centipede 自己的表，按文件名顺序执行：

```powershell
Copy-Item .env.example .env
go run ./cmd/migrate
```

`000003` 增加本地账号字段，`000004` 创建词汇条目和上下文表。生产环境必须在实际配置文件或 Nacos 配置中设置至少 32 个字符的 `auth.jwt_secret`，并启用 `auth.cookie_secure=true`；`config/application-production.yaml` 中的 `CHANGE_ME` 不能直接启动。

## 前端

```powershell
Set-Location frontend
corepack pnpm install
corepack pnpm dev
```

前端开发服务器会把 `/api` 代理到 `http://localhost:7788`；检查和生产构建使用 `corepack pnpm typecheck` 与 `corepack pnpm build`。

## 常用命令

```powershell
go test ./...
go vet ./...
go build ./cmd/api
go build ./cmd/migrate
```
