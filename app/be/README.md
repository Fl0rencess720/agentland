# Agentland 应用后端

应用后端保存项目、消息和 Run，使用 Redis Stream 保存运行事件，并经 Sandbox Gateway 访问 `agentd` 、工作区和预览服务。

## 本地运行

启动 PostgreSQL、Redis 和 Sandbox Gateway 后运行：

```bash
export DATABASE_URL='postgres://agentland:agentland@127.0.0.1:5432/agentland?sslmode=disable'
export REDIS_ADDR='127.0.0.1:6379'
export AGENTLAND_GATEWAY_URL='http://127.0.0.1:18080'
go run ./app/be/cmd
```

服务默认监听 `:18081`。预览公开来源模板 `PREVIEW_PUBLIC_URL_TEMPLATE` 默认为 `http://{token}.localhost:18081`，后端会返回 `http://<token>.localhost:18081/p/<token>/`。模板只接受 HTTP(S) 来源，必须在主机名中包含 `{token}`；生产环境应配置通配 DNS 和 TLS，例如 `https://{token}.preview.example.com`，反向代理需保留原始 `Host`。

启动时会按外键顺序创建表，并检查 PostgreSQL 和 Redis 连接。GitHub 登录还需配置 `AUTH_GITHUB_CLIENT_ID`、`AUTH_GITHUB_CLIENT_SECRET`、`AUTH_JWT_PRIVATE_KEY_PATH` 和 `AUTH_JWT_PUBLIC_KEY_PATH`。生产环境应设置 `AUTH_OAUTH_COOKIE_SECURE=true`，OAuth nonce Cookie 使用 `HttpOnly` 与 `SameSite=Lax`，有效期与 OAuth state 一致。

前端与后端使用不同来源时，用 `SERVER_HTTP_CORS_ALLOWED_ORIGINS` 配置允许携带凭据的前端来源，多个来源以逗号分隔，例如 `https://app.example.com,http://localhost:3000`。每项必须写完整的协议、主机和可选端口，且不带路径；后端只为列表中的 `Origin` 回显跨来源响应头，不接受 `*`。同源部署无需配置该变量。

服务默认不信任代理转发的客户端 IP。部署在反向代理后时，用 `SERVER_HTTP_TRUSTED_PROXIES` 配置明确的代理 IP 或 CIDR；例如 `10.0.0.0/8,192.0.2.10`。限流器会按 `RATE_LIMIT_VISITOR_TTL` 清理长期无请求的 IP 记录。预览资源使用独立限流器，默认每个 IP 每秒 100 次请求、突发 500 次，可用 `RATE_LIMIT_PREVIEW_REQUESTS_PER_SECOND` 和 `RATE_LIMIT_PREVIEW_BURST` 调整。

Run 消息最多 256 KiB，工作区文件内容最多 1 MiB，其余 JSON 请求使用较小的请求体上限，超限返回 `413 REQUEST_TOO_LARGE`。活跃 Run 的 Redis Stream 保留完整事件；Run 进入终态后，事件流保留 24 小时。

`RUNTIME_IDLE_TIMEOUT` 控制空闲期限，默认 15 分钟；`RUNTIME_MAX_SESSION_DURATION` 控制 Sandbox Session 的绝对期限，默认 1 小时。文件、预览和活跃 Run 只更新最后活动时间，不延长绝对期限。

OpenTelemetry 默认关闭；设置 `OTEL_ENABLED=true` 和 `OTEL_ENDPOINT=127.0.0.1:4317` 后会导出 HTTP、Run Worker 与 Gateway 请求链路。创建 Run 时的 W3C Trace Context 会写入 PostgreSQL，后台 Worker 领取任务后继续同一条 Trace。

## 验证

```bash
go test ./app/be/...
go vet ./app/be/...
```

PostgreSQL 和 Redis 集成测试使用独立测试实例：

```bash
TEST_DATABASE_URL='postgres://agentland:agentland@127.0.0.1:5432/agentland_test?sslmode=disable' \
TEST_REDIS_ADDR='127.0.0.1:6379' \
go test -count=1 ./app/be/...
```
