# Gateway API 文档

## 接口清单

| 分组 | 方法 | 路径 |
| --- | --- | --- |
| code-runner | `POST` | `/api/code-runner/sandboxes` |
| code-runner | `POST` | `/api/code-runner/contexts` |
| code-runner | `POST` | `/api/code-runner/contexts/{contextId}/execute` |
| code-runner | `DELETE` | `/api/code-runner/contexts/{contextId}` |
| code-runner | `GET` | `/api/code-runner/fs/tree` |
| code-runner | `GET` | `/api/code-runner/fs/file` |
| code-runner | `POST` | `/api/code-runner/fs/file` |
| code-runner | `POST` | `/api/code-runner/fs/upload` |
| code-runner | `GET` | `/api/code-runner/fs/download` |
| agent-sessions | `POST` | `/api/agent-sessions/invocations/*path` |
| agent-sessions | `GET` | `/api/agent-sessions/invocations/*path` |
| agent-sessions | `ANY` | `/api/agent-sessions/{sessionId}/endpoints/by-port/{port}[/*path]` |

## 公共约定

本节描述所有接口共享的 Header 和响应行为。

### 公共请求 Header

| Header | 必填 | 说明 |
| --- | --- | --- |
| `Content-Type` | 按接口要求 | JSON 接口使用 `application/json`；上传接口必须 `multipart/form-data`。 |
| `x-agentland-request-id` | 否 | 请求链路 ID。可传，不传则由网关生成。 |
| `x-agentland-session` | 部分接口必填 | 会话 ID。`code-runner` 除创建沙箱外都必填。`agent-sessions/invocations` 可不传。 |
| `x-agentland-runtime` | 否 | 仅 `agent-sessions/invocations` 创建会话时使用。 |
| `x-agentland-runtime-namespace` | 否 | 仅 `agent-sessions/invocations` 创建会话时使用。 |

### 公共响应 Header

| Header | 说明 |
| --- | --- |
| `x-agentland-request-id` | 网关始终返回。用于日志与链路追踪。 |
| `x-agentland-session` | 与会话相关接口会返回（包括透传场景）。 |

### 统一错误体（网关本地错误）

网关在参数校验失败或内部异常时，会返回固定 JSON 格式。

```json
{
  "code": 1,
  "msg": "Form Error"
}
```

```json
{
  "code": 0,
  "msg": "Server Error"
}
```

说明：

- `code=1` 对应参数错误，HTTP 状态码 `400`。
- `code=0` 对应网关内部错误，HTTP 状态码 `500`。
- 会话不存在时，部分接口返回 `404` 与 `{"error":"session not found"}`。
- 代理链路不可达时，返回 `502` 与纯文本 `sandbox unreachable`。

## code-runner 接口

本组接口用于代码执行与文件系统访问。除创建沙箱外，必须传
`x-agentland-session`。

### 1. 创建沙箱

该接口创建一个新的代码沙箱会话，返回 `sandbox_id`。后续所有
`code-runner` 调用都使用这个会话 ID。

- 方法与路径：`POST /api/code-runner/sandboxes`
- 必填 Header：`Content-Type: application/json`

请求体：

```json
{
  "language": "python"
}
```

字段说明：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `language` | string | 是 | 仅支持 `python`、`shell`（大小写不敏感）。 |

成功响应（HTTP 200）：

```json
{
  "msg": "success",
  "code": 200,
  "data": {
    "sandbox_id": "session-sbx-1"
  }
}
```

### 2. 创建执行上下文

该接口在指定沙箱内创建可复用执行上下文，适合多轮执行保留状态。

- 方法与路径：`POST /api/code-runner/contexts`
- 必填 Header：`Content-Type: application/json`、`x-agentland-session`

请求体：

```json
{
  "language": "python",
  "cwd": "/workspace"
}
```

字段说明：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `language` | string | 是 | 仅支持 `python`、`shell`。 |
| `cwd` | string | 否 | 工作目录。空值默认 `/workspace`。 |

成功响应（HTTP 200）：

```json
{
  "msg": "success",
  "code": 200,
  "data": {
    "context_id": "ctx-1",
    "language": "python",
    "cwd": "/workspace",
    "state": "ready",
    "created_at": "2026-02-17T08:30:00Z"
  }
}
```

