from __future__ import annotations

import argparse
import sys
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "src"))

from agentland import _mcp_args


class MCPArgsTests(unittest.TestCase):
    @mock.patch.dict("os.environ", {"AGENTLAND_BASE_URL": "http://127.0.0.1:19090"}, clear=False)
    def test_env_base_url(self) -> None:
        self.assertEqual("http://127.0.0.1:19090", _mcp_args.env_base_url())

    @mock.patch.dict("os.environ", {"AGENTLAND_TIMEOUT": "45"}, clear=False)
    def test_env_timeout_valid(self) -> None:
        self.assertEqual(45, _mcp_args.env_timeout())

    @mock.patch.dict("os.environ", {"AGENTLAND_TIMEOUT": "abc"}, clear=False)
    def test_env_timeout_invalid_fallback(self) -> None:
        self.assertEqual(_mcp_args.DEFAULT_TIMEOUT_SECONDS, _mcp_args.env_timeout())

    def test_add_mcp_arguments(self) -> None:
        parser = argparse.ArgumentParser()
        _mcp_args.add_mcp_arguments(
            parser,
            default_base_url="http://127.0.0.1:8080",
            default_timeout=30,
        )
        args = parser.parse_args([])
        self.assertEqual("stdio", args.transport)
        self.assertEqual("http://127.0.0.1:8080", args.base_url)
        self.assertEqual(30, args.timeout)


if __name__ == "__main__":
    unittest.main()
