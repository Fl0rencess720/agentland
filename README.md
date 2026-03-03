# agentland

`agentland` 是一个面向 AI Agent 的 Kubernetes 沙箱运行时平台，支持 Sandbox as Tool 和 Agent IN Sandbox 两类场景。通过统一的 Gateway API 暴露能力，提供 Python SDK 和 MCP。

`agentland` 提供两种主要 Kubernetes 自定义资源定义（CRD），各自对应两类应用场景 `CodeInterpreter`（通用工具沙箱）和 `AgentSession`（Agent 运行时沙箱），可通预热池实现沙箱亚秒级启动。

## 架构概览

系统由三个核心组件和一组控制器/CRD 组成。

1. **Gateway**：接收外部 HTTP 请求
2. **AgentCore（controller manager + gRPC）**：创建 CR 并等待就绪，将 CR 收敛为 `Sandbox` 与 Pod 状态
3. **Korokd**：运行在 CodeInterpreter Sandbox Pod 内，负责代码执行、文件操作、浏览器操作和鉴权功能。

## MCP 与 Python SDK

项目提供 Python SDK 和本地 MCP Server。

### 启动本地 MCP Server

安装 SDK 后，可以直接启动 MCP Server：

`--base-url` 填入 agentland-gateway 的 URL

```bash
pip install agentland
agentland mcp --transport stdio --base-url http://127.0.0.1:8080 --timeout 30
```

也可以通过环境变量设置默认网关地址与超时：

```bash
export AGENTLAND_BASE_URL=http://127.0.0.1:8080
export AGENTLAND_TIMEOUT=30
agentland mcp --transport stdio
```

### Python SDK 快速上手

**代码执行示例：**

```python
from agentland.sandbox import Sandbox

Sandbox.configure(base_url="http://127.0.0.1:8080", timeout=30)

# 1) 创建代码执行沙箱
sandbox = Sandbox.create()

# 2) 创建执行上下文
context = sandbox.context.create(language="python", cwd="/workspace")

# 3) 第一次执行：定义变量 x
first = context.exec("x = 41")
print(first.get("stdout", ""))

# 4) 第二次执行：直接访问上一次 exec 定义的变量 x
second = context.exec("print('x + 1 =', x + 1)")
print(second.get("stdout", ""))

# 5) 删除 context
context.delete()
```

**文件操作示例：**

```python
from agentland.sandbox import Sandbox

Sandbox.configure(base_url="http://127.0.0.1:8080", timeout=30)

# 1) 创建代码执行沙箱
sandbox = Sandbox.create()

# 2) 在 sandbox 内写入文件
sandbox.fs.write("/workspace/data.txt", "line1\nline2")

# 3) 读取该文件内容
file_resp = sandbox.fs.read("/workspace/data.txt", encoding="utf8")

# 4) 打印读取结果
print(file_resp.get("content", ""))
```

**浏览器操作示例：**

浏览器操作使用 [agent-browser](https://github.com/vercel-labs/agent-browser) 实现

```python
from agentland.sandbox import Sandbox

Sandbox.configure(base_url="http://127.0.0.1:8080", timeout=30)

# 1) 创建代码执行沙箱
sandbox = Sandbox.create()

# 2) 创建 shell context
shell_context = sandbox.context.create(language="shell", cwd="/workspace")

# 3) 在同一个 context 内执行 agent-browser 帮助命令
help_resp = shell_context.exec("agent-browser --help")
print(help_resp.get("stdout", ""))

# 4) 删除 shell context
shell_context.delete()
```

## 核心 CRD

控制面 API Group 为 `agentland.fl0rencess720.app/v1alpha1`。

- `CodeInterpreter`：代码执行会话资源
- `AgentRuntime`：可复用的 Agent 运行时模板，Agent 应用的镜像在此定义
- `AgentSession`：通用 Agent 会话资源，引用 `AgentRuntime`
- `Sandbox`：与实际运行 Pod 一一对应
- `SandboxPool`：预热 Pod 池
- `SandboxClaim`：从预热池中分配沙箱的请求

## Helm 部署

你可以使用 Helm 直接安装 `agentland`。当前 chart 路径为
`charts/agentland`，资源名使用统一前缀策略，执行
`helm install agentland ...` 时会生成 `agentland-*` 资源名。

安装或升级 Helm release。

```bash
helm upgrade --install agentland charts/agentland \
  -n agentland-system \
  --create-namespace
```

### 部署验证

安装完成后，你可以用以下命令检查核心资源是否就绪。

```bash
kubectl -n agentland-system get deploy,svc,sa
kubectl -n agentland-system get pods
kubectl -n agentland-sandboxes get pods
kubectl get crd | grep 'agentland.fl0rencess720.app'
```

### 卸载

如果你需要回收 Helm 部署资源，先卸载 release，再按需删除命名空间。

```bash
helm uninstall agentland -n agentland-system
kubectl delete ns agentland-sandboxes --ignore-not-found=true
```

## 📄 License

agentland 采用 [Apache License 2.0](LICENSE) 开源许可证发布
