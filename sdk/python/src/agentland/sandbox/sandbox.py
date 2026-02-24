"""Public sandbox SDK objects."""

from __future__ import annotations

import os
from dataclasses import dataclass
from typing import Any

from ._http import _HTTPClient
from .errors import SDKError

DEFAULT_TIMEOUT_SECONDS = 30


def _normalize_language(language: str) -> str:
    value = language.strip().lower()
    if value not in {"python", "shell"}:
        raise SDKError("language must be 'python' or 'shell'")
    return value


def _ensure_timeout(timeout_ms: int) -> int:
    if timeout_ms < 100 or timeout_ms > 300000:
        raise SDKError("timeout_ms must be between 100 and 300000")
    return timeout_ms


def _ensure_non_empty(name: str, value: str) -> str:
    cleaned = value.strip()
    if not cleaned:
        raise SDKError(f"{name} is required")
    return cleaned


@dataclass(slots=True)
class _SDKConfig:
    base_url: str | None = None
    timeout: int = DEFAULT_TIMEOUT_SECONDS


class Sandbox:
    """Represents one code-runner sandbox session."""

    _config = _SDKConfig()

    @classmethod
    def configure(cls, *, base_url: str, timeout: int = DEFAULT_TIMEOUT_SECONDS) -> None:
        cls._config = _SDKConfig(base_url=base_url.strip().rstrip("/"), timeout=timeout)

    @classmethod
    def _client(cls) -> _HTTPClient:
        if not cls._config.base_url:
            raise SDKError("SDK is not configured. Call Sandbox.configure(base_url=...) first")
        return _HTTPClient(base_url=cls._config.base_url, timeout=cls._config.timeout)

    @classmethod
    def create(cls, language: str = "python") -> Sandbox:
        payload = {"language": _normalize_language(language)}
        out = cls._client().request_json("POST", "/api/code-runner/sandboxes", json_body=payload)
        sandbox_id = _ensure_non_empty("sandbox_id", str(out.get("sandbox_id", "")))
        return cls(sandbox_id=sandbox_id, _client=cls._client())

    @classmethod
    def connect(cls, sandbox_id: str) -> Sandbox:
        # Connect does not call server-side lookup by design.
        return cls(
            sandbox_id=_ensure_non_empty("sandbox_id", sandbox_id),
            _client=cls._client(),
        )

    def __init__(self, *, sandbox_id: str, _client: _HTTPClient) -> None:
        self.sandbox_id = sandbox_id
        self._client_impl = _client
        self.context = _ContextService(self)
        self.fs = _FSService(self)


class _ContextService:
    def __init__(self, sandbox: Sandbox) -> None:
        self._sandbox = sandbox

    def create(self, language: str = "python", cwd: str = "/workspace") -> Context:
        payload: dict[str, Any] = {"language": _normalize_language(language)}
        if cwd.strip():
            payload["cwd"] = cwd.strip()
        out = self._sandbox._client_impl.request_json(
            "POST",
            "/api/code-runner/contexts",
            session_id=self._sandbox.sandbox_id,
            json_body=payload,
        )
        context_id = _ensure_non_empty("context_id", str(out.get("context_id", "")))
        return Context(sandbox=self._sandbox, context_id=context_id)


class Context:
    """Represents one execution context inside a sandbox."""

    def __init__(self, *, sandbox: Sandbox, context_id: str) -> None:
        self._sandbox = sandbox
        self.context_id = _ensure_non_empty("context_id", context_id)

    def exec(self, code: str, timeout_ms: int = 30000) -> dict[str, Any]:
        payload = {
            "code": _ensure_non_empty("code", code),
            "timeout_ms": _ensure_timeout(timeout_ms),
        }
        return self._sandbox._client_impl.request_json(
            "POST",
            f"/api/code-runner/contexts/{self.context_id}/execute",
            session_id=self._sandbox.sandbox_id,
            json_body=payload,
        )

    def delete(self) -> dict[str, Any]:
        return self._sandbox._client_impl.request_json(
            "DELETE",
            f"/api/code-runner/contexts/{self.context_id}",
            session_id=self._sandbox.sandbox_id,
        )


class _FSService:
    def __init__(self, sandbox: Sandbox) -> None:
        self._sandbox = sandbox

    def tree(
        self,
        path: str = ".",
        depth: int = 5,
        include_hidden: bool = False,
    ) -> dict[str, Any]:
        if depth < 1 or depth > 20:
            raise SDKError("depth must be between 1 and 20")
        return self._sandbox._client_impl.request_json(
            "GET",
            "/api/code-runner/fs/tree",
            session_id=self._sandbox.sandbox_id,
            query={
                "path": path,
                "depth": depth,
                "includeHidden": "true" if include_hidden else "false",
            },
        )

    def read(self, path: str, encoding: str = "utf8") -> dict[str, Any]:
        clean_path = _ensure_non_empty("path", path)
        return self._sandbox._client_impl.request_json(
            "GET",
            "/api/code-runner/fs/file",
            session_id=self._sandbox.sandbox_id,
            query={"path": clean_path, "encoding": encoding},
        )

    def write(self, path: str, content: str, encoding: str = "utf8") -> dict[str, Any]:
        payload = {
            "path": _ensure_non_empty("path", path),
            "content": content,
            "encoding": encoding,
        }
        return self._sandbox._client_impl.request_json(
            "POST",
            "/api/code-runner/fs/file",
            session_id=self._sandbox.sandbox_id,
            json_body=payload,
        )

    def upload(self, file: str, target_file_path: str) -> dict[str, Any]:
        local_file = _ensure_non_empty("file", file)
        target = _ensure_non_empty("target_file_path", target_file_path)
        if not os.path.isfile(local_file):
            raise SDKError(f"file does not exist: {local_file}")
        return self._sandbox._client_impl.upload_file(
            session_id=self._sandbox.sandbox_id,
            local_file=local_file,
            target_file_path=target,
        )

    def download(self, path: str, save_path: str) -> dict[str, Any]:
        remote = _ensure_non_empty("path", path)
        local = _ensure_non_empty("save_path", save_path)
        resp = self._sandbox._client_impl.download_file(
            session_id=self._sandbox.sandbox_id,
            remote_path=remote,
        )

        parent = os.path.dirname(local)
        if parent:
            os.makedirs(parent, exist_ok=True)
        with open(local, "wb") as fh:
            fh.write(resp.body)

        file_name = ""
        content_disposition = resp.headers.get("Content-Disposition", "")
        marker = "filename="
        if marker in content_disposition:
            raw_name = content_disposition.split(marker, 1)[1].strip()
            file_name = raw_name.strip('"')
        if not file_name:
            file_name = os.path.basename(local)

        source_path = resp.headers.get("X-Agentland-File-Path", remote)
        return {
            "source_path": source_path,
            "save_path": local,
            "file_name": file_name,
            "size": len(resp.body),
        }