### 3. 在上下文中执行代码

该接口在已存在的 `context_id` 内执行代码。

- 方法与路径：`POST /api/code-runner/contexts/{contextId}/execute`
- 必填 Header：`Content-Type: application/json`、`x-agentland-session`

路径参数：

| 参数 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `contextId` | string | 是 | 上下文 ID。 |

请求体：

```json
{
  "code": "print(1)",
  "timeout_ms": 30000
}
```

字段说明：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `code` | string | 是 | 要执行的代码。 |
| `timeout_ms` | int | 否 | 执行超时，范围 `100` 到 `300000`。默认 `30000`。 |

成功响应（HTTP 200）：

```json
{
  "msg": "success",
  "code": 200,
  "data": {
    "context_id": "ctx-1",
    "execution_count": 1,
    "exit_code": 0,
    "stdout": "1\n",
    "stderr": "",
    "duration_ms": 5
  }
}
```

### 4. 删除执行上下文

该接口销毁指定上下文。

- 方法与路径：`DELETE /api/code-runner/contexts/{contextId}`
- 必填 Header：`x-agentland-session`

路径参数：

| 参数 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `contextId` | string | 是 | 上下文 ID。 |

成功响应（HTTP 200）：

```json
{
  "msg": "success",
  "code": 200,
  "data": {
    "context_id": "ctx-1"
  }
}
```

### 5. 获取目录树

该接口返回目录树结构，支持深度和隐藏文件控制。

- 方法与路径：`GET /api/code-runner/fs/tree`
- 必填 Header：`x-agentland-session`

查询参数：

| 参数 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `path` | string | 否 | 目录路径。默认 `.`。 |
| `depth` | int | 否 | 目录深度，范围 `1` 到 `20`，默认 `5`。 |
| `includeHidden` | bool | 否 | 是否包含隐藏文件，默认 `false`。 |

成功响应（HTTP 200）：

```json
{
  "msg": "success",
  "code": 200,
  "data": {
    "root": "/workspace",
    "nodes": [
      {
        "path": "a.txt",
        "name": "a.txt",
        "type": "file",
        "size": 3,
        "modTime": "2026-02-17T08:30:00Z"
      }
    ]
  }
}
```

### 6. 读取文件

该接口读取文件内容，支持 `utf8` 和 `base64` 两种返回编码。

- 方法与路径：`GET /api/code-runner/fs/file`
- 必填 Header：`x-agentland-session`

查询参数：

| 参数 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `path` | string | 是 | 文件路径。 |
| `encoding` | string | 否 | `utf8`、`utf-8`、`base64`。默认 `utf8`。 |

成功响应（HTTP 200）：

```json
{
  "msg": "success",
  "code": 200,
  "data": {
    "path": "/workspace/a.txt",
    "size": 3,
    "encoding": "utf8",
    "content": "abc"
  }
}
```

### 7. 写文件

该接口写入文件内容。不存在的父目录会自动创建。

- 方法与路径：`POST /api/code-runner/fs/file`
- 必填 Header：`Content-Type: application/json`、`x-agentland-session`

请求体：

```json
{
  "path": "/workspace/data.txt",
  "content": "line1\nline2",
  "encoding": "utf-8"
}
```

字段说明：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `path` | string | 是 | 目标文件路径。 |
| `content` | string | 是 | 文件内容。 |
| `encoding` | string | 否 | `utf8`、`utf-8`、`base64`。默认 `utf8`。 |

成功响应（HTTP 200）：

```json
{
  "msg": "success",
  "code": 200,
  "data": {
    "path": "/workspace/data.txt",
    "size": 11,
    "encoding": "utf8"
  }
}
```

### 8. 上传文件

该接口通过 `multipart/form-data` 上传文件到沙箱路径。当前实现不支持
JSON 上传格式。

- 方法与路径：`POST /api/code-runner/fs/upload`
- 必填 Header：`Content-Type: multipart/form-data`、`x-agentland-session`

表单字段：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `file` | file | 是 | 上传文件本体。 |
| `target_file_path` | string | 是 | 沙箱内目标路径。 |

`curl` 示例：

