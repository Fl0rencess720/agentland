# Agentland Web API

前端默认使用 `/api/v1`，所有 JSON 响应采用统一外层结构：

```json
{
  "code": 200,
  "msg": "ok",
  "data": {}
}
```

登录后的请求携带 `Authorization: Bearer <access_token>`。服务端返回 `401` 时，前端调用 `POST /auth/refresh` 刷新令牌并重试一次。

## 认证

```text
POST /auth/github/start
POST /auth/github/callback
POST /auth/refresh
GET  /auth/me
POST /auth/logout
```

GitHub 登录发起时会把服务端返回的 state 暂存到 `sessionStorage`。回调使用 `/login?code=...&state=...`，前端以固定长度摘要比较回调 state 与暂存值，匹配后完成令牌交换并进入 `/projects`；state 在一次回调尝试后立即删除。

## 项目

```text
GET    /projects?keyword=
POST   /projects
GET    /projects/:projectId
DELETE /projects/:projectId
```

项目详情包含运行环境和正在执行的 Run：

```json
{
  "id": "project-1",
  "name": "Demo",
  "status": "DRAFT",
  "runtime_status": "active",
  "active_run_id": "run-1",
  "last_run_id": "run-0"
}
```

`runtime_status` 可取 `active`、`expired`、`unavailable`。`unavailable` 项目允许发送首条消息以创建运行环境；`expired` 项目进入只读状态。

## Agent Run

创建 Run：

```http
POST /projects/:projectId/runs
Authorization: Bearer <access_token>
Idempotency-Key: <uuid>
Content-Type: application/json

{"message":"创建一个待办应用"}
```

服务端返回 `202`：

```json
{
  "code": 202,
  "msg": "accepted",
  "data": {
    "run_id": "run-1",
    "user_message_id": "message-1",
    "status": "queued"
  }
}
```

```text
GET  /runs/:runId
POST /runs/:runId/cancel
GET  /projects/:projectId/messages?cursor=
```

Run 详情包含 `input_message_id`、`assistant_message_id`、`error_code`、`error_message`、`last_sequence` 和各阶段时间。

## SSE 事件

```http
GET /runs/:runId/events
Accept: text/event-stream
Authorization: Bearer <access_token>
Last-Event-ID: <stream-entry-id>
```

SSE 直接返回事件流，不使用 JSON 外层结构。前端处理以下事件：

```text
run.started
message.delta
tool.started
tool.output
tool.completed
run.completed
run.failed
run.cancelled
ping
```

每条事件的 `data` 格式如下：

```json
{
  "type": "message.delta",
  "run_id": "run-1",
  "conversation_id": "conversation-1",
  "sequence": 8,
  "timestamp": "2026-08-02T10:00:00Z",
  "payload": {"delta":"完成"}
}
```

前端首次打开活动 Run 时从头读取事件以恢复工具状态；同一连接断线重连时携带最近的 `Last-Event-ID`。

## 文件

```text
GET /projects/:projectId/files/tree?path=.
GET /projects/:projectId/files/content?path=src/App.tsx
PUT /projects/:projectId/files/content?path=src/App.tsx
```

文件写入请求携带当前 SHA：

```json
{"content":"...","sha":"sha-1"}
```

服务端返回新的 `path`、`size` 和 `sha`。服务端文件已变化时返回 `409 FILE_CONFLICT`，前端让用户加载服务端版本或使用最新 SHA 明确覆盖。

## 预览

```text
POST /projects/:projectId/previews  body: {"port":3000}
GET  /projects/:projectId/preview
```

预览状态可取 `idle`、`starting`、`running`、`failed`、`expired`。状态为 `running` 时返回绝对预览地址：

```json
{
  "status": "running",
  "port": 3000,
  "preview_url": "http://preview-token.localhost:18081/p/preview-token/"
}
```

`preview_url` 必须使用与 Agentland 页面不同的 HTTP(S) 来源。前端校验通过后在受限 `iframe` 中显示，并启用 `allow-same-origin` 供生成应用使用 Web Storage；同源、相对或格式错误的地址会显示安全错误。
