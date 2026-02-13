# agentland

`agentland` 是一个面向 AI Agent 的 Kubernetes 沙箱运行时平台，支持代码执行场景和
通用 Agent 调用场景。通过统一的 Gateway API 暴露能力，提供两种主要自定义资源定义（CRD）：
`CodeInterpreter`（直接代码执行）和 `AgentSession`（通用 Agent 调用）。

控制面由一组 Kubernetes 控制器组成，负责把自定义资源（CR）收敛为真实的
Sandbox Pod。你可以通过预热池减少沙箱冷启动时延，通过 Gateway 签发的
短时 JWT 进行鉴权。

## 项目能力

`agentland` 聚焦三类核心能力：会话化执行、Kubernetes 原生生命周期管理、
以及安全请求转发。

- 提供代码执行 API：`POST /api/code-runner/run`
- 提供通用 Agent 调用 API：
  `POST/GET /api/agent-sessions/invocations/*path`
- 通过 `AgentRuntime` 抽象运行时模板，避免在请求链路中硬编码镜像
- 通过 `SandboxPool + SandboxClaim` 提供预热池调度能力
- 在 `agentcore` 内置基于空闲时长与最大会话时长的 GC 机制
- 使用 JWT 在 Gateway 与 CodeInterpreter Sandbox Pod 之间做鉴权

## 架构概览

系统由三个核心组件和一组控制器/CRD 组成。

1. **Gateway**：接收外部 HTTP 请求
2. **AgentCore（controller manager + gRPC）**：创建会话类 CR 并等待就绪，将 CR 收敛为 `Sandbox` 与 Pod 状态
3. **Korokd**：运行在 CodeInterpreter Sandbox Pod 内，负责代码执行和鉴权功能。

CodeInterpreter 代码执行链路如下：

1. 客户端请求 `Gateway /api/code-runner/run`
2. Gateway 调用 `AgentCore.CreateCodeInterpreter`
3. AgentCore 创建 `CodeInterpreter` CR
4. 控制器创建 `SandboxClaim/Sandbox/Pod`（或直连 `Sandbox/Pod`）
5. Gateway 反向代理到 Sandbox 内的 `Korokd /api/execute`

Agent 调用链路如下：

1. 客户端请求 `Gateway /api/agent-sessions/invocations/*path`
2. Gateway 解析 `runtime_name/runtime_namespace`
3. Gateway 调用 `AgentCore.CreateAgentSession`
4. AgentCore 创建带 `runtimeRef` 的 `AgentSession`
5. `AgentSession` 控制器解析 `AgentRuntime` 并完成 Sandbox 资源编排
6. Gateway 保留路径和方法反向代理到 Sandbox

## 核心 CRD

控制面 API Group 为 `agentland.fl0rencess720.app/v1alpha1`。

- `CodeInterpreter`：代码执行会话资源
- `AgentRuntime`：可复用的 Agent 运行时模板，Agent 应用的镜像在此定义
- `AgentSession`：通用 Agent 会话资源，引用 `AgentRuntime`
- `Sandbox`：与实际运行 Pod 一一对应
- `SandboxPool`：预热 Pod 池
- `SandboxClaim`：从预热池中分配沙箱的请求

## 📄 License

agentland 采用 [Apache License 2.0](LICENSE) 开源许可证发布
