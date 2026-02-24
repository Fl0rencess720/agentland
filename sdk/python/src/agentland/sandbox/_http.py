"""Internal HTTP client for sandbox APIs."""

from __future__ import annotations

import json
import mimetypes
import os
import urllib.parse
from dataclasses import dataclass
from typing import IO, Any, Mapping

import httpx

from .errors import SDKError

SESSION_HEADER = "x-agentland-session"


@dataclass(slots=True)
class _Response:
    status: int
    headers: Mapping[str, str]
    body: bytes


def _decode_json_bytes(raw: bytes) -> Any:
    if not raw:
        return None
    text = raw.decode("utf-8", errors="replace").strip()
    if not text:
        return None
    try:
        return json.loads(text)
    except json.JSONDecodeError as exc:
        raise SDKError("response is not valid JSON", response_text=text) from exc


def _extract_error_message(data: Any, fallback: str) -> tuple[str, int | None]:
    if isinstance(data, dict):
        code = data.get("code")
        if isinstance(code, bool):
            code = None
        if not isinstance(code, int):
            code = None
        msg = data.get("msg") or data.get("error") or fallback
        if not isinstance(msg, str):
            msg = fallback
        return msg, code
    return fallback, None


class _HTTPClient:
    def __init__(self, *, base_url: str, timeout: int) -> None:
        normalized = base_url.strip().rstrip("/")
        if not normalized:
            raise SDKError("base_url is required")
        self.base_url = normalized
        self.timeout = timeout

    def _build_url(self, path: str, query: dict[str, Any] | None = None) -> str:
        url = f"{self.base_url}{path}"
        if query:
            clean = {k: v for k, v in query.items() if v is not None}
            if clean:
                url += "?" + urllib.parse.urlencode(clean)
        return url

    def _request(
        self,
        method: str,
        path: str,
        *,
        session_id: str | None = None,
        query: dict[str, Any] | None = None,
        headers: dict[str, str] | None = None,
        body: bytes | None = None,
    ) -> _Response:
        return self._dispatch(
            method,
            path,
            session_id=session_id,
            query=query,
            headers=headers,
            body=body,
        )

    def _dispatch(
        self,
        method: str,
        path: str,
        *,
        session_id: str | None = None,
        query: dict[str, Any] | None = None,
        headers: dict[str, str] | None = None,
        body: bytes | None = None,
        form_data: dict[str, str] | None = None,
        files: dict[str, tuple[str, IO[bytes], str]] | None = None,
    ) -> _Response:
        request_headers = {} if headers is None else dict(headers)
        if session_id:
            request_headers[SESSION_HEADER] = session_id
        try:
            resp = httpx.request(
                method,
                self._build_url(path, query),
                headers=request_headers,
                content=body,
                data=form_data,
                files=files,
                timeout=self.timeout,
            )
        except httpx.RequestError as exc:
            raise SDKError(f"http request failed: {exc}") from exc

        if resp.status_code >= 400:
            text = resp.text
            parsed = None
            if text.strip():
                try:
                    parsed = json.loads(text)
                except json.JSONDecodeError:
                    parsed = None
            msg, code = _extract_error_message(parsed, f"http request failed: {resp.status_code}")
            raise SDKError(
                msg,
                http_status=resp.status_code,
                code=code,
                response_text=text or None,
            )

        return _Response(
            status=resp.status_code,
            headers=resp.headers,
            body=resp.content,
        )

    @staticmethod
    def _unwrap_json_result(payload: Any) -> dict[str, Any]:
        if isinstance(payload, dict) and "code" in payload and "msg" in payload:
            code = payload.get("code")
            msg = payload.get("msg", "request failed")
            if code != 200:
                raise SDKError(str(msg), code=code if isinstance(code, int) else None)
            data = payload.get("data")
            if not isinstance(data, dict):
                raise SDKError("response data is empty or invalid", code=200)
            return data
        if isinstance(payload, dict):
            return payload
        raise SDKError("response JSON must be an object")

    def request_json(
        self,
        method: str,
        path: str,
        *,
        session_id: str | None = None,
        query: dict[str, Any] | None = None,
        json_body: dict[str, Any] | None = None,
    ) -> dict[str, Any]:
        body = None
        headers: dict[str, str] = {}
        if json_body is not None:
            body = json.dumps(json_body).encode("utf-8")
            headers["Content-Type"] = "application/json"
        resp = self._request(
            method,
            path,
            session_id=session_id,
            query=query,
            headers=headers,
            body=body,
        )
        payload = _decode_json_bytes(resp.body)
        return self._unwrap_json_result(payload)

    def upload_file(
        self,
        *,
        session_id: str,
        local_file: str,
        target_file_path: str,
    ) -> dict[str, Any]:
        file_name = os.path.basename(local_file)
        guessed_type = mimetypes.guess_type(file_name)[0] or "application/octet-stream"
        with open(local_file, "rb") as fh:
            resp = self._dispatch(
                "POST",
                "/api/code-runner/fs/upload",
                session_id=session_id,
                form_data={"target_file_path": target_file_path},
                files={"file": (file_name, fh, guessed_type)},
            )
        payload = _decode_json_bytes(resp.body)
        return self._unwrap_json_result(payload)

    def download_file(
        self,
        *,
        session_id: str,
        remote_path: str,
    ) -> _Response:
        return self._request(
            "GET",
            "/api/code-runner/fs/download",
            session_id=session_id,
            query={"path": remote_path},
        )
