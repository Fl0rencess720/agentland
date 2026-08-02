# agentd 实现说明

`agentd` 是 Agentland 自带的应用层 Agent。它是一个普通 Go HTTP 服务，可以运行在任意 Linux 容器中。Agentland 使用 `AgentRuntime` 将它放入 `AgentSession` Sandbox，但 `agentd` 本身不读取 Kubernetes 资源，也不调用 AgentCore 或 CodeInterpreter。

## 运行结构

```text
Gateway
  -> AgentSession Sandbox
       -> agentd :1883
            -> Eino ChatModel
            -> 本地工具
            -> MCP Client
            -> Skills
            -> /workspace/.agentland Memory
```

Gateway 使用现有 `/api/agent-sessions/invocations/*path` 透明代理请求，并向 Sandbox 注入 JWT。`agentd` 复用相同的 JWT 校验方式。开发环境可以设置 `AL_AGENTD_AUTH_ENABLED=false` 关闭校验。

## Agent 循环

Eino 的 `ChatModelAgent` 默认带有 `MaxIterations`。本项目需要让 Agent 持续工作到模型主动结束，因此 `pkg/agentd/agent.go` 使用 Eino 的 `ToolCallingChatModel`、`schema.Message` 和 `tool.InvokableTool` 实现了一个小型循环：

```text
读取历史
  -> 调用模型并流式输出
  -> 模型返回 ToolCall
  -> 顺序执行工具并写入 ToolMessage
  -> 再次调用模型
  -> 模型不再调用工具时结束
```

循环不限制模型步骤、工具调用次数或总运行时间。用户断开 SSE 或调用取消接口时，`context.Context` 会同时取消模型、MCP 和 Shell 子进程。网络错误、HTTP 408、409、429 和 5xx 错误最多重试五次，等待时间依次增长并加入随机抖动。鉴权、参数等永久错误立即返回。已经产生输出的流发生错误时直接结束当前 Run，避免把重试内容重复发送给客户端。

同一个 `conversation_id` 同时只运行一个 Run。内存中的 `run_id -> cancel` 表提供显式取消能力，进程退出后由 HTTP 连接和操作系统清理剩余任务。

## HTTP 接口

发起对话：

```bash
curl -N http://127.0.0.1:1883/api/chat \
  -H 'Content-Type: application/json' \
  -d '{"conversation_id":"main","message":"创建一个待办应用"}'
```

响应使用 SSE：

```text
event: run.started
data: {"type":"run.started","run_id":"...","sequence":1,...}

event: message.delta
data: {"type":"message.delta","payload":{"content":"..."},...}

event: tool.started
event: tool.output
event: tool.completed
event: run.completed
```

通过 Gateway 调用时，请求路径为：

```text
POST /api/agent-sessions/invocations/api/chat
```

其他接口：

```text
POST /api/runs/{run_id}/cancel
GET  /api/conversations/{conversation_id}/messages
GET  /health
```

## 本地工具

内置工具全部在 `agentd` 所在容器中执行：

```text
shell       运行 Bash、构建、测试、Git、Python、Node.js 和 agent-browser
read_file   读取文本文件
write_file  原子创建或替换文件
edit_file   按精确旧文本修改文件
list_files  递归列出文件
grep        使用 Go 正则表达式搜索文本
read_skill  按需加载完整 SKILL.md
```

所有文件路径都经过绝对路径、相对路径和符号链接检查，只允许访问 `/workspace`。`shell` 使用独立进程组；取消 Run 时会终止命令及其子进程。命令没有固定执行超时。

## MCP

`agentd` 使用官方 Go MCP SDK 建立连接，再通过 Eino `officialmcp` 转换为普通 Eino 工具。配置按顺序读取：

```text
/etc/agentland/mcp.json
/workspace/.agentland/mcp.json
```

后一份配置可以覆盖同名 Server。示例：

