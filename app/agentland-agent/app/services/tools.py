from __future__ import annotations

"""coding agent 使用的工具实现。"""

import asyncio
import json
import os
import subprocess
import threading
from pathlib import Path

from langchain_core.tools import tool

from app.services.mcp_config import load_mcp_server_configs

try:
    from langchain_mcp_adapters.client import MultiServerMCPClient
except ImportError:  # pragma: no cover - exercised only when dependency is absent
    MultiServerMCPClient = None

_tool_cache_lock = threading.Lock()
_tool_cache: dict[str, list[object]] = {}


@tool
def bash(command: str, cwd: str = "", timeout_ms: int = 0) -> str:
    """Run a shell command and return stdout/stderr/exit_code as JSON."""
    if not command:
        raise ValueError("missing command")

    timeout = None
    if timeout_ms > 0:
        timeout = timeout_ms / 1000.0

    try:
        completed = subprocess.run(
            ["bash", "-lc", command],
            cwd=cwd or None,
            capture_output=True,
            text=True,
            timeout=timeout,
            check=False,
        )
        exit_code = completed.returncode
        stdout = completed.stdout
        stderr = completed.stderr
    except subprocess.TimeoutExpired as exc:
        # 与常见 shell 约定保持一致：超时退出码使用 124。
        exit_code = 124
        stdout = exc.stdout.decode("utf-8", errors="replace") if isinstance(exc.stdout, bytes) else (exc.stdout or "")
        stderr = exc.stderr.decode("utf-8", errors="replace") if isinstance(exc.stderr, bytes) else (exc.stderr or "")

    return json.dumps({"stdout": stdout, "stderr": stderr, "exit_code": exit_code}, ensure_ascii=False)


@tool
def read(path: str, offset: int = 0, max_bytes: int = 65536) -> str:
    """Read a file. Returns JSON with content and truncation metadata."""
    if not path:
        raise ValueError("missing path")
    if max_bytes <= 0:
        max_bytes = 65536
    if offset < 0:
        raise ValueError("offset must be >= 0")

    file_path = Path(path)
    with file_path.open("rb") as handle:
        if offset > 0:
            handle.seek(offset)
        content = handle.read(max_bytes)
        # 额外读取 1 字节用于标记是否截断，避免一次性读完整文件。
        truncated = len(handle.read(1)) > 0

    return json.dumps(
        {
            "path": path,
            "offset": offset,
            "bytes_read": len(content),
            "truncated": truncated,
            "content": content.decode("utf-8", errors="replace"),
        },
        ensure_ascii=False,
    )


@tool
def write(path: str, content: str, append: bool = False, mkdir: bool = True) -> str:
    """Write a file. Returns JSON with bytes_written."""
    if not path:
        raise ValueError("missing path")

    file_path = Path(path)
    if mkdir:
        os.makedirs(file_path.parent, exist_ok=True)

    mode = "a" if append else "w"
    with file_path.open(mode, encoding="utf-8") as handle:
        written = handle.write(content)

    return json.dumps({"path": path, "bytes_written": written, "appended": append}, ensure_ascii=False)


def default_tools() -> list[object]:
    """按稳定顺序返回工具列表，便于模型绑定。"""

    return [bash, read, write]


def load_tools() -> list[object]:
    """返回基础工具加上可选 MCP 工具。"""

    base_tools = default_tools()
    mcp_servers = load_mcp_server_configs()
    if not mcp_servers:
        return base_tools

    cache_key = json.dumps(mcp_servers, sort_keys=True, ensure_ascii=False)
    with _tool_cache_lock:
        cached = _tool_cache.get(cache_key)
        if cached is not None:
            return cached

    mcp_tools = _load_mcp_tools_sync(mcp_servers)
    merged_tools = [*base_tools, *mcp_tools]
    with _tool_cache_lock:
        _tool_cache[cache_key] = merged_tools
    return merged_tools


def tool_signature(tools: list[object]) -> tuple[str, ...]:
    """为工具集生成稳定签名，用于缓存绑定模型。"""

    return tuple(_tool_name(tool) for tool in tools)


def clear_tool_cache() -> None:
    """测试辅助：清空 MCP tool 缓存。"""

    with _tool_cache_lock:
        _tool_cache.clear()


def _load_mcp_tools_sync(mcp_servers: dict[str, object]) -> list[object]:
    if MultiServerMCPClient is None:
        raise RuntimeError(
            "MCP tools are configured, but langchain-mcp-adapters is not installed. "
            "Add it to the environment before starting the service."
        )
    return asyncio.run(_load_mcp_tools_async(mcp_servers))


async def _load_mcp_tools_async(mcp_servers: dict[str, object]) -> list[object]:
    client = MultiServerMCPClient(mcp_servers)
    tools = await client.get_tools()
    return list(tools)


def _tool_name(tool: object) -> str:
    name = getattr(tool, "name", None)
    if isinstance(name, str) and name:
        return name
    function_name = getattr(tool, "__name__", None)
    if isinstance(function_name, str) and function_name:
        return function_name
    return repr(tool)
