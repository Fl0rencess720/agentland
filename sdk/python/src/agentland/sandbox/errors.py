"""SDK error definitions."""

from __future__ import annotations


class SDKError(Exception):
    """Represents an HTTP or business-level SDK failure."""

    def __init__(
        self,
        message: str,
        *,
        http_status: int | None = None,
        code: int | None = None,
        response_text: str | None = None,
    ) -> None:
        super().__init__(message)
        self.http_status = http_status
        self.code = code
        self.response_text = response_text

    def __str__(self) -> str:
        parts = [super().__str__()]
        if self.http_status is not None:
            parts.append(f"http_status={self.http_status}")
        if self.code is not None:
            parts.append(f"code={self.code}")
        return ", ".join(parts)

