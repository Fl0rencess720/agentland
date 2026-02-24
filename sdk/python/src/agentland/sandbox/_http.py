"""Internal HTTP client for sandbox APIs."""

from __future__ import annotations

import json
import mimetypes
import os
import urllib.error
import urllib.parse
import urllib.request
import uuid
from dataclasses import dataclass
from email.message import Message
from typing import Any

from .errors import SDKError

SESSION_HEADER = "x-agentland-session"


@dataclass(slots=True)
class _Response:
    status: int
    headers: Message
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
        request_headers = {} if headers is None else dict(headers)
        if session_id:
            request_headers[SESSION_HEADER] = session_id
        req = urllib.request.Request(
            self._build_url(path, query),
            data=body,
            headers=request_headers,
            method=method,
        )
        try:
            with urllib.request.urlopen(req, timeout=self.timeout) as resp:
                return _Response(
                    status=resp.status,
                    headers=resp.headers,
                    body=resp.read(),
                )
        except urllib.error.HTTPError as exc:
            payload = exc.read()
            text = payload.decode("utf-8", errors="replace")
            parsed = None
            if text.strip():
                try:
                    parsed = json.loads(text)
                except json.JSONDecodeError:
                    parsed = None
            msg, code = _extract_error_message(parsed, f"http request failed: {exc.code}")
            raise SDKError(
                msg,
                http_status=exc.code,
                code=code,
                response_text=text or None,
            ) from exc
        except urllib.error.URLError as exc:
            raise SDKError(f"http request failed: {exc.reason}") from exc

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
        boundary = "----agentland-" + uuid.uuid4().hex

        with open(local_file, "rb") as fh:
            file_bytes = fh.read()

        body = (
            f"--{boundary}\r\n"
            'Content-Disposition: form-data; name="target_file_path"\r\n\r\n'
            f"{target_file_path}\r\n"
            f"--{boundary}\r\n"
            f'Content-Disposition: form-data; name="file"; filename="{file_name}"\r\n'
            f"Content-Type: {guessed_type}\r\n\r\n"
        ).encode("utf-8") + file_bytes + f"\r\n--{boundary}--\r\n".encode("utf-8")

        headers = {"Content-Type": f"multipart/form-data; boundary={boundary}"}
        resp = self._request(
            "POST",
            "/api/code-runner/fs/upload",
            session_id=session_id,
            headers=headers,
            body=body,
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

