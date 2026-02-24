"""Bridge layer between MCP tools and Agentland SDK."""

from __future__ import annotations

import sys
from threading import Thread
from typing import Any

from agentland.sandbox import SDKError, Sandbox


class CodeInterpreterToolBridge:
    """Implements MCP tool semantics on top of the Python SDK."""

    def __init__(self, *, base_url: str, timeout: int = 30) -> None:
        Sandbox.configure(base_url=base_url, timeout=timeout)

    @staticmethod
    def _require_sandbox_id(sandbox_id: str) -> str:
        sid = sandbox_id.strip()
        if not sid:
            raise ValueError("sandbox_id is required")
        return sid

    @staticmethod
    def _normalize_language(language: str | None) -> str:
        normalized = (language or "").strip().lower()
        if not normalized:
            return "python"
        return normalized

    def sandbox_create(self, *, language: str | None = None) -> dict[str, Any]:
        sandbox = Sandbox.create(language=self._normalize_language(language))
        return {"sandbox_id": sandbox.sandbox_id}

    def code_execute(
        self,
        *,
        sandbox_id: str,
        code: str,
        language: str | None = None,
        cwd: str | None = None,
        timeout_ms: int = 0,
    ) -> dict[str, Any]:
        sid = self._require_sandbox_id(sandbox_id)
        if not code.strip():
            raise ValueError("code is required")

        sandbox = Sandbox.connect(sid)
        context = None
        try:
            context = sandbox.context.create(
                language=self._normalize_language(language),
                cwd=(cwd or "/workspace"),
            )
            timeout = timeout_ms if timeout_ms > 0 else 30000
            out = context.exec(code, timeout_ms=timeout)
            if not str(out.get("context_id", "")).strip():
                out["context_id"] = context.context_id
            return out
        finally:
            if context is not None:
                self._delete_context_async(context)

    @staticmethod
    def _delete_context_async(context: Any) -> None:
        def _run() -> None:
            try:
                context.delete()
            except SDKError as exc:
                print(
                    f"agentland-sdk: Failed to delete context asynchronously: {exc}",
                    file=sys.stderr,
                )
            except Exception as exc:
                print(
                    f"agentland-sdk: Unexpected error during async context deletion: {exc}",
                    file=sys.stderr,
                )

        Thread(target=_run, daemon=True).start()

    def fs_tree(
        self,
        *,
        sandbox_id: str,
        path: str = "",
        depth: int = 0,
        includeHidden: bool = False,
    ) -> dict[str, Any]:
        sid = self._require_sandbox_id(sandbox_id)
        sandbox = Sandbox.connect(sid)
        kwargs: dict[str, Any] = {
            "path": path.strip() or ".",
            "include_hidden": includeHidden,
        }
        if depth > 0:
            kwargs["depth"] = depth
        return sandbox.fs.tree(**kwargs)

    def fs_file_get(
        self,
        *,
        sandbox_id: str,
        path: str,
        encoding: str = "",
    ) -> dict[str, Any]:
        sid = self._require_sandbox_id(sandbox_id)
        sandbox = Sandbox.connect(sid)
        if encoding.strip():
            return sandbox.fs.read(path=path, encoding=encoding)
        return sandbox.fs.read(path=path)

    def fs_file_write(
        self,
        *,
        sandbox_id: str,
        path: str,
        content: str,
        encoding: str = "",
    ) -> dict[str, Any]:
        sid = self._require_sandbox_id(sandbox_id)
        sandbox = Sandbox.connect(sid)
        if encoding.strip():
            return sandbox.fs.write(path=path, content=content, encoding=encoding)
        return sandbox.fs.write(path=path, content=content)
