"""Structured SDK result objects."""

from __future__ import annotations

from dataclasses import dataclass
from collections.abc import Mapping
from typing import Any

from .errors import SDKError


def _as_str(value: Any, field_name: str) -> str:
    if value is None:
        raise SDKError(f"{field_name} must be a string")
    return str(value)


def _as_int(value: Any, field_name: str) -> int:
    if isinstance(value, bool):
        raise SDKError(f"{field_name} must be an integer")
    if isinstance(value, int):
        return value
    try:
        return int(value)
    except (TypeError, ValueError) as exc:
        raise SDKError(f"{field_name} must be an integer") from exc


@dataclass(slots=True)
class ExecutionResult:
    """Structured execution response."""

    context_id: str
    execution_count: int
    exit_code: int
    stdout: str
    stderr: str
    duration_ms: int
    execution_id: str | None = None

    @classmethod
    def from_payload(cls, payload: Mapping[str, Any]) -> "ExecutionResult":
        if not isinstance(payload, Mapping):
            raise SDKError("execution response payload must be an object")
        execution_id_raw = payload.get("execution_id")
        execution_id = None if execution_id_raw is None else str(execution_id_raw)
        return cls(
            execution_id=execution_id,
            context_id=_as_str(payload.get("context_id", ""), "context_id"),
            execution_count=_as_int(payload.get("execution_count", 0), "execution_count"),
            exit_code=_as_int(payload.get("exit_code", 0), "exit_code"),
            stdout=_as_str(payload.get("stdout", ""), "stdout"),
            stderr=_as_str(payload.get("stderr", ""), "stderr"),
            duration_ms=_as_int(payload.get("duration_ms", 0), "duration_ms"),
        )

    def to_dict(self) -> dict[str, Any]:
        return {
            "execution_id": self.execution_id,
            "context_id": self.context_id,
            "execution_count": self.execution_count,
            "exit_code": self.exit_code,
            "stdout": self.stdout,
            "stderr": self.stderr,
            "duration_ms": self.duration_ms,
        }


@dataclass(slots=True)
class ExecutionOutput:
    """Current buffered output for one execution."""

    execution_id: str
    context_id: str
    state: str
    execution_count: int
    exit_code: int | None
    stdout: str
    stderr: str
    duration_ms: int
    error: str | None = None

    @classmethod
    def from_payload(cls, payload: Mapping[str, Any]) -> "ExecutionOutput":
        if not isinstance(payload, Mapping):
            raise SDKError("execution output payload must be an object")

        exit_code_raw = payload.get("exit_code")
        exit_code = None if exit_code_raw is None else _as_int(exit_code_raw, "exit_code")

        error_raw = payload.get("error")
        error = None if error_raw is None else str(error_raw)

        return cls(
            execution_id=_as_str(payload.get("execution_id", ""), "execution_id"),
            context_id=_as_str(payload.get("context_id", ""), "context_id"),
            state=_as_str(payload.get("state", ""), "state"),
            execution_count=_as_int(payload.get("execution_count", 0), "execution_count"),
            exit_code=exit_code,
            stdout=_as_str(payload.get("stdout", ""), "stdout"),
            stderr=_as_str(payload.get("stderr", ""), "stderr"),
            duration_ms=_as_int(payload.get("duration_ms", 0), "duration_ms"),
            error=error,
        )

    def to_dict(self) -> dict[str, Any]:
        return {
            "execution_id": self.execution_id,
            "context_id": self.context_id,
            "state": self.state,
            "execution_count": self.execution_count,
            "exit_code": self.exit_code,
            "stdout": self.stdout,
            "stderr": self.stderr,
            "duration_ms": self.duration_ms,
            "error": self.error,
        }


@dataclass(slots=True)
class PreviewLink:
    """Signed preview link for one sandbox port."""

    session_id: str
    port: int
    preview_token: str
    preview_url: str
    expires_at: str

    @classmethod
    def from_payload(cls, payload: Mapping[str, Any]) -> "PreviewLink":
        if not isinstance(payload, Mapping):
            raise SDKError("preview payload must be an object")

        return cls(
            session_id=_as_str(payload.get("session_id", ""), "session_id"),
            port=_as_int(payload.get("port", 0), "port"),
            preview_token=_as_str(
                payload.get("preview_token", ""),
                "preview_token",
            ),
            preview_url=_as_str(payload.get("preview_url", ""), "preview_url"),
            expires_at=_as_str(payload.get("expires_at", ""), "expires_at"),
        )

    def to_dict(self) -> dict[str, Any]:
        return {
            "session_id": self.session_id,
            "port": self.port,
            "preview_token": self.preview_token,
            "preview_url": self.preview_url,
            "expires_at": self.expires_at,
        }


@dataclass(slots=True)
class ExecutionStreamEvent:
    """One SSE event during execution."""

    type: str
    timestamp: int | None = None
    context_id: str | None = None
    execution_id: str | None = None
    text: str | None = None
    execution_count: int | None = None
    execution_time: int | None = None
    exit_code: int | None = None
    result: ExecutionResult | None = None
    error: str | None = None

    @classmethod
    def from_payload(cls, payload: Mapping[str, Any]) -> "ExecutionStreamEvent":
        if not isinstance(payload, Mapping):
            raise SDKError("stream event payload must be an object")

        evt_type = _as_str(payload.get("type", ""), "type").strip()
        if not evt_type:
            raise SDKError("type is required")

        timestamp = payload.get("timestamp")
        ts = None if timestamp is None else _as_int(timestamp, "timestamp")

        context_id_raw = payload.get("context_id")
        context_id = None if context_id_raw is None else str(context_id_raw)

        execution_id_raw = payload.get("execution_id")
        execution_id = None if execution_id_raw is None else str(execution_id_raw)

        text_raw = payload.get("text")
        text = None if text_raw is None else str(text_raw)

        ec_raw = payload.get("execution_count")
        execution_count = None if ec_raw is None else _as_int(ec_raw, "execution_count")

        et_raw = payload.get("execution_time")
        execution_time = None if et_raw is None else _as_int(et_raw, "execution_time")

        exit_code_raw = payload.get("exit_code")
        exit_code = None if exit_code_raw is None else _as_int(exit_code_raw, "exit_code")

        result_payload = payload.get("result")
        result = None
        if isinstance(result_payload, Mapping):
            result = ExecutionResult.from_payload(result_payload)

        err_raw = payload.get("error")
        error = None if err_raw is None else str(err_raw)

        return cls(
            type=evt_type,
            timestamp=ts,
            context_id=context_id,
            execution_id=execution_id,
            text=text,
            execution_count=execution_count,
            execution_time=execution_time,
            exit_code=exit_code,
            result=result,
            error=error,
        )