```json
{
  "servers": [
    {
      "name": "local-files",
      "transport": "stdio",
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-filesystem", "/workspace"]
    },
    {
      "name": "remote-tools",
      "transport": "streamable_http",
      "url": "https://example.com/mcp",
      "headers": {
        "Authorization": "Bearer ${MCP_TOKEN}"
      },
      "tools": ["search", "fetch"]
    }
  ]
}
```

环境变量在读取 JSON 时展开。暴露给模型的工具名为 `mcp__{server}__{tool}`，避免与内置工具或其他 Server 重名。`stdio` 进程和 HTTP Session 随 `agentd` 一起关闭。

## Skills

Skills 来源为：

```text
/app/skills
/workspace/.agentland/skills
```

启动时只解析每个 `SKILL.md` 的 YAML Front Matter，并把名称和描述加入系统提示。模型决定使用某项 Skill 后，再调用 `read_skill` 读取正文。工作区内的同名 Skill 覆盖镜像内版本。镜像会复制仓库现有的 `agent-browser` Skill，并安装对应 CLI 和 Chromium。

## Memory

会话数据保存在普通文件中：

```text
/workspace/.agentland/
├── conversations/{conversation_id}/history.jsonl
├── conversations/{conversation_id}/summary.json
└── MEMORY.md
```

`history.jsonl` 以追加方式保存用户消息、模型消息、ToolCall 和 ToolMessage。每条消息完整写入后执行 `fsync`，进程意外退出造成的损坏尾行会在恢复时忽略，中间记录损坏会返回错误。

上下文估算值达到模型窗口的 70% 时，Agent 使用同一个模型概括较早的完整对话段，并在后续请求中使用系统提示、`MEMORY.md`、摘要和最近消息。原始 JSONL 始终保留。`AL_AGENT_MODEL_CONTEXT_TOKENS` 用于声明模型窗口，默认值为 `128000`。

`agentd` 只看到 `/workspace` 文件系统。运行环境使用持久目录时，Memory 和生成代码可以跨容器恢复；运行环境使用临时目录时，数据跟随容器生命周期。

## 配置

```text
AL_AGENT_MODEL                    模型名称
AL_AGENT_MODEL_BASE_URL           OpenAI 兼容接口地址
AL_AGENT_MODEL_API_KEY            模型密钥
AL_AGENT_MODEL_CONTEXT_TOKENS     模型上下文窗口，默认 128000
AL_AGENTD_WORKSPACE_ROOT          工作区，默认 /workspace
AL_AGENTD_SKILLS_DIR              内置 Skills，默认 /app/skills
AL_AGENTD_MCP_CONFIG_PATHS        MCP 配置路径，使用逗号分隔
AL_AGENTD_SYSTEM_PROMPT           自定义系统提示
AL_AGENTD_AUTH_ENABLED            是否校验 Sandbox JWT，默认 true
AL_SANDBOX_JWT_PUBLIC_KEY_PATH    JWT 公钥路径
AL_SANDBOX_JWT_ISSUER             JWT issuer
AL_SANDBOX_JWT_AUDIENCE           JWT audience
AL_SANDBOX_JWT_CLOCK_SKEW         JWT 时间偏差
```

## 构建与测试

```bash
go test ./pkg/agentd ./cmd/agentd
docker build -f docker/Dockerfile.agentd -t agentland-agentd:dev .
```

本地启动示例：

```bash
docker run --rm -p 1883:1883 \
  -v "$PWD/workspace:/workspace" \
  -e AL_AGENT_MODEL=gpt-4.1 \
  -e AL_AGENT_MODEL_API_KEY="$OPENAI_API_KEY" \
  -e AL_AGENTD_AUTH_ENABLED=false \
  agentland-agentd:dev
```

单元测试覆盖超过 32 次的连续工具循环、五次模型重试、永久模型错误、工作区路径隔离、文件修改、Shell、两种 MCP 传输、Skills 覆盖、Memory 尾行恢复以及 SSE 接口。
