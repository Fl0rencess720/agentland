from __future__ import annotations

import io
import sys
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "src"))

from agentland.mcp.bridge import CodeInterpreterToolBridge


class _FakeContext:
    def __init__(self, *, context_id: str = "ctx-1") -> None:
        self.context_id = context_id

    def exec(self, code: str, timeout_ms: int = 30000) -> dict:
        return {
            "execution_count": 1,
            "exit_code": 0,
            "stdout": "ok\n",
            "stderr": "",
            "duration_ms": 5,
        }

    def delete(self) -> dict:
        return {"context_id": self.context_id}


class _FakeContextService:
    def __init__(self) -> None:
        self.created = []
        self.ctx = _FakeContext()

    def create(self, language: str = "python", cwd: str = "/workspace") -> _FakeContext:
        self.created.append({"language": language, "cwd": cwd})
        return self.ctx


class _FakeFSService:
    def __init__(self) -> None:
        self.calls = []

    def tree(self, **kwargs) -> dict:
        self.calls.append(("tree", kwargs))
        return {"root": kwargs.get("path", "."), "nodes": []}

    def read(self, **kwargs) -> dict:
        self.calls.append(("read", kwargs))
        return {
            "path": kwargs["path"],
            "size": 3,
            "encoding": kwargs.get("encoding", "utf8"),
            "content": "abc",
        }

    def write(self, **kwargs) -> dict:
        self.calls.append(("write", kwargs))
        return {
            "path": kwargs["path"],
            "size": len(kwargs["content"]),
            "encoding": kwargs.get("encoding", "utf8"),
        }


class _FakeSandbox:
    configured = None
    create_calls = []
    connect_calls = []
    last = None

    def __init__(self, sandbox_id: str) -> None:
        self.sandbox_id = sandbox_id
        self.context = _FakeContextService()
        self.fs = _FakeFSService()

    @classmethod
    def configure(cls, *, base_url: str, timeout: int) -> None:
        cls.configured = {"base_url": base_url, "timeout": timeout}

    @classmethod
    def create(cls, language: str = "python") -> _FakeSandbox:
        cls.create_calls.append(language)
        cls.last = _FakeSandbox("session-created")
        return cls.last

    @classmethod
    def connect(cls, sandbox_id: str) -> _FakeSandbox:
        cls.connect_calls.append(sandbox_id)
        cls.last = _FakeSandbox(sandbox_id)
        return cls.last


class _ImmediateThread:
    def __init__(self, target, daemon: bool = False) -> None:  # type: ignore[no-untyped-def]
        self._target = target
        self.daemon = daemon

    def start(self) -> None:
        self._target()


class MCPBridgeTests(unittest.TestCase):
    @mock.patch("agentland.mcp.bridge.Sandbox", _FakeSandbox)
    def test_sandbox_create_default_language(self) -> None:
        bridge = CodeInterpreterToolBridge(base_url="http://127.0.0.1:8080", timeout=30)
        out = bridge.sandbox_create(language="")
        self.assertEqual({"sandbox_id": "session-created"}, out)
        self.assertEqual("python", _FakeSandbox.create_calls[-1])

    @mock.patch("agentland.mcp.bridge.Sandbox", _FakeSandbox)
    def test_code_execute_and_async_cleanup(self) -> None:
        bridge = CodeInterpreterToolBridge(base_url="http://127.0.0.1:8080", timeout=30)
        cleanup_called = {"ok": False}

        def _cleanup(context):  # type: ignore[no-untyped-def]
            cleanup_called["ok"] = True
            context.delete()

        with mock.patch.object(bridge, "_delete_context_async", side_effect=_cleanup):
            out = bridge.code_execute(
                sandbox_id="session-1",
                code="print(1)",
                language="python",
                cwd="/workspace",
                timeout_ms=20000,
            )

        self.assertEqual(0, out["exit_code"])
        self.assertEqual("ctx-1", out["context_id"])
        self.assertTrue(cleanup_called["ok"])

    @mock.patch("agentland.mcp.bridge.Sandbox", _FakeSandbox)
    def test_fs_tree_optional_depth(self) -> None:
        bridge = CodeInterpreterToolBridge(base_url="http://127.0.0.1:8080", timeout=30)
        bridge.fs_tree(sandbox_id="session-1", path="", depth=0, includeHidden=True)
        method, kwargs = _FakeSandbox.last.fs.calls[-1]
        self.assertEqual("tree", method)
        self.assertEqual(".", kwargs["path"])
        self.assertEqual(True, kwargs["include_hidden"])
        self.assertNotIn("depth", kwargs)

    @mock.patch("agentland.mcp.bridge.Sandbox", _FakeSandbox)
    def test_missing_sandbox_id(self) -> None:
        bridge = CodeInterpreterToolBridge(base_url="http://127.0.0.1:8080", timeout=30)
        with self.assertRaises(ValueError):
            bridge.fs_file_get(sandbox_id=" ", path="/workspace/a.txt")

    @mock.patch("agentland.mcp.bridge.Thread", _ImmediateThread)
    def test_async_delete_logs_unexpected_error(self) -> None:
        bridge = CodeInterpreterToolBridge(base_url="http://127.0.0.1:8080", timeout=30)

        class _BrokenContext:
            def delete(self) -> None:
                raise RuntimeError("boom")

        stderr = io.StringIO()
        with mock.patch("agentland.mcp.bridge.sys.stderr", stderr):
            bridge._delete_context_async(_BrokenContext())
        self.assertIn("Unexpected error during async context deletion: boom", stderr.getvalue())


if __name__ == "__main__":
    unittest.main()
