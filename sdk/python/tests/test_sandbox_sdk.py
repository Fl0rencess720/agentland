from __future__ import annotations

import json
import os
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "src"))

from agentland.sandbox import (
    ExecutionOutput,
    ExecutionResult,
    PreviewLink,
    SDKError,
    Sandbox,
)


class _FakeResponse:
    def __init__(self, *, status_code: int, body: bytes, headers: dict[str, str] | None = None):
        self.status_code = status_code
        self.content = body
        self.headers = {} if headers is None else dict(headers)
        self.text = body.decode("utf-8", errors="replace")


class _FakeStreamResponse:
    def __init__(
        self,
        *,
        status_code: int,
        lines: list[str],
        headers: dict[str, str] | None = None,
        body: bytes = b"",
    ):
        self.status_code = status_code
        self.headers = {} if headers is None else dict(headers)
        self._lines = list(lines)
        self._body = body

    def iter_lines(self):  # type: ignore[no-untyped-def]
        return iter(self._lines)

    def read(self) -> bytes:
        return self._body


class _FakeStreamContext:
    def __init__(self, resp: _FakeStreamResponse):
        self._resp = resp

    def __enter__(self) -> _FakeStreamResponse:
        return self._resp

    def __exit__(self, exc_type, exc, tb):  # type: ignore[no-untyped-def]
        return False