```bash
curl -X POST "$BASE/api/code-runner/fs/upload" \
  -H "x-agentland-session: $SESSION_ID" \
  -F "file=@./dataset.csv" \
  -F "target_file_path=/workspace/dataset.csv"
```

成功响应（HTTP 200）：

```json
{
  "msg": "success",
  "code": 200,
  "data": {
    "source_path": "dataset.csv",
    "target_path": "/workspace/dataset.csv",
    "size": 123
  }
}
```

### 9. 下载文件

该接口返回二进制文件流，不是 JSON 包裹格式。

- 方法与路径：`GET /api/code-runner/fs/download`
- 必填 Header：`x-agentland-session`

查询参数：

| 参数 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `path` | string | 是 | 要下载的源文件路径。 |

成功响应：

- HTTP 状态码：`200`
- 响应体：`application/octet-stream` 二进制流
- 关键响应 Header：  
  - `Content-Disposition: attachment; filename="xxx"`  
  - `X-Agentland-File-Path: /workspace/xxx`

## agent-sessions 接口

本组接口用于通用 Agent 转发。网关会维护会话，并把请求透传到对应沙箱。

### 1. 调用 Agent 入口（POST/GET）

该接口转发到沙箱业务入口路径。路径、查询参数、响应体都保持透传。

- 方法与路径：`POST /api/agent-sessions/invocations/*path`
- 方法与路径：`GET /api/agent-sessions/invocations/*path`

请求说明：

| 项目 | 说明 |
| --- | --- |
| `*path` | 要转发到业务容器的路径，例如 `/chat`、`/v1/messages`。 |
| 查询参数 | 原样透传。 |
| 请求体 | 请求体字节透传。当前网关转发时会写 `Content-Type: application/json`，建议仅传 JSON。 |
| `x-agentland-session` | 可选。传且有效则复用会话，不传或无效则自动新建。 |

会话创建时的运行时选择优先级：

1. Header `x-agentland-runtime` / `x-agentland-runtime-namespace`
2. Query `runtime` / `runtime_namespace`
3. 网关默认值（默认 `default-runtime`、`agentland-sandboxes`）

成功响应：

- HTTP 状态码：由上游业务返回决定
- 响应体：上游业务响应原样透传
- 响应 Header：包含 `x-agentland-session`

失败响应：

- 新建会话失败：`500`，`{"code":0,"msg":"Server Error"}`
- 代理失败：`502`，`sandbox unreachable`

### 2. 按端口透传（ANY）

该接口把请求转发到沙箱内 `127.0.0.1:{port}`，适合转发 Web 应用或
自定义 HTTP 服务。

- 方法与路径：`ANY /api/agent-sessions/{sessionId}/endpoints/by-port/{port}`
- 方法与路径：`ANY /api/agent-sessions/{sessionId}/endpoints/by-port/{port}/*path`

路径参数：

| 参数 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `sessionId` | string | 是 | 已存在的会话 ID。 |
| `port` | string | 是 | 沙箱内目标端口。 |
| `*path` | string | 否 | 目标子路径，省略时为 `/`。 |

查询参数：

| 参数 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `scheme` | string | 否 | `http` 或 `https`，默认 `http`。 |
| 其他参数 | string | 否 | 原样透传到上游。 |

请求体说明：

- 请求体会透传到上游端口服务。
- 当前网关转发时会写 `Content-Type: application/json`，建议按 JSON 接口使用。

成功响应：

- HTTP 状态码：由上游业务返回决定
- 响应体：上游业务响应原样透传

失败响应：

- 会话不存在：`404`，`{"error":"session not found"}`
- 缺少关键路径参数：`400`，`{"error":"port and sessionId are required"}`
- 代理失败：`502`，`sandbox unreachable`

## 前端接入建议

本节给出与实现一致的落地建议，避免常见对接问题。

1. 先调用 `POST /api/code-runner/sandboxes` 获取 `sandbox_id`，并在后续
   `code-runner` 请求头统一携带 `x-agentland-session`。
2. 文件上传必须使用 `multipart/form-data`，不要用 JSON。
3. 文件下载按二进制处理，不要按 JSON 解析响应体。
4. `agent-sessions/invocations` 透传上游响应，前端需要按业务服务自己的
   协议处理状态码和响应体。
