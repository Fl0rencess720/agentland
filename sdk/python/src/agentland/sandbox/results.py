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

    @classmethod
    def from_payload(cls, payload: Mapping[str, Any]) -> "ExecutionResult":
        if not isinstance(payload, Mapping):
            raise SDKError("execution response payload must be an object")
        return cls(
            context_id=_as_str(payload.get("context_id", ""), "context_id"),
            execution_count=_as_int(payload.get("execution_count", 0), "execution_count"),
            exit_code=_as_int(payload.get("exit_code", 0), "exit_code"),
            stdout=_as_str(payload.get("stdout", ""), "stdout"),
            stderr=_as_str(payload.get("stderr", ""), "stderr"),
            duration_ms=_as_int(payload.get("duration_ms", 0), "duration_ms"),
        )

    def to_dict(self) -> dict[str, Any]:
        return {
            "context_id": self.context_id,
            "execution_count": self.execution_count,
            "exit_code": self.exit_code,
            "stdout": self.stdout,
            "stderr": self.stderr,
            "duration_ms": self.duration_ms,
        }