class SandboxSDKTest(unittest.TestCase):
    def setUp(self) -> None:
        Sandbox.configure(base_url="http://127.0.0.1:8080", timeout=5)

    @mock.patch("agentland.sandbox._http.httpx.request")
    def test_create_sandbox_success(self, mock_open: mock.Mock) -> None:
        mock_open.return_value = _FakeResponse(
            status_code=200,
            body=json.dumps(
                {"code": 200, "msg": "success", "data": {"sandbox_id": "session-1"}}
            ).encode("utf-8"),
        )

        sandbox = Sandbox.create()
        self.assertEqual("session-1", sandbox.sandbox_id)

    @mock.patch("agentland.sandbox._http.httpx.request")
    def test_connect_does_not_issue_request(self, mock_open: mock.Mock) -> None:
        sandbox = Sandbox.connect("session-existing")
        self.assertEqual("session-existing", sandbox.sandbox_id)
        mock_open.assert_not_called()

    @mock.patch("agentland.sandbox._http.httpx.request")
    def test_create_preview_success(self, mock_open: mock.Mock) -> None:
        mock_open.return_value = _FakeResponse(
            status_code=200,
            body=json.dumps(
                {
                    "code": 200,
                    "msg": "success",
                    "data": {
                        "session_id": "session-1",
                        "port": 3000,
                        "preview_token": "pv-token",
                        "preview_url": "http://127.0.0.1:8080/p/pv-token/",
                        "expires_at": "2026-03-06T13:00:00Z",
                    },
                }
            ).encode("utf-8"),
        )

        sandbox = Sandbox.connect("session-1")
        preview = sandbox.create_preview(3000, expires_in_seconds=600)

        self.assertIsInstance(preview, PreviewLink)
        self.assertEqual("session-1", preview.session_id)
        self.assertEqual(3000, preview.port)
        self.assertEqual("pv-token", preview.preview_token)
        self.assertEqual("http://127.0.0.1:8080/p/pv-token/", preview.preview_url)

        call = mock_open.call_args
        self.assertEqual("POST", call.args[0])
        self.assertTrue(call.args[1].endswith("/api/previews"))
        self.assertEqual(
            {"port": 3000, "expires_in_seconds": 600},
            json.loads(call.kwargs["content"].decode("utf-8")),
        )

    def test_create_preview_invalid_expiry(self) -> None:
        sandbox = Sandbox.connect("session-1")
        with self.assertRaises(SDKError) as ctx:
            sandbox.create_preview(3000, expires_in_seconds=0)
        self.assertIn("expires_in_seconds", str(ctx.exception))

    @mock.patch("agentland.sandbox._http.httpx.request")
    @mock.patch("agentland.sandbox._http.httpx.stream")
    def test_context_exec_with_raw_payload(self, mock_stream: mock.Mock, mock_open: mock.Mock) -> None:
        captured_stream_kwargs: dict[str, object] = {}

        def _stream_side_effect(*args, **kwargs):  # type: ignore[no-untyped-def]
            captured_stream_kwargs.update(kwargs)
            return _FakeStreamContext(
                _FakeStreamResponse(
                    status_code=200,
                    headers={"Content-Type": "text/event-stream"},
                    lines=[
                        "data: {\"type\":\"init\",\"timestamp\":1,\"context_id\":\"ctx-1\",\"execution_id\":\"exec-1\"}",
                        "data: {\"type\":\"stdout\",\"timestamp\":2,\"context_id\":\"ctx-1\",\"execution_id\":\"exec-1\",\"text\":\"ok\\n\"}",
                        "data: {\"type\":\"count\",\"timestamp\":2,\"context_id\":\"ctx-1\",\"execution_id\":\"exec-1\",\"execution_count\":1}",
                        "data: {\"type\":\"execution_complete\",\"timestamp\":3,\"context_id\":\"ctx-1\",\"execution_id\":\"exec-1\",\"execution_time\":3,\"exit_code\":0}",
                    ],
                )
            )

        request_responses = [
            _FakeResponse(
                status_code=200,
                body=json.dumps({"context_id": "ctx-1"}).encode("utf-8"),
            ),
            _FakeResponse(
                status_code=200,
                body=json.dumps({"context_id": "ctx-1"}).encode("utf-8"),
            ),
        ]
        mock_open.side_effect = request_responses
        mock_stream.side_effect = _stream_side_effect

        sandbox = Sandbox.connect("session-1")
        ctx = sandbox.context.create(language="python", cwd="/workspace")
        self.assertEqual("ctx-1", ctx.context_id)
        out = ctx.exec("print('ok')", timeout_ms=30000)
        self.assertIsInstance(out, ExecutionResult)
        self.assertEqual("exec-1", out.execution_id)
        self.assertEqual("ctx-1", out.context_id)
        self.assertEqual(1, out.execution_count)
        self.assertEqual(0, out.exit_code)
        self.assertEqual("ok\n", out.stdout)
        self.assertEqual("", out.stderr)
        self.assertEqual(3, out.duration_ms)
        with self.assertRaises(TypeError):
            _ = out["stdout"]  # type: ignore[index]
        with self.assertRaises(AttributeError):
            _ = out.get("stdout", "")  # type: ignore[attr-defined]
        stream_json = captured_stream_kwargs.get("json")
        self.assertEqual({"code": "print('ok')", "timeout_ms": 30000}, stream_json)
        self.assertNotIn("content", captured_stream_kwargs)
        deleted = ctx.delete()
        self.assertEqual("ctx-1", deleted["context_id"])

    @mock.patch("agentland.sandbox._http.httpx.request")
    def test_get_execution_output(self, mock_open: mock.Mock) -> None:
        request_responses = [
            _FakeResponse(
                status_code=200,
                body=json.dumps({"context_id": "ctx-1"}).encode("utf-8"),
            ),
            _FakeResponse(
                status_code=200,
                body=json.dumps(
                    {
                        "code": 200,
                        "msg": "success",
                        "data": {
                            "execution_id": "exec-1",
                            "context_id": "ctx-1",
                            "state": "running",
                            "execution_count": 2,
                            "stdout": "line1\n",
                            "stderr": "",
                            "duration_ms": 12,
                        },
                    }
                ).encode("utf-8"),
            ),
        ]
        mock_open.side_effect = request_responses

        sandbox = Sandbox.connect("session-1")
        ctx = sandbox.context.create(language="python", cwd="/workspace")
        out = ctx.get_output("exec-1")

        self.assertIsInstance(out, ExecutionOutput)
        self.assertEqual("exec-1", out.execution_id)
        self.assertEqual("ctx-1", out.context_id)
        self.assertEqual("running", out.state)
        self.assertEqual(2, out.execution_count)
        self.assertEqual("line1\n", out.stdout)
        self.assertEqual("", out.stderr)
        self.assertIsNone(out.exit_code)

        last_call = mock_open.call_args_list[-1]
        self.assertEqual("GET", last_call.args[0])
        self.assertTrue(last_call.args[1].endswith("/api/code-runner/contexts/ctx-1/executions/exec-1/output"))

    @mock.patch("agentland.sandbox._http.httpx.request")
    def test_upload_uses_local_path_and_multipart(self, mock_open: mock.Mock) -> None:
        captured_request: dict[str, object] = {}

        def _side_effect(method, url, **kwargs):  # type: ignore[no-untyped-def]
            captured_request["method"] = method
            captured_request["url"] = url
            captured_request["kwargs"] = kwargs
            files = kwargs["files"]
            file_info = files["file"]
            captured_request["uploaded_content"] = file_info[1].read().decode("utf-8")
            return _FakeResponse(
                status_code=200,
                body=json.dumps(
                    {
                        "code": 200,
                        "msg": "success",
                        "data": {
                            "source_path": "data.csv",
                            "target_path": "/workspace/data.csv",
                            "size": 12,
                        },
                    }
                ).encode("utf-8"),
            )

        mock_open.side_effect = _side_effect

        sandbox = Sandbox.connect("session-1")
        with tempfile.TemporaryDirectory() as td:
            local_file = os.path.join(td, "data.csv")
            Path(local_file).write_text("name,value\n", encoding="utf-8")
            out = sandbox.fs.upload(local_file, "/workspace/data.csv")

        self.assertEqual("/workspace/data.csv", out["target_path"])
        self.assertEqual("POST", captured_request["method"])
        kwargs = captured_request["kwargs"]
        self.assertIsInstance(kwargs, dict)
        files = kwargs["files"]
        self.assertIsInstance(files, dict)
        self.assertIn("file", files)
        file_info = files["file"]
        self.assertEqual("data.csv", file_info[0])
        self.assertEqual("text/csv", file_info[2])
        self.assertEqual("name,value\n", captured_request["uploaded_content"])
        data = kwargs["data"]
        self.assertEqual("/workspace/data.csv", data["target_file_path"])

    @mock.patch("agentland.sandbox._http.httpx.request")
    def test_download_saves_local_file(self, mock_open: mock.Mock) -> None:
        mock_open.return_value = _FakeResponse(
            status_code=200,
            body=b"id,score\n1,100\n",
            headers={
                "Content-Disposition": 'attachment; filename="result.csv"',
                "X-Agentland-File-Path": "/workspace/result.csv",
            },
        )

        sandbox = Sandbox.connect("session-1")
        with tempfile.TemporaryDirectory() as td:
            save_path = os.path.join(td, "nested", "result.csv")
            out = sandbox.fs.download("/workspace/result.csv", save_path)
            content = Path(save_path).read_bytes()

        self.assertEqual(b"id,score\n1,100\n", content)
        self.assertEqual("/workspace/result.csv", out["source_path"])
        self.assertEqual("result.csv", out["file_name"])
        self.assertGreater(out["size"], 0)

    @mock.patch("agentland.sandbox._http.httpx.request")
    def test_http_error_raises_sdk_error(self, mock_open: mock.Mock) -> None:
        mock_open.return_value = _FakeResponse(
            status_code=400,
            body=json.dumps({"code": 1, "msg": "Form Error"}).encode("utf-8"),
        )

        with self.assertRaises(SDKError) as ctx:
            Sandbox.create()
        self.assertEqual(400, ctx.exception.http_status)
        self.assertEqual(1, ctx.exception.code)


if __name__ == "__main__":
    unittest.main()
