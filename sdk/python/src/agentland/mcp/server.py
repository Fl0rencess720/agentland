"""MCP server registration for Agentland code-runner tools."""

from __future__ import annotations

import asyncio
from typing import TYPE_CHECKING

from .bridge import CodeInterpreterToolBridge

if TYPE_CHECKING:
    from mcp.server.fastmcp import FastMCP


def _require_fastmcp() -> type["FastMCP"]:
    try:
        from mcp.server.fastmcp import FastMCP
    except ImportError as exc:  # pragma: no cover
        raise RuntimeError("Missing MCP dependency.") from exc
    return FastMCP


def create_server(*, base_url: str, timeout: int = 30) -> "FastMCP":
    """Create MCP server with tools aligned with gateway MCP."""
    FastMCP = _require_fastmcp()
    mcp = FastMCP(
        "Agentland Code Runner",
        instructions=(
            "Use sandbox_create to create sandbox and keep sandbox_id. "
            "Use code_execute for one-shot execution. "
            "Use fs_tree/fs_file_get/fs_file_write for filesystem operations."
        ),
    )
    bridge = CodeInterpreterToolBridge(base_url=base_url, timeout=timeout)

    @mcp.tool()
    async def sandbox_create(language: str = "") -> dict:
        """Create a code runner sandbox session."""
        return await asyncio.to_thread(bridge.sandbox_create, language=language)

    @mcp.tool()
    async def code_execute(
        sandbox_id: str,
        code: str,
        *,
        language: str = "",
        cwd: str = "",
        timeout_ms: int = 0,
    ) -> dict:
        """Execute code once in a temporary context that is deleted asynchronously after execution."""
        return await asyncio.to_thread(
            bridge.code_execute,
            sandbox_id=sandbox_id,
            code=code,
            language=language,
            cwd=cwd,
            timeout_ms=timeout_ms,
        )

    @mcp.tool()
    async def fs_tree(
        sandbox_id: str,
        *,
        path: str = "",
        depth: int = 0,
        includeHidden: bool = False,
    ) -> dict:
        """List files and directories under a path."""
        return await asyncio.to_thread(
            bridge.fs_tree,
            sandbox_id=sandbox_id,
            path=path,
            depth=depth,
            includeHidden=includeHidden,
        )

    @mcp.tool()
    async def fs_file_get(
        sandbox_id: str,
        path: str,
        *,
        encoding: str = "",
    ) -> dict:
        """Read file content with utf8 or base64 encoding."""
        return await asyncio.to_thread(
            bridge.fs_file_get,
            sandbox_id=sandbox_id,
            path=path,
            encoding=encoding,
        )

    @mcp.tool()
    async def fs_file_write(
        sandbox_id: str,
        path: str,
        content: str,
        *,
        encoding: str = "",
    ) -> dict:
        """Write file content with utf8 or base64 encoding."""
        return await asyncio.to_thread(
            bridge.fs_file_write,
            sandbox_id=sandbox_id,
            path=path,
            content=content,
            encoding=encoding,
        )

    return mcp

