from __future__ import annotations

import io
import json
import os
import sys
import tempfile
import unittest
import urllib.error
from email.message import Message
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "src"))

from agentland.sandbox import SDKError, Sandbox


class _FakeResponse:
    def __init__(self, *, status: int, body: bytes, headers: dict[str, str] | None = None):
        self.status = status
        self._body = body
        self.headers = Message()
        for key, value in (headers or {}).items():
            self.headers[key] = value

    def read(self) -> bytes:
        return self._body

    def __enter__(self) -> _FakeResponse:
        return self

    def __exit__(self, exc_type, exc, tb) -> None:
        return None


class SandboxSDKTest(unittest.TestCase):
    def setUp(self) -> None:
        Sandbox.configure(base_url="http://127.0.0.1:8080", timeout=5)

    @mock.patch("agentland.sandbox._http.urllib.request.urlopen")
    def test_create_sandbox_success(self, mock_open: mock.Mock) -> None:
        mock_open.return_value = _FakeResponse(
            status=200,
            body=json.dumps(
                {"code": 200, "msg": "success", "data": {"sandbox_id": "session-1"}}
            ).encode("utf-8"),
        )

        sandbox = Sandbox.create(language="python")
        self.assertEqual("session-1", sandbox.sandbox_id)

    @mock.patch("agentland.sandbox._http.urllib.request.urlopen")
    def test_connect_does_not_issue_request(self, mock_open: mock.Mock) -> None:
        sandbox = Sandbox.connect("session-existing")
        self.assertEqual("session-existing", sandbox.sandbox_id)
        mock_open.assert_not_called()

    @mock.patch("agentland.sandbox._http.urllib.request.urlopen")
    def test_context_exec_with_raw_payload(self, mock_open: mock.Mock) -> None:
        responses = [
            _FakeResponse(
                status=200,
                body=json.dumps({"context_id": "ctx-1"}).encode("utf-8"),
            ),
            _FakeResponse(
                status=200,
                body=json.dumps(
                    {
                        "context_id": "ctx-1",
                        "execution_count": 1,
                        "exit_code": 0,
                        "stdout": "ok\n",
                        "stderr": "",
                        "duration_ms": 3,
                    }
                ).encode("utf-8"),
            ),
            _FakeResponse(
                status=200,
                body=json.dumps({"context_id": "ctx-1"}).encode("utf-8"),
            ),
        ]
        mock_open.side_effect = responses

        sandbox = Sandbox.connect("session-1")
        ctx = sandbox.context.create(language="python", cwd="/workspace")
        self.assertEqual("ctx-1", ctx.context_id)
        out = ctx.exec("print('ok')", timeout_ms=30000)
        self.assertEqual(0, out["exit_code"])
        self.assertEqual("ok\n", out["stdout"])
        deleted = ctx.delete()
        self.assertEqual("ctx-1", deleted["context_id"])

    @mock.patch("agentland.sandbox._http.urllib.request.urlopen")
    def test_upload_uses_local_path_and_multipart(self, mock_open: mock.Mock) -> None:
        captured_request = {}

        def _side_effect(req, timeout=None):  # type: ignore[no-untyped-def]
            captured_request["headers"] = dict(req.header_items())
            captured_request["body"] = req.data
            return _FakeResponse(
                status=200,
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
        content_type = captured_request["headers"].get("Content-type", "")
        self.assertIn("multipart/form-data", content_type)
        body = captured_request["body"]
        self.assertIsInstance(body, bytes)
        self.assertIn(b'target_file_path"\r\n\r\n/workspace/data.csv', body)
        self.assertIn(b'name="file"; filename="data.csv"', body)

    @mock.patch("agentland.sandbox._http.urllib.request.urlopen")
    def test_download_saves_local_file(self, mock_open: mock.Mock) -> None:
        mock_open.return_value = _FakeResponse(
            status=200,
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

    @mock.patch("agentland.sandbox._http.urllib.request.urlopen")
    def test_http_error_raises_sdk_error(self, mock_open: mock.Mock) -> None:
        payload = io.BytesIO(json.dumps({"code": 1, "msg": "Form Error"}).encode("utf-8"))
        mock_open.side_effect = urllib.error.HTTPError(
            url="http://127.0.0.1:8080/api/code-runner/sandboxes",
            code=400,
            msg="Bad Request",
            hdrs={},
            fp=payload,
        )

        with self.assertRaises(SDKError) as ctx:
            Sandbox.create("python")
        self.assertEqual(400, ctx.exception.http_status)
        self.assertEqual(1, ctx.exception.code)


if __name__ == "__main__":
    unittest.main()

