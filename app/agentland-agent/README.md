# Agentland Agent FastAPI demo

这个 demo 提供一个纯 HTTP 的 LangGraph coding agent 服务。它现在有两层
能力：

- 一个普通 agent 分支，用于持续对话
- 一个 Ralph 分支，用于执行多轮任务编排

统一入口会先经过一个 LangGraph graph router。router agent 会输出结构化
JSON，将请求分流到 chat 或 task 分支。

项目结构如下：

- `app/main.py`：应用入口
- `app/api/endpoints/`：HTTP 路由
- `app/schemas/`：请求模型
- `app/models/`：运行态模型
- `app/services/`：核心逻辑，包括 agent loop、router、memory、Ralph
- `app/database/`：数据库占位目录
- `tests/`：测试

## Start the service

按下面的步骤启动服务：

```bash
cd demo/agentland-agent
uv pip install -r requirements.txt
export OPENAI_API_KEY=your_key
uv run uvicorn app.main:app --host 0.0.0.0 --port 8000
```

## API overview

服务暴露以下接口：

- `POST /v1/chat/stream`：统一 SSE chat 接口，内部先走 graph router
- `POST /v1/ralph/stream`：直接启动 Ralph 风格的 10 轮外层编排
- `POST /v1/sessions/{session_id}/steer`：向活跃 chat 会话写入 steering
  消息
- `POST /v1/sessions/{session_id}/followup`：向活跃 chat 会话写入
  follow-up 消息
- `GET /health`：健康检查

## Unified chat interface

`POST /v1/chat/stream` 是整合后的入口。它先调用一个 LangGraph router
node。router 会返回 JSON 结果：

```json
{
  "intent": "chat",
  "reason": "plain conversation",
  "source": "model"
}
```

然后 graph 会按 `intent` 分支：

- `chat`：走普通 agent 服务
- `task`：走 Ralph loop 服务

返回仍然是 `text/event-stream`。这个接口会先发送一条 `route` 事件，然后
继续发送目标分支自己的 SSE 事件。

请求体示例：

```json
{
  "message": "请帮我实现一个需求，并修改工作区里的代码",
  "workspace_path": "/absolute/path/to/workspace",
  "session_id": "demo-session"
}
```

当它进入普通 chat 分支时，会先发送一条 `session` 事件，其中包含
`session_file`。这个文件使用和 `pi-mono` 相同原理的 append-only JSONL
memory。

## Persistent memory

普通 chat 分支不再依赖进程内 `history`。它使用一个持久化 session manager：

- 会话目录按 `cwd` 分桶
- 会话文件是 append-only JSONL
- 每条 entry 都有 `id` 和 `parentId`
- 当前上下文由 leaf 路径回放得到
- 支持 `compaction`、`branch_summary`、`custom`、`custom_message`

默认会话根目录是 `~/.pi/agent/sessions/`。测试时你也可以通过
`PI_SESSION_ROOT` 覆盖它。

## Ralph interface

`POST /v1/ralph/stream` 直接接收一个原始需求，并在现有 `run_agent`
之上启动 Ralph 风格的外层循环。

它和参考 `ralph` 保持同一原理：

- 外层循环默认最多运行 10 轮
- 每一轮都以全新的聊天上下文重新启动 agent
- 持久化状态落到工作区 `.ralph/<session_id>/prd.json` 和
  `.ralph/<session_id>/progress.txt`
- 只有当 agent 输出 `<promise>COMPLETE</promise>` 时才提前停止

请求体示例：

```json
{
  "requirement": "为当前项目增加一个 Ralph 风格的任务编排接口",
  "workspace_path": "/absolute/path/to/workspace",
  "session_id": "ralph-demo"
}
```

除了常规的 `assistant_delta`、`tool_call`、`tool_result` 事件以外，
Ralph 还会发送这些生命周期事件：

- `session`
- `planner_fallback`
- `plan_ready`
- `iteration_start`
- `iteration_complete`
- `done`

为了降低 OpenAI 兼容网关差异带来的影响，chat router、普通 agent 和
Ralph planner 都显式关闭了 `Responses API` 自动路由，优先使用更通用的
chat completions 路径。
