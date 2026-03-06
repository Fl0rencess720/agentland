# Agentland Python SDK

Agentland Python SDK lets you create sandboxes, run code, stream execution
events, and query the current buffered output of a running execution.

Agentland Python SDK provides:

1. `agentland.sandbox` for code-runner APIs.
2. `agentland mcp` command to start the local MCP server backed by this SDK.

## Quick start

```bash
pip install agentland
agentland mcp --transport stdio --base-url http://127.0.0.1:8080
```

## Run code and read `execution_id`

This example streams one execution, reads the `execution_id` from the first
event, and then queries the current buffered output with that ID.

```python
from agentland.sandbox import Sandbox

Sandbox.configure(base_url="http://127.0.0.1:8080", timeout=30)

sandbox = Sandbox.create()
context = sandbox.context.create(language="python", cwd="/workspace")

execution_id = None
for event in context.exec_stream(
    "import time\nfor i in range(3):\n    print(i)\n    time.sleep(1)",
    timeout_ms=30000,
):
    if event.execution_id and execution_id is None:
        execution_id = event.execution_id
    if event.type == "stdout" and event.text:
        print(event.text, end="")
    if event.type == "execution_complete":
        break

if execution_id is not None:
    output = context.get_output(execution_id)
    print(output.state)
    print(output.stdout)
```

## Run code and wait for the final result

If you only need the final output, use `Context.exec()`.

```python
from agentland.sandbox import Sandbox

Sandbox.configure(base_url="http://127.0.0.1:8080", timeout=30)

sandbox = Sandbox.create()
context = sandbox.context.create(language="bash", cwd="/workspace")
result = context.exec("echo hello", timeout_ms=30000)

print(result.execution_id)
print(result.stdout)
print(result.exit_code)
```
